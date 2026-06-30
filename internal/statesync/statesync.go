// Package statesync mirrors the handful of `aws s3 sync` / `aws cloudfront
// create-invalidation` calls that entrypoint.sh used to shell out to, so the
// runtime image no longer needs the ~120MB awscli (python) package. It is
// isolated here — like internal/cachestore/s3store — so the aws-sdk-go-v2
// dependency stays contained.
//
// The sync semantics are deliberately simpler than the real `aws s3 sync`: it
// uploads/downloads every object rather than diffing by size+mtime. Every path
// it handles (the chromedp session cookie, the claims ledger, backtest
// snapshots, and the two static sites) is small, and the static-site publishes
// already invalidate the whole CloudFront distribution, so a full copy each run
// is cheap and keeps the reconciliation logic auditable.
package statesync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3API is the slice of the S3 client this package needs (fakeable in tests).
type s3API interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// cfAPI is the slice of the CloudFront client this package needs.
type cfAPI interface {
	CreateInvalidation(context.Context, *cloudfront.CreateInvalidationInput, ...func(*cloudfront.Options)) (*cloudfront.CreateInvalidationOutput, error)
}

// Syncer copies bytes between a local directory tree and an S3 bucket/prefix
// and invalidates CloudFront distributions.
type Syncer struct {
	s3 s3API
	cf cfAPI
}

// New builds a Syncer using the default AWS credential/region chain.
func New(ctx context.Context) (*Syncer, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Syncer{s3: s3.NewFromConfig(cfg), cf: cloudfront.NewFromConfig(cfg)}, nil
}

// Down copies every object under bucket/prefix into localDir, recreating the
// key's path (minus prefix) under localDir. Existing local files are
// overwritten. A missing prefix (no objects) is a no-op.
func (s *Syncer) Down(ctx context.Context, bucket, prefix, localDir string) error {
	keys, err := s.list(ctx, bucket, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue // the prefix "folder" placeholder, nothing to write
		}
		dst := filepath.Join(localDir, filepath.FromSlash(rel))
		if err := s.download(ctx, bucket, key, dst); err != nil {
			return fmt.Errorf("download %s: %w", key, err)
		}
	}
	return nil
}

// Up uploads every file under localDir to bucket/prefix, mapping each file's
// path (relative to localDir) onto the key. When del is true it then removes
// remote objects under prefix that no longer have a local counterpart, matching
// `aws s3 sync --delete`. A non-existent localDir is a no-op.
func (s *Syncer) Up(ctx context.Context, bucket, prefix, localDir string, del bool) error {
	if _, err := os.Stat(localDir); os.IsNotExist(err) {
		return nil
	}
	// uploaded holds the set of keys we just wrote, used for --delete reconciliation.
	uploaded := map[string]bool{}
	err := filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		key := prefix + filepath.ToSlash(rel)
		if err := s.upload(ctx, bucket, key, path); err != nil {
			return fmt.Errorf("upload %s: %w", path, err)
		}
		uploaded[key] = true
		return nil
	})
	if err != nil {
		return err
	}
	if !del {
		return nil
	}
	return s.deleteOrphans(ctx, bucket, prefix, uploaded)
}

// deleteOrphans removes every object under bucket/prefix that is not in the
// keep set. This is the destructive half of an `--delete` sync, so it is kept
// small and explicit: it only ever deletes keys it actually saw remotely, and
// only those absent from the just-uploaded set.
func (s *Syncer) deleteOrphans(ctx context.Context, bucket, prefix string, keep map[string]bool) error {
	remote, err := s.list(ctx, bucket, prefix)
	if err != nil {
		return err
	}
	for _, key := range remote {
		if keep[key] {
			continue
		}
		if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: &bucket, Key: aws.String(key),
		}); err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
	}
	return nil
}

// Invalidate creates a CloudFront invalidation for all paths ("/*") on distID.
func (s *Syncer) Invalidate(ctx context.Context, distID string) error {
	ref := strconv.FormatInt(time.Now().UnixNano(), 10)
	_, err := s.cf.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{
		DistributionId: &distID,
		InvalidationBatch: &cftypes.InvalidationBatch{
			CallerReference: &ref,
			Paths: &cftypes.Paths{
				Quantity: aws.Int32(1),
				Items:    []string{"/*"},
			},
		},
	})
	return err
}

// list returns every object key under bucket/prefix, paginating as needed.
func (s *Syncer) list(ctx context.Context, bucket, prefix string) ([]string, error) {
	var keys []string
	var token *string
	for {
		out, err := s.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &bucket, Prefix: &prefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return keys, nil
}

func (s *Syncer) download(ctx context.Context, bucket, key, dst string) error {
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, out.Body)
	return err
}

func (s *Syncer) upload(ctx context.Context, bucket, key, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = s.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: f})
	return err
}

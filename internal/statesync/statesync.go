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
	"errors"
	"fmt"
	"io"
	"mime"
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
// overwritten. A missing prefix (no objects) downloads nothing but still
// creates localDir: downstream writers assume the directory exists —
// auth_client writes its cookie cache with a plain open, which fails ENOENT on
// a missing parent — and the one case where nothing has ever been synced is
// exactly a new tenant's FIRST connect, where that write is the whole point
// (TestDown_EmptyPrefixStillCreatesTheLocalDir).
func (s *Syncer) Down(ctx context.Context, bucket, prefix, localDir string) error {
	keys, err := s.list(ctx, bucket, prefix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		return err
	}
	root, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	// Ranging the map's keys: list now reports sizes too, which Down has no use
	// for. Iteration order is therefore random, which is fine here — each key
	// writes an independent file — but it is why nothing below may depend on
	// encounter order.
	for key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue // the prefix "folder" placeholder, nothing to write
		}
		dst := filepath.Join(localDir, filepath.FromSlash(rel))
		// Defense-in-depth: a crafted key ("../etc/...") must not let a write
		// escape localDir, even though the source bucket is ours.
		abs, err := filepath.Abs(dst)
		if err != nil {
			return err
		}
		if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			return fmt.Errorf("refusing key %q: escapes %s", key, localDir)
		}
		if err := s.download(ctx, bucket, key, dst); err != nil {
			return fmt.Errorf("download %s: %w", key, err)
		}
	}
	return nil
}

// UpOptions carries the two behaviours that differ between Up's call sites.
// They are a struct rather than positional bools because `Up(ctx, b, p, d,
// false, true)` says nothing at the call site about which knob is which.
type UpOptions struct {
	// Delete removes remote objects under prefix that no longer have a local
	// counterpart, matching `aws s3 sync --delete`.
	Delete bool
	// SkipUnchanged suppresses the upload of any file whose size already
	// matches the remote object's. See Up's comment for why this is opt-in
	// per call site rather than the default.
	SkipUnchanged bool
}

// Up uploads files under localDir to bucket/prefix, mapping each file's path
// (relative to localDir) onto the key. A non-existent localDir is a no-op.
//
// This used to upload EVERY file unconditionally, which the doc comment
// described as "matching `aws s3 sync --delete`" — but aws s3 sync skips files
// it judges unchanged, and that half of the semantics was lost when
// internal/statesync replaced awscli (to drop ~120MB of python from the image).
// The result: entrypoint.sh runs sync_up after every task, so every task
// rewrote every file in every synced tree. With bucket versioning on and a
// NoncurrentVersionExpiration rule that covered only cache/, the archive/
// prefix reached 486,877 versions / 635 GB against 679 live objects / 877 MB
// (measured 2026-08-18). One immutable July 2 snapshot,
// archive/hkb/dt=2026-07-02/rankings.html, held 1,363 versions occupying
// 3.21 GB and was still being rewritten six weeks later.
//
// SkipUnchanged is OPT-IN PER CALL SITE, and that is the load-bearing part.
// The comparison is size-only — no checksum — so it cannot see a content change
// that preserves length, and two of the four synced trees are full of exactly
// that shape. session/.fantrax_cookie_cache.json is a fixed-layout JSON holding
// a fixed-length session token and an ISO timestamp: three of the four live
// tenant copies are byte-identical in length (3947, 3947, 3947), so a REFRESHED
// cookie almost certainly serialises to the same size as the stale one it
// replaces. Skipping it would pin every tenant to a dead cookie and silently
// force a chromedp browser login on every task — a worse failure than the cost
// it saves, and an invisible one. The claims cursor (.waivers/last-claims.json,
// a fixed-shape timestamp) has the same hazard.
//
// So callers enable this only for trees whose objects are immutable once
// written. Today that is .archive/ alone, which is also ~635 of the 636 GB, so
// the narrow scope costs nothing measurable: the remaining trees total under
// 1 MB and re-uploading them each run is ~60 PUTs.
func (s *Syncer) Up(ctx context.Context, bucket, prefix, localDir string, opts UpOptions) error {
	if _, err := os.Stat(localDir); os.IsNotExist(err) {
		return nil
	}
	// One listing up front, reused for every skip decision — the alternative is
	// a HeadObject per file, which trades the PUTs we are removing for an equal
	// number of HEADs. Populated only when skipping is on, so the non-skipping
	// call sites pay nothing. A listing failure is not fatal: it degrades to
	// uploading everything, which is exactly the old behaviour, because being
	// slow and correct beats silently not uploading a changed file.
	var remoteSize map[string]int64
	if opts.SkipUnchanged {
		sizes, err := s.list(ctx, bucket, prefix)
		if err != nil {
			remoteSize = nil
		} else {
			remoteSize = sizes
		}
	}
	// uploaded holds every key that should exist remotely after this call —
	// both the ones written and the ones skipped as already-current. Skipped
	// keys MUST be in here: it is the keep-set for --delete reconciliation, and
	// omitting them would make a skipped file look like an orphan and delete it.
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
		uploaded[key] = true
		if sz, ok := remoteSize[key]; ok && sz == info.Size() {
			return nil
		}
		if err := s.upload(ctx, bucket, key, path); err != nil {
			return fmt.Errorf("upload %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !opts.Delete {
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
	for key := range remote {
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

// list returns every object key under bucket/prefix mapped to its size,
// paginating as needed. Size rides along because Up's skip decision needs it
// and a second listing to fetch it would cost exactly what the skip saves; a
// nil Size (which the API models as optional) is reported as -1 so it can never
// compare equal to a real file length and accidentally suppress an upload.
func (s *Syncer) list(ctx context.Context, bucket, prefix string) (map[string]int64, error) {
	sizes := map[string]int64{}
	var token *string
	for {
		out, err := s.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &bucket, Prefix: &prefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if o.Key == nil {
				continue
			}
			if o.Size == nil {
				sizes[*o.Key] = -1
				continue
			}
			sizes[*o.Key] = *o.Size
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return sizes, nil
}

func (s *Syncer) download(ctx context.Context, bucket, key, dst string) error {
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	// 0700/0600: Down targets include the Fantrax session cookie (a credential);
	// keep state owner-only.
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// Close is checked rather than deferred-and-dropped because this is a
	// *write*: the final flush happens inside Close, so a full disk or a failing
	// volume surfaces there and nowhere else. Discarding it would let download
	// return nil having left a silently truncated file — and one of the things
	// this syncs down is the Fantrax session cookie, where a half-written file
	// is a login loop rather than an obvious failure.
	if _, cerr := io.Copy(f, out.Body); cerr != nil {
		// Joined, not dropped. The copy error is the cause and errors.Join keeps
		// it first, but a Close that also fails is a second, independent fact
		// about the volume — and on this path the volume is exactly what is
		// under suspicion, so it is the worst possible moment to throw it away.
		return errors.Join(cerr, f.Close())
	}
	return f.Close()
}

func (s *Syncer) upload(ctx context.Context, bucket, key, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	in := &s3.PutObjectInput{Bucket: &bucket, Key: &key, Body: f}
	// awscli set Content-Type from the file extension; without it S3 serves
	// application/octet-stream and browsers download the recap/report HTML
	// instead of rendering it. Restore ext-based detection.
	if ct := mime.TypeByExtension(filepath.Ext(src)); ct != "" {
		in.ContentType = &ct
	}
	_, err = s.s3.PutObject(ctx, in)
	return err
}

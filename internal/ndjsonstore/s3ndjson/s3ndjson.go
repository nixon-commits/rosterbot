// Package s3ndjson is the S3 adapter for ndjsonstore.Store. It is the only
// place the AWS SDK enters the NDJSON store stack, mirroring how
// internal/cachestore/s3store keeps the SDK out of internal/cache.
package s3ndjson

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

type api interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Store implements ndjsonstore.Store against an S3 bucket. Keys handed to and
// returned from Store are relative to prefix; the bucket prefix is joined and
// stripped here so callers never see it (key-derived fields like the Analysis
// Store's system= segment then parse the same way locally and on S3).
type Store struct {
	client api
	bucket string
	prefix string
}

// New constructs a Store using the default AWS config (env vars / ~/.aws/).
// prefix should end in "/", e.g. "analysis/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Store{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *Store) Put(key string, b []byte) error {
	full := s.prefix + key
	_, err := s.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &full, Body: bytes.NewReader(b),
	})
	return err
}

func (s *Store) Get(key string) ([]byte, error) {
	full := s.prefix + key
	obj, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &s.bucket, Key: &full,
	})
	if err != nil {
		return nil, err
	}
	defer obj.Body.Close()
	return io.ReadAll(obj.Body)
}

// List pages through every object under prefix and returns prefix-relative keys.
func (s *Store) List(prefix string) ([]string, error) {
	ctx := context.Background()
	full := s.prefix + prefix

	var keys []string
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &s.bucket, Prefix: &full, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if o.Key == nil {
				continue
			}
			keys = append(keys, strings.TrimPrefix(*o.Key, s.prefix))
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	return keys, nil
}

// Compile-time proof that *Store satisfies ndjsonstore.Store.
var _ ndjsonstore.Store = (*Store)(nil)

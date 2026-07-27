// Package s3ndjson is the S3 adapter for ndjsonstore.Store. It keeps the AWS SDK
// out of the NDJSON store stack, mirroring how internal/cachestore/s3store keeps
// it out of internal/cache; the SDK plumbing both share lives in
// internal/s3blob.
package s3ndjson

import (
	"context"
	"fmt"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// Store implements ndjsonstore.Store against an S3 bucket. Keys handed to and
// returned from Store are relative to prefix, so key-derived fields (the
// Analysis Store's system= segment) parse the same way locally and on S3.
type Store struct{ blob *s3blob.Blob }

// New constructs a Store using the default AWS config (env vars / ~/.aws/).
// prefix should end in "/", e.g. "analysis/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &Store{blob: b}, nil
}

// ndjsonstore.Store carries no context — the partition walk is synchronous — so
// the background context is supplied here.
func (s *Store) Put(key string, b []byte) error {
	return s.blob.Put(context.Background(), key, b)
}

// Get reads one partition. Unlike the cache, a missing object here is an error:
// ReadAll only ever Gets keys it has just listed, so absence means the partition
// vanished mid-walk rather than "not cached yet".
func (s *Store) Get(key string) ([]byte, error) {
	b, found, err := s.blob.Get(context.Background(), key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("s3ndjson: %s: no such object", s.blob.Key(key))
	}
	return b, nil
}

// List returns every key under prefix, relative to the bucket prefix.
func (s *Store) List(prefix string) ([]string, error) {
	return s.blob.List(context.Background(), prefix)
}

// Compile-time proof that *Store satisfies ndjsonstore.Store.
var _ ndjsonstore.Store = (*Store)(nil)

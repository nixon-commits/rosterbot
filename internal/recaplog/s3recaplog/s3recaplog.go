// Package s3recaplog is the S3 adapter for recaplog.Store. It keeps the AWS SDK
// out of the internal/recaplog leaf, the same arrangement as
// internal/cachestore/s3store and internal/ndjsonstore/s3ndjson, and like them
// it is a thin shim over the shared internal/s3blob.
//
// Read-only by design: CloudFront is the only writer to this prefix, so the
// adapter exposes no Put and the task role needs no write permission on the log
// bucket.
package s3recaplog

import (
	"context"
	"fmt"

	"github.com/nixon-commits/rosterbot/internal/recaplog"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// Store implements recaplog.Store against the recap access-log bucket.
type Store struct{ blob *s3blob.Blob }

// New constructs a Store using the default AWS config. prefix should end in
// "/", e.g. "recap/" — matching the LogFilePrefix set on the SiteCdn
// distribution.
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &Store{blob: b}, nil
}

// recaplog.Store carries no context — the read is a synchronous batch job — so
// the background context is supplied here.
func (s *Store) List(prefix string) ([]string, error) {
	return s.blob.List(context.Background(), prefix)
}

// Get reads one log object. A miss is an error rather than a silent empty
// result: ReadRecent only ever Gets keys it has just listed, so absence means
// the object was expired or deleted mid-read, not that there is no data.
func (s *Store) Get(key string) ([]byte, error) {
	b, found, err := s.blob.Get(context.Background(), key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("s3recaplog: %s: no such object", s.blob.Key(key))
	}
	return b, nil
}

// Compile-time proof that *Store satisfies recaplog.Store.
var _ recaplog.Store = (*Store)(nil)

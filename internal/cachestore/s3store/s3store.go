// Package s3store is the S3 adapter for cache.Store. It is isolated here so the
// aws-sdk-go-v2 dependency stays out of the zero-dep internal/cache leaf; the
// SDK plumbing itself lives one layer down in internal/s3blob, shared with the
// NDJSON and lineup-API adapters.
package s3store

import (
	"context"

	cache "github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// Store implements cache.Store against an S3 bucket+prefix, one object per key.
type Store struct{ blob *s3blob.Blob }

// New builds a Store using the default AWS credential/region chain. prefix
// should end in "/", e.g. "cache/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &Store{blob: b}, nil
}

// objKey appends the .json suffix the local fsStore also uses, so a listing of
// the cache/ prefix reads like a listing of the local .cache directory.
func objKey(key string) string { return key + ".json" }

// cache.Store carries no context — the interface predates it and the callers are
// synchronous cache reads — so the background context is supplied here.
func (s *Store) Get(key string) ([]byte, bool, error) {
	return s.blob.Get(context.Background(), objKey(key))
}

func (s *Store) Put(key string, data []byte) error {
	return s.blob.Put(context.Background(), objKey(key), data)
}

func (s *Store) Remove(key string) error {
	return s.blob.Delete(context.Background(), objKey(key))
}

// Compile-time assertion: *Store must satisfy cache.Store.
var _ cache.Store = (*Store)(nil)

// Package s3lineup is the S3 adapter for the lineup API's stores: published
// lineups, the run ledger, per-run output and progress, the notification feed,
// the WebAuthn identity record and the infra lister. It is isolated here (like
// internal/cachestore/s3store) so the aws-sdk-go-v2 dependency stays out of the
// lineupapi leaf, which the Lambda handler and cmd both import.
//
// Every store here is a thin shim over an internal/s3blob Blob: the shim owns
// its key layout and its interface shape, and the Blob owns the SDK plumbing —
// credential resolution, prefix joining, not-found mapping and list pagination —
// shared with s3store and s3ndjson.
package s3lineup

import (
	"context"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// Store reads/writes published lineups under a bucket+prefix, one object per
// key at <prefix><key>.json.
type Store struct{ blob *s3blob.Blob }

// New builds a Store using the default AWS credential/region chain. prefix
// should end in "/", e.g. "lineup/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &Store{blob: b}, nil
}

func lineupKey(key string) string { return key + ".json" }

func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return s.blob.Get(ctx, lineupKey(key))
}

func (s *Store) Publish(key string, data []byte) error {
	return s.blob.PutJSON(context.Background(), lineupKey(key), data)
}

var (
	_ lineupapi.ObjectStore = (*Store)(nil)
	_ lineupapi.Publisher   = (*Store)(nil)
)

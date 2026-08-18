// Package s3backtest is the S3 adapter for backtest.SnapshotStore. It keeps the
// AWS SDK out of internal/backtest, mirroring internal/archive/s3archive,
// internal/cachestore/s3store and internal/ndjsonstore/s3ndjson; the SDK
// plumbing they all share lives in internal/s3blob.
package s3backtest

import (
	"context"

	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// Store implements backtest.SnapshotStore against an S3 bucket. Keys are
// relative to prefix, so the snapshots/<date>.json and
// snapshots-systems/system=<sys>/<date>.json layouts parse identically against
// S3 and against backtest.FileSnapshotStore's local tree.
type Store struct{ blob *s3blob.Blob }

// New constructs a Store using the default AWS config (env vars / ~/.aws/).
// prefix should end in "/", e.g. "backtest/" (or "backtest/user=<id>/", which
// statestore supplies for this per-tenant artifact).
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &Store{blob: b}, nil
}

// Get reports a missing snapshot as (nil, false, nil) rather than an error,
// matching s3blob.Get and the FileSnapshotStore contract: a day the producer
// never ran is an ordinary outcome that grading records as "missing".
func (s *Store) Get(key string) ([]byte, bool, error) {
	return s.blob.Get(context.Background(), key)
}

// Put writes one snapshot. Snapshots are rewritten when the same date is
// optimized twice (last write wins, which the hourly lineup job relies on), so
// this is deliberately an unconditional overwrite and not a conditional write.
func (s *Store) Put(key string, b []byte) error {
	return s.blob.PutJSON(context.Background(), key, b)
}

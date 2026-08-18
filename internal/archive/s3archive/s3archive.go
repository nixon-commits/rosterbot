// Package s3archive is the S3 adapter for archive.Store. It keeps the AWS SDK
// out of internal/archive, which is a stdlib-only leaf, mirroring how
// internal/cachestore/s3store keeps it out of internal/cache and
// internal/ndjsonstore/s3ndjson out of the NDJSON stack; the SDK plumbing all
// three share lives in internal/s3blob.
package s3archive

import (
	"context"
	"fmt"

	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// Store implements archive.Store against an S3 bucket. Keys are relative to
// prefix, so the <source>/dt=YYYY-MM-DD/<filename> layout parses identically
// against S3 and against archive.FileStore's local tree.
type Store struct{ blob *s3blob.Blob }

// New constructs a Store using the default AWS config (env vars / ~/.aws/).
// prefix should end in "/", e.g. "archive/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &Store{blob: b}, nil
}

// WritePartition puts each artifact under dir.
//
// Unlike archive.FileStore this is NOT atomic across the partition: S3 has no
// directory rename, so a put failing partway leaves the earlier artifacts
// written. That is a deliberate, stated difference rather than an oversight.
// Two things bound it. The caller (cmd/archive.go) fetches a source's artifacts
// in full before calling Write and treats any error here as a failed source, so
// the operator sees it and `archive --date <day>` re-runs the partition. And
// each artifact is an independent upstream capture — a partial partition is
// degraded, not corrupt.
//
// It also does not delete files a previous write of the same date left behind,
// where FileStore's rename does. Sources emit a fixed filename set per day, so
// an orphan needs a source to change shape mid-season; reconciling would cost a
// LIST on every write, which is the request volume this store exists to avoid.
func (s *Store) WritePartition(dir string, arts []archive.Artifact) error {
	for _, a := range arts {
		key := dir + "/" + a.Filename
		if err := s.blob.Put(context.Background(), key, a.Bytes); err != nil {
			return fmt.Errorf("s3archive: put %s: %w", s.blob.Key(key), err)
		}
	}
	return nil
}

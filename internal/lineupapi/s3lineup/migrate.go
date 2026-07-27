package s3lineup

import (
	"context"
	"fmt"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// MigrateStore copies flat ledger records between two prefixes of one bucket.
// It is scoped to the bucket root rather than a single prefix, since a migration
// spans two, so the keys it returns are whole object keys.
type MigrateStore struct{ blob *s3blob.Blob }

// NewMigrate builds a MigrateStore over the whole bucket.
func NewMigrate(ctx context.Context, bucket string) (*MigrateStore, error) {
	b, err := s3blob.New(ctx, bucket, "")
	if err != nil {
		return nil, err
	}
	return &MigrateStore{blob: b}, nil
}

// ListLedgerKeys lists every flat ledger key under prefix (skipping per-run
// output sub-keys such as <prefix><id>/output.json), following pagination until
// every match is collected. Used for the run-ledger migration's dry-run preview
// and post-copy verification (cmd/migrate_run_ledger.go).
func (m *MigrateStore) ListLedgerKeys(ctx context.Context, prefix string) ([]string, error) {
	return flatKeys(ctx, m.blob, prefix, 0)
}

// MigrateLedgerPrefix copies every flat ledger key from srcPrefix to dstPrefix
// within the same bucket, byte-for-byte, skipping per-run output sub-keys. It is
// idempotent — rerunning it overwrites each destination object with the current
// source bytes. Returns the full source keys it copied (still under srcPrefix)
// so the caller can print a count and spot-check IDs.
func (m *MigrateStore) MigrateLedgerPrefix(ctx context.Context, srcPrefix, dstPrefix string) ([]string, error) {
	keys, err := flatKeys(ctx, m.blob, srcPrefix, 0)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", srcPrefix, err)
	}
	for _, k := range keys {
		data, found, err := m.blob.Get(ctx, k)
		if err != nil {
			return nil, fmt.Errorf("get %s: %w", k, err)
		}
		if !found {
			return nil, fmt.Errorf("get %s: listed but missing", k)
		}
		dstKey := dstPrefix + strings.TrimPrefix(k, srcPrefix)
		if err := m.blob.PutJSON(ctx, dstKey, data); err != nil {
			return nil, fmt.Errorf("put %s: %w", dstKey, err)
		}
	}
	return keys, nil
}

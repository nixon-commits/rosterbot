package s3lineup

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func newMigrate(f *s3blobtest.Fake) *MigrateStore { return &MigrateStore{blob: f.Blob("b", "")} }

func TestMigrateLedgerPrefixCopiesFlatKeysOnly(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"runs/9999999999-abc.json": []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runs/8888888888-def.json": []byte(`{"id":"def","status":"SUCCESS","started_at":"2026-06-21T00:00:00Z"}`),
		"runs/abc/output.json":     []byte(`{"type":"grade","data":{}}`),
	})

	copied, err := newMigrate(f).MigrateLedgerPrefix(context.Background(), "runs/", "runledger/")
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(copied) != 2 {
		t.Fatalf("want 2 keys copied, got %d: %v", len(copied), copied)
	}

	if _, ok := f.Objects["runledger/9999999999-abc.json"]; !ok {
		t.Fatalf("expected runledger/9999999999-abc.json to exist; got keys %v", f.Keys())
	}
	if _, ok := f.Objects["runledger/8888888888-def.json"]; !ok {
		t.Fatalf("expected runledger/8888888888-def.json to exist; got keys %v", f.Keys())
	}
	if _, ok := f.Objects["runledger/abc/output.json"]; ok {
		t.Fatal("output sub-object should not have been migrated")
	}
	if _, ok := f.Objects["runs/9999999999-abc.json"]; !ok {
		t.Fatal("source ledger object should still exist after copy (this is a copy, not a move)")
	}
}

func TestMigrateLedgerPrefixPreservesBytesExactly(t *testing.T) {
	body := []byte(`{"id":"xyz","status":"FAILED","started_at":"2026-06-22T00:00:00Z","log_tail":"boom\nline2"}`)
	f := s3blobtest.With(map[string][]byte{"runs/7777777777-xyz.json": body})

	if _, err := newMigrate(f).MigrateLedgerPrefix(context.Background(), "runs/", "runledger/"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, ok := f.Objects["runledger/7777777777-xyz.json"]
	if !ok {
		t.Fatal("migrated object missing")
	}
	if string(got) != string(body) {
		t.Fatalf("bytes mismatch: got %s want %s", got, body)
	}
}

func TestMigrateLedgerPrefixNoLimitCollectsAllPages(t *testing.T) {
	f := s3blobtest.New()
	// 1500 ledger records -- more than one S3 ListObjectsV2 page (1000 max), so
	// this only passes if flatKeys(..., limit=0) actually paginates through to
	// the end instead of stopping after the first page.
	for i := 0; i < 1500; i++ {
		key := fmt.Sprintf("runs/%010d-r%04d.json", 9999999999-i, i)
		f.Objects[key] = []byte(fmt.Sprintf(`{"id":"r%04d","status":"SUCCESS","started_at":"2026-06-01T00:00:00Z"}`, i))
	}

	copied, err := newMigrate(f).MigrateLedgerPrefix(context.Background(), "runs/", "runledger/")
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(copied) != 1500 {
		t.Fatalf("want 1500 keys copied, got %d", len(copied))
	}
	dstCount := 0
	for k := range f.Objects {
		if strings.HasPrefix(k, "runledger/") {
			dstCount++
		}
	}
	if dstCount != 1500 {
		t.Fatalf("want 1500 objects under runledger/, got %d", dstCount)
	}
}

func TestListLedgerKeysSkipsOutputSubKeys(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"runledger/9999999999-abc.json": []byte(`{"id":"abc"}`),
		"runs/abc/output.json":          []byte(`{"type":"grade","data":{}}`),
	})

	got, err := newMigrate(f).ListLedgerKeys(context.Background(), "runledger/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != "runledger/9999999999-abc.json" {
		t.Fatalf("want exactly the one ledger key, got %v", got)
	}
}

package s3lineup

import (
	"context"
	"fmt"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestOutputStoreRoundTrip(t *testing.T) {
	f := s3blobtest.New()
	s := &OutputStore{blob: f.Blob("b", "runs/")}

	if _, ok, _ := s.GetOutput(context.Background(), "abc"); ok {
		t.Fatal("expected miss")
	}
	if err := s.PutOutput(context.Background(), "abc", []byte(`{"type":"grade","data":{}}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, stored := f.Objects["runs/abc/output.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", f.Keys())
	}
	got, ok, err := s.GetOutput(context.Background(), "abc")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != `{"type":"grade","data":{}}` {
		t.Fatalf("bytes mismatch: %s", got)
	}
}

func TestRunsListIgnoresOutputSubKeys(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"runledger/9999999999-abc.json": []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runledger/abc/output.json":     []byte(`{"type":"grade","data":{}}`),
	})
	s := &RunsStore{blob: f.Blob("b", "runledger/")}
	runs, err := s.List(context.Background(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "abc" {
		t.Fatalf("want exactly the ledger run, got %+v", runs)
	}
}

// TestRunsListPaginatesPastOutputSubKeys reproduces the live bug that motivated
// rosterbot-432: sub-objects sharing a ledger's prefix (formerly
// runs/<hex-id>/output.json; the scenario is now purely defensive since
// runledger/ never actually holds anything but flat ledger keys in production)
// whose hex id starts with a character below the ledger's inverted-timestamp
// prefix ("8...") sort first in S3's lexicographic listing. With enough of them
// to fill a page, a single-page list (the old MaxKeys=limit behavior) sees zero
// ledger records and returns an empty run list even though ledger records exist.
// The reader must paginate (follow NextContinuationToken) until it collects
// `limit` ledger records or exhausts all pages.
func TestRunsListPaginatesPastOutputSubKeys(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 16
	// 32 output-shaped sub-objects with hex ids "00".."1f" — all start with a
	// digit below '8', so they sort before every ledger key below and fully
	// occupy the first two pages on their own.
	for i := 0; i < 32; i++ {
		f.Objects[fmt.Sprintf("runledger/%02x/output.json", i)] = []byte(`{"type":"grade","data":{}}`)
	}
	// 3 ledger records, newest first by inverted timestamp, sorting after all
	// of the above.
	f.Objects["runledger/8214999999-newest.json"] = []byte(`{"id":"newest","status":"SUCCESS","started_at":"2026-07-03T00:00:00Z"}`)
	f.Objects["runledger/8215999999-middle.json"] = []byte(`{"id":"middle","status":"SUCCESS","started_at":"2026-07-02T00:00:00Z"}`)
	f.Objects["runledger/8216999999-oldest.json"] = []byte(`{"id":"oldest","status":"SUCCESS","started_at":"2026-07-01T00:00:00Z"}`)

	s := &RunsStore{blob: f.Blob("b", "runledger/")}
	runs, err := s.List(context.Background(), 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 ledger runs, got %d: %+v", len(runs), runs)
	}
	if runs[0].ID != "newest" || runs[1].ID != "middle" {
		t.Fatalf("want newest-first [newest, middle], got %+v", runs)
	}
}

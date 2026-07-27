package s3ndjson

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

type row struct {
	Dt   string `json:"dt"`
	Name string `json:"name"`
}

func newStore(f *s3blobtest.Fake) *Store { return &Store{blob: f.Blob("b", "analysis/")} }

func TestPutJoinsBucketPrefix(t *testing.T) {
	f := s3blobtest.New()
	s := newStore(f)

	if err := s.Put("grades/dt=2026-07-20/rows.ndjson", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok := f.Objects["analysis/grades/dt=2026-07-20/rows.ndjson"]; !ok {
		t.Fatalf("object not written under the bucket prefix; keys=%v", f.Keys())
	}
}

func TestListStripsBucketPrefix(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"analysis/grades/dt=2026-07-20/rows.ndjson": []byte("x"),
	})
	keys, err := newStore(f).List("grades/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || keys[0] != "grades/dt=2026-07-20/rows.ndjson" {
		t.Errorf("keys = %v, want prefix-relative keys", keys)
	}
}

func TestListPaginates(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 2
	for _, d := range []string{"01", "02", "03", "04", "05"} {
		f.Objects["analysis/grades/dt=2026-07-"+d+"/rows.ndjson"] = []byte("x")
	}
	keys, err := newStore(f).List("grades/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 5 {
		t.Fatalf("want all 5 keys across pages, got %d: %v", len(keys), keys)
	}
	if f.ListCalls < 2 {
		t.Errorf("expected the continuation loop to page, got %d list calls", f.ListCalls)
	}
}

// A key that lists but does not read back is a torn store, not an empty one, so
// Get reports it rather than handing ReadAll a zero-length partition.
func TestGetMissingObjectIsAnError(t *testing.T) {
	_, err := newStore(s3blobtest.New()).Get("grades/dt=2026-07-20/rows.ndjson")
	if err == nil {
		t.Fatal("want an error for a missing object")
	}
	if !strings.Contains(err.Error(), "analysis/grades/dt=2026-07-20/rows.ndjson") {
		t.Errorf("error %q should name the full object key", err)
	}
}

func TestRoundTripThroughReadAll(t *testing.T) {
	f := s3blobtest.New()
	s := newStore(f)

	if err := ndjsonstore.Write(s, "grades/dt=2026-07-18/rows.ndjson", []row{{Dt: "2026-07-18", Name: "early"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ndjsonstore.Write(s, "grades/dt=2026-07-20/rows.ndjson", []row{{Dt: "2026-07-20", Name: "late"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Objects["analysis/grades/dt=2026-07-19/notes.txt"] = []byte("ignore me")

	got, err := ndjsonstore.ReadAll[row](s, "grades/", "rows.ndjson", nil)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(got), got)
	}
	if got[0].Name != "early" || got[1].Name != "late" {
		t.Errorf("rows = %+v, want chronological order", got)
	}
}

package ndjsonstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type row struct {
	Dt   string `json:"dt"`
	Name string `json:"name"`
	Tag  string `json:"-"` // key-derived, never in the body
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	in := []row{{Dt: "2026-07-20", Name: "a"}, {Dt: "2026-07-20", Name: "b"}}
	b, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := strings.Count(string(b), "\n"); got != 2 {
		t.Errorf("expected one line per row, got %d newlines", got)
	}
	out, err := Unmarshal[row](b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("round trip = %+v, want the input back", out)
	}
}

func TestMarshalEmpty(t *testing.T) {
	b, err := Marshal([]row{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := Unmarshal[row](b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected no rows, got %d", len(out))
	}
}

func TestUnmarshalMalformed(t *testing.T) {
	if _, err := Unmarshal[row]([]byte("{not json}\n")); err == nil {
		t.Fatal("expected an error on malformed NDJSON")
	}
}

func TestReadAllOrdersByKeyAndFiltersFilename(t *testing.T) {
	s := NewMemStore()
	mustWrite(t, s, "p/dt=2026-07-20/rows.ndjson", []row{{Dt: "2026-07-20", Name: "late"}})
	mustWrite(t, s, "p/dt=2026-07-18/rows.ndjson", []row{{Dt: "2026-07-18", Name: "early"}})
	// Same prefix, different filename — must be ignored.
	mustWrite(t, s, "p/dt=2026-07-19/other.ndjson", []row{{Dt: "2026-07-19", Name: "nope"}})

	got, err := ReadAll[row](s, "p/", "rows.ndjson", nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d (%+v)", len(got), got)
	}
	if got[0].Name != "early" || got[1].Name != "late" {
		t.Errorf("rows = %+v, want chronological by dt= partition", got)
	}
}

func TestReadAllMixedPartitionDepths(t *testing.T) {
	// The Analysis Store's real shape: legacy partitions with no system=
	// segment alongside current ones that have it. One walk must find both.
	s := NewMemStore()
	mustWrite(t, s, "g/dt=2026-06-01/rows.ndjson", []row{{Name: "legacy"}})
	mustWrite(t, s, "g/dt=2026-07-01/system=atc-ros/rows.ndjson", []row{{Name: "current"}})

	got, err := ReadAll[row](s, "g/", "rows.ndjson", nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected both partition depths, got %d (%+v)", len(got), got)
	}
}

func TestReadAllDecorateStampsKeyDerivedFields(t *testing.T) {
	s := NewMemStore()
	mustWrite(t, s, "g/dt=2026-07-01/system=atc-ros/rows.ndjson", []row{{Name: "x"}, {Name: "y"}})

	got, err := ReadAll[row](s, "g/", "rows.ndjson", func(key string, rows []row) {
		for i := range rows {
			rows[i].Tag = key
		}
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, r := range got {
		if !strings.Contains(r.Tag, "system=atc-ros") {
			t.Errorf("Tag = %q, want the partition key stamped on every row", r.Tag)
		}
	}
}

func TestReadAllEmptyStore(t *testing.T) {
	got, err := ReadAll[row](NewMemStore(), "p/", "rows.ndjson", nil)
	if err != nil {
		t.Fatalf("an empty store is not an error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no rows, got %d", len(got))
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	s := NewFileStore(root)

	key := "grades/dt=2026-07-20/system=atc-ros/rows.ndjson"
	mustWrite(t, s, key, []row{{Dt: "2026-07-20", Name: "a"}})

	// Nested directories are created on Put.
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); err != nil {
		t.Fatalf("expected the partition file on disk: %v", err)
	}

	got, err := ReadAll[row](s, "grades/", "rows.ndjson", nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("rows = %+v, want the written row", got)
	}
}

func TestFileStoreListMissingPrefixIsEmpty(t *testing.T) {
	s := NewFileStore(t.TempDir())
	keys, err := s.List("nothing-here/")
	if err != nil {
		t.Fatalf("a missing prefix is not an error, got %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected no keys, got %v", keys)
	}
}

func TestFileStoreListReturnsSlashKeys(t *testing.T) {
	s := NewFileStore(t.TempDir())
	mustWrite(t, s, "g/dt=2026-07-20/rows.ndjson", []row{{Name: "a"}})

	keys, err := s.List("g/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || keys[0] != "g/dt=2026-07-20/rows.ndjson" {
		t.Errorf("keys = %v, want slash-separated keys relative to root", keys)
	}
}

func mustWrite(t *testing.T, s Store, key string, rows []row) {
	t.Helper()
	if err := Write(s, key, rows); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

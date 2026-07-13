package teamvalue

import (
	"testing"
	"time"
)

func sampleRow() Row {
	return Row{
		Dt: "2026-07-12", TeamID: "t1", TeamName: "Alpha", LogoURL: "https://x/a.png",
		HitterMLBValue: 100, HitterMinorsValue: 40, PitcherMLBValue: 60, PitcherMinorsValue: 30,
		HitterMLBCount: 5, HitterMinorsCount: 3, PitcherMLBCount: 4, PitcherMinorsCount: 2,
		RosteredCount: 16, MatchedCount: 14,
	}
}

func TestRow_DerivedTotals(t *testing.T) {
	r := sampleRow()
	if got := r.TotalValue(); got != 230 {
		t.Errorf("TotalValue = %d, want 230", got)
	}
	if got := r.MLBValue(); got != 160 {
		t.Errorf("MLBValue = %d, want 160", got)
	}
	if got := r.MinorsValue(); got != 70 {
		t.Errorf("MinorsValue = %d, want 70", got)
	}
	if got := r.HitterValue(); got != 140 {
		t.Errorf("HitterValue = %d, want 140", got)
	}
	if got := r.PitcherValue(); got != 90 {
		t.Errorf("PitcherValue = %d, want 90", got)
	}
}

func TestNDJSON_RoundTrip(t *testing.T) {
	in := []Row{sampleRow(), {Dt: "2026-07-12", TeamID: "t2", TeamName: "Beta"}}
	b, err := MarshalNDJSON(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := UnmarshalNDJSON(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d", len(out))
	}
	if out[0] != in[0] {
		t.Errorf("row 0 round-trip mismatch:\n got %+v\nwant %+v", out[0], in[0])
	}
	if out[1].TeamID != "t2" || out[1].RosteredCount != 0 {
		t.Errorf("row 1 mismatch: %+v", out[1])
	}
}

func TestObjectKey(t *testing.T) {
	d := time.Date(2026, 7, 12, 23, 0, 0, 0, time.UTC)
	if got := ObjectKey(d); got != "dt=2026-07-12/values.ndjson" {
		t.Errorf("ObjectKey = %q, want dt=2026-07-12/values.ndjson", got)
	}
	// A non-UTC instant still keys on its UTC calendar date.
	loc := time.FixedZone("PT", -7*3600)
	dLocal := time.Date(2026, 7, 12, 20, 0, 0, 0, loc) // 2026-07-13 03:00 UTC
	if got := ObjectKey(dLocal); got != "dt=2026-07-13/values.ndjson" {
		t.Errorf("ObjectKey(local) = %q, want dt=2026-07-13/values.ndjson", got)
	}
}

func TestFileWriter_Reader_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	d1 := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	if err := w.WriteValues(d1, []Row{{Dt: "2026-07-12", TeamID: "t1"}}); err != nil {
		t.Fatalf("write d1: %v", err)
	}
	if err := w.WriteValues(d2, []Row{{Dt: "2026-07-13", TeamID: "t1"}, {Dt: "2026-07-13", TeamID: "t2"}}); err != nil {
		t.Fatalf("write d2: %v", err)
	}
	rows, err := NewFileReader(dir).ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows across 2 days, got %d", len(rows))
	}
	// Glob is sorted, so the earlier partition comes first.
	if rows[0].Dt != "2026-07-12" {
		t.Fatalf("want first row from 2026-07-12, got %q", rows[0].Dt)
	}
}

// Re-writing the same date's partition overwrites (last-write-wins), so a
// re-run in the same day is idempotent rather than duplicating rows.
func TestFileWriter_ReRunOverwrites(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	d := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if err := w.WriteValues(d, []Row{{Dt: "2026-07-12", TeamID: "t1", HitterMLBValue: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteValues(d, []Row{{Dt: "2026-07-12", TeamID: "t1", HitterMLBValue: 2}}); err != nil {
		t.Fatal(err)
	}
	rows, err := NewFileReader(dir).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].HitterMLBValue != 2 {
		t.Fatalf("want single overwritten row value 2, got %+v", rows)
	}
}

func TestFileReader_EmptyDir(t *testing.T) {
	rows, err := NewFileReader(t.TempDir()).ReadAll()
	if err != nil {
		t.Fatalf("readall empty: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("want 0 rows, got %d", len(rows))
	}
}

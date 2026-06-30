// internal/analysis/reader_test.go
package analysis

import (
	"testing"
	"time"
)

func TestUnmarshalNDJSON_RoundTrip(t *testing.T) {
	in := []GradeRow{
		{Dt: "2026-06-15", PlayerID: "1", Name: "A", Projected: 5, Actual: 7, Diff: 2, Bucket: "OF"},
		{Dt: "2026-06-15", PlayerID: "2", Name: "B", Projected: 3, Actual: 1, Diff: -2, Bucket: "SP", IsPitcher: true},
	}
	b, err := MarshalNDJSON(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := UnmarshalNDJSON(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 2 || out[0].PlayerID != "1" || !out[1].IsPitcher || out[1].Diff != -2 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestFileReader_ReadAll(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	d1 := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGrades(d1, []GradeRow{{Dt: "2026-06-14", PlayerID: "1"}}); err != nil {
		t.Fatalf("write d1: %v", err)
	}
	if err := w.WriteGrades(d2, []GradeRow{{Dt: "2026-06-15", PlayerID: "2"}, {Dt: "2026-06-15", PlayerID: "3"}}); err != nil {
		t.Fatalf("write d2: %v", err)
	}
	rows, err := NewFileReader(dir).ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows across 2 days, got %d", len(rows))
	}
	// Glob is sorted, so the 2026-06-14 partition comes first.
	if rows[0].Dt != "2026-06-14" {
		t.Fatalf("want first row from 2026-06-14, got %q", rows[0].Dt)
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

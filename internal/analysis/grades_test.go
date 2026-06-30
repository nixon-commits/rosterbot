package analysis

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMarshalNDJSON(t *testing.T) {
	rows := []GradeRow{
		{Dt: "2026-06-15", PlayerID: "1", Name: "A", Projected: 5, Actual: 7, Diff: 2, Bucket: "OF"},
		{Dt: "2026-06-15", PlayerID: "2", Name: "B", Projected: 3, Actual: 1, Diff: -2, Bucket: "SP", IsPitcher: true},
	}
	b, err := MarshalNDJSON(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 NDJSON lines, got %d: %q", len(lines), b)
	}
	if !strings.Contains(lines[0], `"player_id":"1"`) || !strings.Contains(lines[1], `"is_pitcher":true`) {
		t.Fatalf("unexpected NDJSON: %q", b)
	}
}

func TestFileWriter_PathLayout(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	date := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGrades(date, "steamer-ros", []GradeRow{{Dt: "2026-06-15", PlayerID: "1"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.ReadFile(dir + "/grades/dt=2026-06-15/system=steamer-ros/grades.ndjson"); err != nil {
		t.Fatalf("expected system-partitioned file: %v", err)
	}
}

func TestSystemFromKey(t *testing.T) {
	cases := map[string]string{
		"analysis/grades/dt=2026-06-15/system=atc-ros/grades.ndjson": "atc-ros",
		"grades/dt=2026-06-15/grades.ndjson":                         LegacySystem, // legacy: no system= segment
	}
	for key, want := range cases {
		if got := SystemFromKey(key); got != want {
			t.Errorf("SystemFromKey(%q) = %q, want %q", key, got, want)
		}
	}
}

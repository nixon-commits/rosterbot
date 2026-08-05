package backtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestSnapshot(t *testing.T, dir string, date time.Time, pitchers []SnapshotPlayer) {
	t.Helper()
	snap := Snapshot{
		Date:        date.Format("2006-01-02"),
		GeneratedAt: date,
		Pitchers:    pitchers,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(dir, date.Format("2006-01-02")+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func TestSummarizeGSGate_SumsSuppressedStartsAcrossDays(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
		{PlayerID: "p2", IsPitcher: true, ProjPtsPerGame: 9.0},
	})
	writeTestSnapshot(t, dir, day("2026-04-11"), []SnapshotPlayer{
		{PlayerID: "p3", IsPitcher: true, ProjPtsPerGame: 7.5, GSSuppressed: true},
		{PlayerID: "p4", IsPitcher: true, ProjPtsPerGame: 4.0, GSSuppressed: true},
	})

	got := SummarizeGSGate(dir, []time.Time{day("2026-04-10"), day("2026-04-11")})

	if got.SuppressedStarts != 3 {
		t.Errorf("SuppressedStarts = %d, want 3", got.SuppressedStarts)
	}
	if got.SuppressedPts != 24.0 {
		t.Errorf("SuppressedPts = %v, want 24.0", got.SuppressedPts)
	}
	if got.Days != 2 || got.DaysWithSnapshot != 2 {
		t.Errorf("Days = %d, DaysWithSnapshot = %d, want 2 and 2", got.Days, got.DaysWithSnapshot)
	}
	if len(got.ByDate) != 2 {
		t.Fatalf("ByDate = %d entries, want 2", len(got.ByDate))
	}
	if got.ByDate[0].Starts != 1 || got.ByDate[0].Pts != 12.5 {
		t.Errorf("ByDate[0] = %+v, want 1 start / 12.5 pts", got.ByDate[0])
	}
}

// A missing day is not a zero-suppression day. DaysWithSnapshot is what makes a
// window thinned by failed runs visible instead of reading as a quiet week.
func TestSummarizeGSGate_MissingDayIsNotAZero(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
	})

	got := SummarizeGSGate(dir, []time.Time{day("2026-04-10"), day("2026-04-11")})

	if got.Days != 2 {
		t.Errorf("Days = %d, want 2 (the window asked for)", got.Days)
	}
	if got.DaysWithSnapshot != 1 {
		t.Errorf("DaysWithSnapshot = %d, want 1", got.DaysWithSnapshot)
	}
	if len(got.ByDate) != 1 {
		t.Errorf("ByDate = %d entries, want only the day that had a snapshot", len(got.ByDate))
	}
}

// Hitters never carry the flag, but guard against a future writer setting it.
func TestSummarizeGSGate_IgnoresHitters(t *testing.T) {
	dir := t.TempDir()
	snap := Snapshot{
		Date:        "2026-04-10",
		GeneratedAt: day("2026-04-10"),
		Hitters:     []SnapshotPlayer{{PlayerID: "h1", ProjPtsPerGame: 5, GSSuppressed: true}},
		Pitchers:    []SnapshotPlayer{{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 8, GSSuppressed: true}},
	}
	b, _ := json.Marshal(snap)
	os.WriteFile(filepath.Join(dir, "2026-04-10.json"), b, 0o644)

	got := SummarizeGSGate(dir, []time.Time{day("2026-04-10")})

	if got.SuppressedStarts != 1 || got.SuppressedPts != 8 {
		t.Errorf("got %d starts / %v pts, want 1 / 8 (hitter ignored)", got.SuppressedStarts, got.SuppressedPts)
	}
}

func TestSummarizeGSGate_EmptyWindow(t *testing.T) {
	got := SummarizeGSGate(t.TempDir(), nil)
	if got.SuppressedStarts != 0 || got.Days != 0 || len(got.ByDate) != 0 {
		t.Errorf("empty window = %+v, want zero value", got)
	}
}

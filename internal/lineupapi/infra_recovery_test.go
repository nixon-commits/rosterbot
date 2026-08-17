package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}
	return d
}

// writeSnapshot lays down a shadow capture where the real one lives:
// backtest/snapshots-systems/system=<sys>/<date>.json. The date is in the
// FILENAME, not a dt= segment, which is the whole reason the listers have to
// read a day out of a basename as well as out of a Hive partition.
func writeSnapshot(t *testing.T, root, sys, date string) {
	t.Helper()
	dir := filepath.Join(root, ".backtest", "snapshots-systems", "system="+sys)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, date+".json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// The 2026-08-01/02 case. The Shadow job failed both nights on Fantrax
// STALE_CLIENT, so no projection capture was ever written. A snapshot records
// what the model projected ON that day; FanGraphs rest-of-season is not
// retrievable retroactively, so re-running `grade` produces nothing — forever.
//
// The page called these "Re-runnable" alongside three genuinely recoverable
// days. Telling an operator to re-run a job that cannot work is worse than
// saying nothing: it converts permanent data loss into a chore that silently
// never completes.
func TestBuildStatus_GapWithNoProjectionCaptureIsNotRerunnable(t *testing.T) {
	root := t.TempDir()
	w := analysis.NewFileWriter(filepath.Join(root, ".analysis"))

	rows := []analysis.GradeRow{{Dt: "x", PlayerID: "1", Projected: 5, Actual: 4}}
	for _, d := range []string{"2026-07-31", "2026-08-03"} {
		if err := w.WriteGrades(day(t, d), "depthcharts-ros", rows); err != nil {
			t.Fatalf("write grades %s: %v", d, err)
		}
		writeSnapshot(t, root, "depthcharts-ros", d)
	}
	// 08-01 and 08-02 have neither grades nor a capture.

	st := buildStatus(context.Background(), NewFileInfraStore(root),
		[]layout.Artifact{layout.Analysis}, day(t, "2026-08-10"))

	a := st.Artifacts[0]
	if got, want := a.Gaps, []string{"2026-08-01", "2026-08-02"}; !eqDays(got, want) {
		t.Fatalf("gaps = %v, want %v", got, want)
	}
	if got, want := a.LostGaps, []string{"2026-08-01", "2026-08-02"}; !eqDays(got, want) {
		t.Errorf("lost gaps = %v, want %v — no capture exists, so neither day can be re-graded", got, want)
	}
}

// The other half, and the one that keeps the signal honest: a day whose capture
// IS on disk is genuinely re-runnable, so it must not be written off as lost.
// 2026-07-01 is the real instance — the day the docs cite as "the shadow
// capture missed it", which nonetheless has a snapshot and did grade.
func TestBuildStatus_GapWithAProjectionCaptureStaysRerunnable(t *testing.T) {
	root := t.TempDir()
	w := analysis.NewFileWriter(filepath.Join(root, ".analysis"))

	rows := []analysis.GradeRow{{Dt: "x", PlayerID: "1"}}
	for _, d := range []string{"2026-06-30", "2026-07-02"} {
		if err := w.WriteGrades(day(t, d), "depthcharts-ros", rows); err != nil {
			t.Fatalf("write grades %s: %v", d, err)
		}
		writeSnapshot(t, root, "depthcharts-ros", d)
	}
	writeSnapshot(t, root, "depthcharts-ros", "2026-07-01") // captured, never graded

	st := buildStatus(context.Background(), NewFileInfraStore(root),
		[]layout.Artifact{layout.Analysis}, day(t, "2026-07-20"))

	a := st.Artifacts[0]
	if got, want := a.Gaps, []string{"2026-07-01"}; !eqDays(got, want) {
		t.Fatalf("gaps = %v, want %v", got, want)
	}
	if len(a.LostGaps) != 0 {
		t.Errorf("lost gaps = %v, want none — the capture is present, so `grade --dates` recovers it", a.LostGaps)
	}
}

// An artifact that declares no recovery input must be untouched: LineupGaps
// re-derives from immutable cached Fantrax period snapshots, so every gap there
// really is re-runnable and nothing should be marked lost.
func TestBuildStatus_ArtifactWithoutARecoveryInputMarksNothingLost(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".lineupgap")
	for _, d := range []string{"2026-07-01", "2026-07-03"} {
		p := filepath.Join(dir, "dt="+d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(p, "gaps.ndjson"), []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	st := buildStatus(context.Background(), NewFileInfraStore(root),
		[]layout.Artifact{layout.LineupGaps}, day(t, "2026-07-20"))

	a := st.Artifacts[0]
	if got, want := a.Gaps, []string{"2026-07-02"}; !eqDays(got, want) {
		t.Fatalf("gaps = %v, want %v", got, want)
	}
	if len(a.LostGaps) != 0 {
		t.Errorf("lost gaps = %v, want none for an artifact with no declared recovery input", a.LostGaps)
	}
}

func eqDays(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

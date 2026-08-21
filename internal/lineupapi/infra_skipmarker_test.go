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

// The All-Star-break regression (rosterbot-u9u): a date with no fantasy-relevant
// baseball produces no graded rows, so no dt= partition is written, and the gap
// scan counted the absence as a hole. Three fabricated gaps sat in the same
// "Re-runnable" list as one real one, and re-running grade for them does nothing.
//
// Driven through the REAL writer and the REAL file lister rather than a fake
// PrefixListing, because the whole fix rests on a claim about key structure —
// that a marker object registers its dt= day even though it is not
// grades.ndjson. A hand-built listing would just restate that assumption.
func TestBuildStatus_SkipMarkerClosesTheGapItExplains(t *testing.T) {
	root := t.TempDir()
	w := analysis.NewFileWriter(filepath.Join(root, ".analysis"))

	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad date %q: %v", s, err)
		}
		return d
	}

	rows := []analysis.GradeRow{{Dt: "x", PlayerID: "1", Projected: 5, Actual: 4}}
	if err := w.WriteGrades(day("2026-07-12"), "depthcharts-ros", rows); err != nil {
		t.Fatalf("write grades: %v", err)
	}
	if err := w.WriteGrades(day("2026-07-16"), "depthcharts-ros", rows); err != nil {
		t.Fatalf("write grades: %v", err)
	}
	// 07-13..07-15 is the break. 07-14 had a game — the All-Star Game — which
	// is why the marker is driven by whether rostered players appeared, not by
	// whether the schedule was empty.
	for _, d := range []string{"2026-07-13", "2026-07-14", "2026-07-15"} {
		if err := w.WriteSkip(day(d), analysis.SkipMarker{
			Dt:            d,
			Reason:        "no rostered player appeared in a game",
			RosterPlayers: 40,
		}); err != nil {
			t.Fatalf("write skip %s: %v", d, err)
		}
	}

	st := buildStatus(context.Background(), NewFileInfraStore(root),
		[]layout.Artifact{layout.Analysis}, day("2026-07-20"))

	if len(st.Artifacts) != 1 {
		t.Fatalf("got %d artifacts", len(st.Artifacts))
	}
	a := st.Artifacts[0]
	if len(a.Gaps) != 0 {
		t.Errorf("gaps = %v, want none — the marked days are explained, not missing", a.Gaps)
	}
	if a.Partitions != 5 {
		t.Errorf("partitions = %d, want 5 (2 graded + 3 marked)", a.Partitions)
	}
	// rosterbot-36r: closing the gap was correct and left the days
	// indistinguishable from graded ones. Partitions still counts all five —
	// they do exist — but the three deliberate skips are now nameable.
	if got, want := a.Skipped, []string{"2026-07-13", "2026-07-14", "2026-07-15"}; !equalStrings(got, want) {
		t.Errorf("skipped = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
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

// A marker is never deleted, and grade re-grades a trailing 3-day window, so a
// day marked ungradeable on one run can receive real graded rows on a later one
// with the marker still sitting beside them. Such a day is GRADED, and must not
// be reported as deliberately skipped — that would be a wrong label that never
// heals, since nothing in the tree can remove the marker that causes it.
//
// This is the case that makes "the marker is the only object under this day"
// the predicate, rather than "a marker exists for this day".
func TestBuildStatus_MarkerOutlivedByRealGrades_IsNotReportedSkipped(t *testing.T) {
	root := t.TempDir()
	w := analysis.NewFileWriter(filepath.Join(root, ".analysis"))

	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad date %q: %v", s, err)
		}
		return d
	}
	rows := []analysis.GradeRow{{Dt: "x", PlayerID: "1", Projected: 5, Actual: 4}}

	// Monday's run judged the day ungradeable and wrote a marker.
	if err := w.WriteSkip(day("2026-07-13"), analysis.SkipMarker{
		Dt: "2026-07-13", Reason: "no rostered player appeared in a game", RosterPlayers: 40,
	}); err != nil {
		t.Fatalf("write skip: %v", err)
	}
	// Tuesday's run re-graded the same day inside the trailing window and found
	// real rows. There is no DeleteSkip anywhere, so the marker survives.
	if err := w.WriteGrades(day("2026-07-13"), "depthcharts-ros", rows); err != nil {
		t.Fatalf("write grades: %v", err)
	}

	st := buildStatus(context.Background(), NewFileInfraStore(root),
		[]layout.Artifact{layout.Analysis}, day("2026-07-20"))

	a := st.Artifacts[0]
	if len(a.Skipped) != 0 {
		t.Errorf("skipped = %v, want none — the day has real graded rows beside the stale marker", a.Skipped)
	}
	if a.Partitions != 1 {
		t.Errorf("partitions = %d, want 1", a.Partitions)
	}
}

// The marker must never be able to hide a day that should have been graded.
// 2026-07-01 is the real case: games were played, the shadow capture missed the
// day, and the partition is genuinely absent and re-runnable.
func TestBuildStatus_UnmarkedMissingDayIsStillReported(t *testing.T) {
	root := t.TempDir()
	w := analysis.NewFileWriter(filepath.Join(root, ".analysis"))

	day := func(s string) time.Time {
		d, _ := time.Parse("2006-01-02", s)
		return d
	}
	rows := []analysis.GradeRow{{Dt: "x", PlayerID: "1"}}
	for _, d := range []string{"2026-06-30", "2026-07-02"} {
		if err := w.WriteGrades(day(d), "depthcharts-ros", rows); err != nil {
			t.Fatalf("write grades %s: %v", d, err)
		}
	}

	st := buildStatus(context.Background(), NewFileInfraStore(root),
		[]layout.Artifact{layout.Analysis}, day("2026-07-20"))

	a := st.Artifacts[0]
	if len(a.Gaps) != 1 || a.Gaps[0] != "2026-07-01" {
		t.Errorf("gaps = %v, want [2026-07-01]", a.Gaps)
	}
}

// A marker is a distinct key carrying its own reason, not a zero-row
// grades.ndjson — an empty NDJSON is exactly what a truncated write looks like.
// It also carries no system= segment, so it sits outside every projected Athena
// partition and cannot surface as a row in rosterbot_analysis.grades.
func TestSkipMarkerKey_IsDistinctAndUnpartitionedBySystem(t *testing.T) {
	d, _ := time.Parse("2006-01-02", "2026-07-13")
	key := analysis.SkipMarkerKey(d)

	if want := "dt=2026-07-13/no-actuals.json"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
	if filepath.Base(key) == "grades.ndjson" {
		t.Error("marker must not use the grades filename — Reader would try to decode it")
	}
}

// And the reader must ignore it: ReadAll filters on the grades filename, so a
// marker in the series must not decode as a row or crash the walk.
func TestAnalysisReader_IgnoresSkipMarkers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".analysis")
	w := analysis.NewFileWriter(dir)

	d, _ := time.Parse("2006-01-02", "2026-07-13")
	if err := w.WriteSkip(d, analysis.SkipMarker{Dt: "2026-07-13", Reason: "no games"}); err != nil {
		t.Fatalf("write skip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dt=2026-07-13", "no-actuals.json")); err != nil {
		t.Fatalf("marker not written where expected: %v", err)
	}

	got, err := analysis.NewFileReader(dir).ReadAll()
	if err != nil {
		t.Fatalf("read grades: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("read %d rows from a marker-only store, want 0", len(got))
	}
}

package backtest

import (
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// The sameETDate guard catches a snapshot the day's own run never refreshed.
// It cannot catch the opposite failure: a run that DID fire on the day and
// wrote a perfectly fresh GeneratedAt over projections the stale-cache
// fallback had served from days earlier. That is exactly what the Shadow job
// did on 2026-08-19..21 (rosterbot-c61b), and grading those days compared
// three-day-old model input against fresh actuals.

// inputStaleDay is the fixture both halves share: a capture written ON the day
// it projects, differing only in how old its projection input was.
func inputStaleDay(t *testing.T, hitterFetched, pitcherFetched time.Time) []ProjectionDayResult {
	t.Helper()
	dir := t.TempDir()
	s := Snapshot{
		Date:                 "2026-08-19",
		ProjectionSystem:     "atc-ros",
		GeneratedAt:          time.Date(2026, 8, 19, 23, 41, 0, 0, time.UTC),
		HitterProjFetchedAt:  hitterFetched,
		PitcherProjFetchedAt: pitcherFetched,
		Hitters: []SnapshotPlayer{
			{PlayerID: "h1", Name: "H1", MLBTeam: "NYY", ProjPtsPerGame: 10.0, HasGame: true},
		},
	}
	if err := WriteSnapshot(NewFileSnapshotStore(dir), "", s); err != nil {
		t.Fatal(err)
	}
	days := []fantrax.DayRoster{{
		Date:    time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
		Players: []fantrax.DayPlayerFP{{PlayerID: "h1", Name: "H1", MLBTeam: "NYY", FPts: 14.0, HadGame: true}},
	}}
	return RunProjectionAnalysis(days, NewFileSnapshotStore(dir), "")
}

func TestRunProjectionAnalysis_StaleProjectionInputExcludesTheDay(t *testing.T) {
	// Both roles fetched three days before the capture — the measured gap on
	// the 2026-08-19..21 atc-ros/thebatx-ros captures.
	old := time.Date(2026, 8, 18, 23, 41, 0, 0, time.UTC).Add(-48 * time.Hour)
	results := inputStaleDay(t, old, old)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Source != SourceInputStale {
		t.Errorf("source = %q, want %q", results[0].Source, SourceInputStale)
	}
	if len(results[0].Players) != 0 {
		t.Errorf("want no graded players for a stale-input capture, got %d", len(results[0].Players))
	}
}

func TestRunProjectionAnalysis_FreshProjectionInputStillGraded(t *testing.T) {
	// The healthy half. A fetch minutes before the capture is the normal
	// hourly case and must grade exactly as before — a guard that excluded
	// these would silently empty the accuracy report.
	fresh := time.Date(2026, 8, 19, 23, 40, 0, 0, time.UTC)
	results := inputStaleDay(t, fresh, fresh)
	if results[0].Source != SourceSnapshot {
		t.Errorf("source = %q, want %q", results[0].Source, SourceSnapshot)
	}
	if len(results[0].Players) != 1 {
		t.Errorf("want 1 graded player, got %d", len(results[0].Players))
	}
}

func TestRunProjectionAnalysis_EitherRoleStaleExcludesTheWholeDay(t *testing.T) {
	// Day-level, matching the existing no-data granularity: the snapshot is
	// one file per date carrying both roles and ProjectionDayResult has a
	// single Source, so a stale pitcher input takes the whole day rather than
	// half-grading it.
	fresh := time.Date(2026, 8, 19, 23, 40, 0, 0, time.UTC)
	old := fresh.Add(-72 * time.Hour)
	if got := inputStaleDay(t, fresh, old)[0].Source; got != SourceInputStale {
		t.Errorf("stale pitcher input: source = %q, want %q", got, SourceInputStale)
	}
	if got := inputStaleDay(t, old, fresh)[0].Source; got != SourceInputStale {
		t.Errorf("stale hitter input: source = %q, want %q", got, SourceInputStale)
	}
}

func TestRunProjectionAnalysis_UnknownInputAgeGradesAsBefore(t *testing.T) {
	// Every snapshot written before this field existed carries the zero value,
	// as does any capture whose projections came from the CSV fallback or an
	// undated cache entry. Absence of evidence is not evidence: grade it.
	results := inputStaleDay(t, time.Time{}, time.Time{})
	if results[0].Source != SourceSnapshot {
		t.Errorf("source = %q, want %q for a pre-field snapshot", results[0].Source, SourceSnapshot)
	}
	if len(results[0].Players) != 1 {
		t.Errorf("want 1 graded player, got %d", len(results[0].Players))
	}
}

func TestFormatReport_NamesStaleInputDays(t *testing.T) {
	rep := Report{
		Start: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC),
		Projections: []ProjectionDayResult{
			{Date: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC), Source: SourceInputStale},
			{Date: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), Source: SourceSnapshot},
		},
	}
	out := FormatReport(rep)
	if !strings.Contains(out, "1 stale-input") {
		t.Errorf("report does not count the stale-input day:\n%s", out)
	}
	if !strings.Contains(out, "2026-08-19") {
		t.Errorf("report does not name the stale-input date:\n%s", out)
	}
	// The healthy half: a report with no stale-input day must not mention one.
	clean := Report{
		Start:       rep.Start,
		End:         rep.End,
		Projections: []ProjectionDayResult{{Date: rep.End, Source: SourceSnapshot}},
	}
	if strings.Contains(FormatReport(clean), "stale-input") {
		t.Errorf("clean report should not mention stale-input:\n%s", FormatReport(clean))
	}
}

package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// today in these tests is the UTC midnight todayET() hands runGrade.
var gradeToday = time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

func gradeDate(s string) time.Time {
	d, err := time.Parse(gradeDateFmt, s)
	if err != nil {
		panic(err)
	}
	return d
}

type fakeGradeReader struct {
	rows []analysis.GradeRow
	err  error
}

func (f fakeGradeReader) ReadAll() ([]analysis.GradeRow, error) { return f.rows, f.err }

func joined(notes []string) string { return strings.Join(notes, " | ") }

// The bead in one test. Before rosterbot-n30 the start was fixed at
// today-window, so an outage longer than --window left the days it outran as
// permanent holes — and opsalert only escalates at the third consecutive
// failure, i.e. once the window has already been outrun.
func TestResolveGradeWindow_ReachesBackPastAnOutageLongerThanTheFloor(t *testing.T) {
	// The 2026-08-01..08-03 STALE_CLIENT outage, one day worse: the store's
	// newest graded day is 8 days back, so 7 days need grading.
	cov := gradeCoverage{Latest: gradeDate("2026-08-18")}

	start, end, notes, _ := resolveGradeWindow(gradeToday, 3, 14, cov)

	if got, want := start.Format(gradeDateFmt), "2026-08-19"; got != want {
		t.Errorf("start = %s, want %s (the day after the newest day the store holds)", got, want)
	}
	if got, want := end.Format(gradeDateFmt), "2026-08-25"; got != want {
		t.Errorf("end = %s, want %s (yesterday)", got, want)
	}
	if !strings.Contains(joined(notes), "covers through 2026-08-18") {
		t.Errorf("notes must state the coverage the window was derived from, got %q", joined(notes))
	}
}

// The floor is not decoration: Fantrax's YTD totals are still settling for the
// last few closed periods, so the trailing days are re-graded even though the
// store already holds them.
func TestResolveGradeWindow_HealthyStoreStillReGradesTheSettlingFloor(t *testing.T) {
	cov := gradeCoverage{Latest: gradeDate("2026-08-25")} // yesterday: nothing missing

	start, end, _, _ := resolveGradeWindow(gradeToday, 3, 14, cov)

	if got, want := start.Format(gradeDateFmt), "2026-08-23"; got != want {
		t.Errorf("start = %s, want %s (today-3, the settling-lag floor)", got, want)
	}
	if got, want := end.Format(gradeDateFmt), "2026-08-25"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

// The property that keeps this from widening the exposure the bead's NOTES
// record: outside the fixed floor, the run only ever touches days the store
// does not hold, so it cannot overwrite a good partition with a worse one.
func TestResolveGradeWindow_ReachBackNeverCoversADayTheStoreHolds(t *testing.T) {
	for _, days := range []int{1, 2, 3, 4, 7, 13, 30} {
		latest := gradeToday.AddDate(0, 0, -days)
		start, _, _, _ := resolveGradeWindow(gradeToday, 3, 14, gradeCoverage{Latest: latest})
		floor := gradeToday.AddDate(0, 0, -3)
		if start.Before(floor) && !start.After(latest) {
			t.Errorf("latest=%s: start=%s re-grades a day the store already holds",
				latest.Format(gradeDateFmt), start.Format(gradeDateFmt))
		}
	}
}

// No silent caps. What the ceiling declined to reach is named, with the dates
// and the recovery command, plus the stale-local-snapshot caution the bead's
// NOTES paid for.
func TestResolveGradeWindow_CapNamesExactlyWhatItDeclinedToReach(t *testing.T) {
	cov := gradeCoverage{Latest: gradeDate("2026-07-20")} // 37 days back

	start, _, notes, _ := resolveGradeWindow(gradeToday, 3, 14, cov)

	if got, want := start.Format(gradeDateFmt), "2026-08-12"; got != want {
		t.Errorf("start = %s, want %s (today-14)", got, want)
	}
	all := joined(notes)
	// The skipped span is [latest+1, floor-1] = 2026-07-21..2026-08-11 = 22 days.
	for _, want := range []string{
		"2026-07-21..2026-08-11",
		"22 day(s)) NOT re-graded",
		"grade --dates 2026-07-21:2026-08-11",
		"snapshots-systems",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("capped-window notes missing %q, got %q", want, all)
		}
	}
}

// An unreadable store and an empty one warrant opposite reactions, and the
// unreadable one must REFUSE rather than degrade to the floor.
//
// The degraded run is not the cheaper failure it looks like: it grades and
// writes its floor days, advancing the store's newest day to yesterday, so any
// older hole falls behind the pointer permanently — on precisely the first run
// after an outage. Refusing costs one day, which the next readable run recovers
// because the pointer never moved.
func TestResolveGradeWindow_UnreadableCoverageRefusesRatherThanPinningTheHole(t *testing.T) {
	start, end, notes, err := resolveGradeWindow(gradeToday, 3, 14, gradeCoverage{Err: errors.New("s3 exploded")})

	if err == nil {
		t.Fatal("an unreadable store must refuse: grading on the floor would advance the " +
			"newest-day pointer past any older hole and make it permanent")
	}
	if !strings.Contains(err.Error(), "s3 exploded") {
		t.Errorf("the refusal must name its cause, got %q", err)
	}
	// Nothing may be gradeable on the refusal path — a caller that ignored the
	// error must not find a usable range sitting beside it.
	if !start.IsZero() || !end.IsZero() || notes != nil {
		t.Errorf("refusal must yield no range and no notes, got start=%v end=%v notes=%v",
			start, end, notes)
	}
}

func TestResolveGradeWindow_EmptyStoreDoesNotGradeBackToTheOpener(t *testing.T) {
	start, _, notes, _ := resolveGradeWindow(gradeToday, 3, 14, gradeCoverage{})

	if got, want := start.Format(gradeDateFmt), "2026-08-23"; got != want {
		t.Errorf("start = %s, want the fixed %s floor for a store with no graded day", got, want)
	}
	if !strings.Contains(joined(notes), "no graded day") {
		t.Errorf("an empty store must be reported as such, not as a 14-day outage: %q", joined(notes))
	}
}

// A ceiling below the floor would quietly shrink the settling-lag re-grade,
// which is the one part of the window that is not a guess.
func TestResolveGradeWindow_CeilingBelowFloorCannotShrinkTheFloor(t *testing.T) {
	start, end, _, _ := resolveGradeWindow(gradeToday, 5, 2, gradeCoverage{Latest: gradeDate("2026-08-25")})

	if got, want := start.Format(gradeDateFmt), "2026-08-21"; got != want {
		t.Errorf("start = %s, want %s (today-5, the floor, despite --max-window 2)", got, want)
	}
	if got, want := end.Format(gradeDateFmt), "2026-08-25"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

func TestResolveGradeWindow_ZeroWindowIsClampedToOneDay(t *testing.T) {
	start, end, _, _ := resolveGradeWindow(gradeToday, 0, 14, gradeCoverage{Latest: gradeDate("2026-08-25")})
	if !start.Equal(end) || start.Format(gradeDateFmt) != "2026-08-25" {
		t.Errorf("window 0 = %s..%s, want yesterday only", start.Format(gradeDateFmt), end.Format(gradeDateFmt))
	}
}

// The pointer is the newest day ANY system graded. A hole in the middle of the
// series is behind it and is never revisited — which is what stops a
// permanently ungradeable day (no shadow capture ever existed for it) from
// being re-fetched daily, forever.
func TestLatestGradedDay_TakesTheNewestDayAcrossSystemsAndIgnoresMidSeriesHoles(t *testing.T) {
	r := fakeGradeReader{rows: []analysis.GradeRow{
		{Dt: "2026-07-31", System: "atc-ros"},
		{Dt: "2026-07-31", System: "depthcharts-ros"},
		// 2026-08-01 and 08-02 are the permanently ungradeable pair.
		{Dt: "2026-08-03", System: "steamer-ros"},
		{Dt: "2026-08-02", System: "thebatx-ros"},
	}}

	cov := latestGradedDay(r)

	if cov.Err != nil {
		t.Fatalf("unexpected error: %v", cov.Err)
	}
	if got, want := cov.Latest.Format(gradeDateFmt), "2026-08-03"; got != want {
		t.Errorf("Latest = %s, want %s (newest across every system)", got, want)
	}
}

func TestLatestGradedDay_EmptyStoreIsZeroNotAnError(t *testing.T) {
	cov := latestGradedDay(fakeGradeReader{})
	if cov.Err != nil || !cov.Latest.IsZero() {
		t.Errorf("empty store = (%v, %v), want (zero, nil)", cov.Latest, cov.Err)
	}
}

// A read failure must reach resolveGradeWindow as a failure, not as an empty
// store — the two produce the same window today but say different things, and
// only one of them is a fault worth printing.
func TestLatestGradedDay_ReadFailureIsReportedNotSwallowed(t *testing.T) {
	cov := latestGradedDay(fakeGradeReader{err: errors.New("list denied")})
	if cov.Err == nil {
		t.Fatal("want the read error carried through, got nil")
	}
	if !cov.Latest.IsZero() {
		t.Errorf("Latest = %v on a failed read, want zero", cov.Latest)
	}
}

func TestLatestGradedDay_UnparseableDtCannotNarrowTheWindow(t *testing.T) {
	r := fakeGradeReader{rows: []analysis.GradeRow{
		{Dt: "2026-08-20"},
		{Dt: "not-a-date"},
		{Dt: "20260821"},
	}}
	if got, want := latestGradedDay(r).Latest.Format(gradeDateFmt), "2026-08-20"; got != want {
		t.Errorf("Latest = %s, want %s — a junk dt must be ignored, never treated as newer", got, want)
	}
}

// A wide manual --dates range is the one path that CAN overwrite a good
// partition with a worse one. It stays permitted; it does not stay silent.
func TestExplicitDatesNotes_WideRangeCarriesTheStaleSnapshotCaution(t *testing.T) {
	notes := explicitDatesNotes(gradeDate("2026-06-16"), gradeDate("2026-08-02"), 14)
	all := joined(notes)
	if !strings.Contains(all, "48 day(s)") {
		t.Errorf("range size must be stated, got %q", all)
	}
	if !strings.Contains(all, "snapshots-systems") || !strings.Contains(all, "OLDER") {
		t.Errorf("wide range must carry the stale-local-copy caution, got %q", all)
	}
}

func TestExplicitDatesNotes_NarrowRangeIsNotScolded(t *testing.T) {
	notes := explicitDatesNotes(gradeDate("2026-07-31"), gradeDate("2026-07-31"), 14)
	all := joined(notes)
	if !strings.Contains(all, "1 day(s)") {
		t.Errorf("range size must be stated, got %q", all)
	}
	if strings.Contains(all, "snapshots-systems") {
		t.Errorf("a single-day recovery is the documented safe path and must not be scolded: %q", all)
	}
}

func TestCountDays(t *testing.T) {
	tests := []struct {
		start, end string
		want       int
	}{
		{"2026-08-24", "2026-08-25", 2},
		{"2026-08-25", "2026-08-25", 1},
		{"2026-08-26", "2026-08-25", 0}, // end before start
	}
	for _, tt := range tests {
		if got := countDays(gradeDate(tt.start), gradeDate(tt.end)); got != tt.want {
			t.Errorf("countDays(%s, %s) = %d, want %d", tt.start, tt.end, got, tt.want)
		}
	}
}

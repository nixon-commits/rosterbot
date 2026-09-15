package cmd

import (
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"testing"
	"time"
)

// Dates in these tests are the UTC midnights todayET() and GetSeasonDateRange
// hand runBacktest. The 2026 season ended 2026-09-06.
var (
	btSeasonStart = time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	btSeasonEnd   = time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
)

// zeroBounder is Fantrax for a date no matchup week covers: the bounds lookup
// succeeds and returns zero, which is what the post-season yesterday gets.
type zeroBounder struct{}

func (zeroBounder) GetMatchupWeekBounds(date, seasonStart time.Time) (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, nil
}

// withBacktestFlags sets the package-level flag values backtestRangeOptions
// reads, restoring them when the test ends.
func withBacktestFlags(t *testing.T, dates string, weeks int) {
	t.Helper()
	oldDates, oldWeeks := backtestDates, backtestWeeks
	backtestDates, backtestWeeks = dates, weeks
	t.Cleanup(func() { backtestDates, backtestWeeks = oldDates, oldWeeks })
}

// The bead in one test (rosterbot-asy7). The Monday 12:00Z schedule passes no
// flags, so the window defaults to the last completed matchup week, found by
// looking up yesterday. On 2026-09-14 yesterday was past the season's final
// date, the lookup returned zero bounds, and the job exited 1 with
// "resolve range: no matchup week found for 2026-09-13" and paged — as it would
// have every Monday until the next opener. Off season that is a clean stop.
func TestResolveBacktestWindow_NoFlagsOutsideTheSeasonIsACleanStop(t *testing.T) {
	cases := []struct {
		name       string
		today      time.Time
		wantNotice string
	}{
		{
			name:       "Monday past the final date",
			today:      time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC),
			wantNotice: "Season ended 2026-09-06. No completed matchup week to grade.",
		},
		{
			name:       "opening day",
			today:      btSeasonStart,
			wantNotice: "Season starts 2026-03-26. No completed matchup week to grade yet.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withBacktestFlags(t, "", 0)
			opts, err := backtestRangeOptions(tc.today, btSeasonStart, btSeasonEnd)
			if err != nil {
				t.Fatalf("backtestRangeOptions: %v", err)
			}

			_, _, notice, err := resolveBacktestWindow(zeroBounder{}, opts, nil)
			if err != nil {
				t.Fatalf("resolveBacktestWindow returned %v; outside the season the job must exit 0", err)
			}
			if notice != tc.wantNotice {
				t.Errorf("notice = %q, want %q", notice, tc.wantNotice)
			}
		})
	}
}

// An explicit --dates range is how a past week gets regraded, and the season
// being over is exactly when that happens. The clean stop belongs to the
// week-relative default only; a guard keyed on the wall clock would block the
// backfill, which is the mistake rosterbot-pjqw's first sketch made.
func TestResolveBacktestWindow_ExplicitInSeasonDatesStillResolveOffSeason(t *testing.T) {
	withBacktestFlags(t, "2026-08-31:2026-09-06", 0)
	today := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	opts, err := backtestRangeOptions(today, btSeasonStart, btSeasonEnd)
	if err != nil {
		t.Fatalf("backtestRangeOptions: %v", err)
	}

	start, end, notice, err := resolveBacktestWindow(zeroBounder{}, opts, nil)
	if err != nil {
		t.Fatalf("resolveBacktestWindow: %v", err)
	}
	if notice != "" {
		t.Errorf("notice = %q, want none: an explicit range has a window to grade", notice)
	}
	if got, want := start.Format("2006-01-02"), "2026-08-31"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-09-06"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

// Inside the bracket "no matchup week ending yesterday" is an ordinary
// answer — the operator's team is on a bye or out — and the Monday job must
// exit 0 with a line saying so, not page. In the regular season the same
// lookup result still means Fantrax published no row, which stays a fault;
// the periods list is what tells the two apart.
func TestResolveBacktestWindow_NoMatchupWeekInAPlayoffRoundIsACleanStop(t *testing.T) {
	withBacktestFlags(t, "", 0)
	seasonEnd := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	round2 := fantrax.ScoringPeriod{Number: 24, Caption: "Playoffs - Round 2", Playoff: true,
		StartDate: time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)}
	week22 := fantrax.ScoringPeriod{Number: 22, Caption: "Scoring Period 22",
		StartDate: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)}

	// Monday after Round 2, team eliminated in Round 1.
	opts, err := backtestRangeOptions(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), btSeasonStart, seasonEnd)
	if err != nil {
		t.Fatal(err)
	}
	_, _, notice, err := resolveBacktestWindow(zeroBounder{}, opts, []fantrax.ScoringPeriod{week22, round2})
	if err != nil {
		t.Fatalf("resolveBacktestWindow returned %v; a playoff week without a matchup must exit 0", err)
	}
	if want := "No matchup week for this team ending 2026-09-20 (Playoffs - Round 2): bye or eliminated. Nothing to grade."; notice != want {
		t.Errorf("notice = %q, want %q", notice, want)
	}

	// Same lookup result on a regular-season Monday is still a fault.
	opts, err = backtestRangeOptions(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), btSeasonStart, seasonEnd)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveBacktestWindow(zeroBounder{}, opts, []fantrax.ScoringPeriod{week22}); err == nil {
		t.Error("regular-season week with no matchup row resolved cleanly; it must stay an error")
	}
}

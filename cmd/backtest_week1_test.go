package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// day is a UTC midnight, the shape todayET() and GetSeasonDateRange hand
// runBacktest.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// layoutBounder is Fantrax for a league whose schedule is exactly these weeks:
// a date inside one returns its bounds, a date in none returns zero bounds and
// no error — which is what the real lookup does for a gap or a date before the
// season's first week.
type layoutBounder struct{ weeks []fantrax.ScoringPeriod }

func (b layoutBounder) GetMatchupWeekBounds(date, _ time.Time) (time.Time, time.Time, error) {
	for _, w := range b.weeks {
		if !date.Before(w.StartDate) && !date.After(w.EndDate) {
			return w.StartDate, w.EndDate, nil
		}
	}
	return time.Time{}, time.Time{}, nil
}

// The real 2026 layout: week 1 ran 12 days, 03-25 to 04-05, so Monday 03-30
// fell INSIDE it. On such a Monday yesterday is in season (the #212 guard does
// not fire), yesterday's week is not over, and the walk steps back before the
// season's first week and finds nothing — which exited 1 with a cobra usage
// dump and paged, once per season (rosterbot-8j9v). There is genuinely nothing
// to grade yet, so it is a clean stop, exactly like the opening-day case.
func TestResolveBacktestWindow_MondayInsideWeekOneIsACleanStop(t *testing.T) {
	seasonStart, seasonEnd := day(2026, 3, 25), day(2026, 9, 27)
	week1 := fantrax.ScoringPeriod{Number: 1, Caption: "Scoring Period 1",
		StartDate: seasonStart, EndDate: day(2026, 4, 5)}
	week2 := fantrax.ScoringPeriod{Number: 2, Caption: "Scoring Period 2",
		StartDate: day(2026, 4, 6), EndDate: day(2026, 4, 12)}
	periods := []fantrax.ScoringPeriod{week1, week2}
	bounder := layoutBounder{weeks: periods}

	t.Run("Monday inside week 1 stops cleanly", func(t *testing.T) {
		withBacktestFlags(t, "", 0)
		opts, err := backtestRangeOptions(day(2026, 3, 30), seasonStart, seasonEnd)
		if err != nil {
			t.Fatal(err)
		}
		start, end, notice, err := resolveBacktestWindow(bounder, opts, periods)
		if err != nil {
			t.Fatalf("resolveBacktestWindow returned %v; a Monday inside week 1 must exit 0", err)
		}
		if notice == "" {
			t.Fatal("no notice printed; the run would report nothing and look like a silent success")
		}
		if !start.IsZero() || !end.IsZero() {
			t.Errorf("window = %s..%s, want no window", start, end)
		}
		want := "Matchup week 1 (2026-03-25 to 2026-04-05) is still in progress. No completed matchup week to grade yet."
		if notice != want {
			t.Errorf("notice = %q, want %q", notice, want)
		}
	})

	// The very next Monday week 1 IS over, and must grade normally — the stop
	// has to be about there being nothing behind yesterday, not about week 1.
	t.Run("the Monday after week 1 grades week 1", func(t *testing.T) {
		withBacktestFlags(t, "", 0)
		opts, err := backtestRangeOptions(day(2026, 4, 6), seasonStart, seasonEnd)
		if err != nil {
			t.Fatal(err)
		}
		start, end, notice, err := resolveBacktestWindow(bounder, opts, periods)
		if err != nil {
			t.Fatal(err)
		}
		if notice != "" {
			t.Fatalf("notice = %q, want none — week 1 is complete and must be graded", notice)
		}
		if !start.Equal(week1.StartDate) || !end.Equal(week1.EndDate) {
			t.Errorf("window = %s..%s, want %s..%s", start, end, week1.StartDate, week1.EndDate)
		}
	})

	// A missing week in the MIDDLE of the season is still a fault: the stop is
	// keyed on yesterday's week being the season's first, not on zero bounds,
	// so a real schedule gap keeps paging.
	t.Run("an in-season schedule gap still errors", func(t *testing.T) {
		withBacktestFlags(t, "", 0)
		week4 := fantrax.ScoringPeriod{Number: 4, Caption: "Scoring Period 4",
			StartDate: day(2026, 4, 20), EndDate: day(2026, 4, 26)}
		gappy := []fantrax.ScoringPeriod{week1, week2, week4} // week 3 never published
		opts, err := backtestRangeOptions(day(2026, 4, 22), seasonStart, seasonEnd)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := resolveBacktestWindow(layoutBounder{weeks: gappy}, opts, gappy); err == nil {
			t.Error("a missing mid-season week resolved cleanly; it must stay an error")
		}
	})

	// --weeks already resolves inside week 1 (lastNMatchupWeeks clips to
	// yesterday rather than erroring), so only the default/--matchup path was
	// ever affected. Pinned so the fix cannot quietly change it.
	t.Run("--weeks inside week 1 still resolves", func(t *testing.T) {
		withBacktestFlags(t, "", 1)
		opts, err := backtestRangeOptions(day(2026, 3, 30), seasonStart, seasonEnd)
		if err != nil {
			t.Fatal(err)
		}
		start, end, notice, err := resolveBacktestWindow(bounder, opts, periods)
		if err != nil {
			t.Fatalf("--weeks 1 inside week 1 returned %v, want a resolved window", err)
		}
		if notice != "" {
			t.Errorf("notice = %q, want none for --weeks", notice)
		}
		if !start.Equal(week1.StartDate) || !end.Equal(day(2026, 3, 29)) {
			t.Errorf("window = %s..%s, want %s..2026-03-29", start, end, week1.StartDate)
		}
	})
}

package recap

import (
	"testing"
	"time"
)

// fakeWeeks serves fixed matchup-week bounds; week index past len(bounds)
// returns zero times to signal end-of-season.
type fakeWeeks struct {
	bounds [][2]time.Time
}

func (f fakeWeeks) GetMatchupWeekByNumber(n int) (time.Time, time.Time, error) {
	if n < 1 || n > len(f.bounds) {
		return time.Time{}, time.Time{}, nil
	}
	b := f.bounds[n-1]
	return b[0], b[1], nil
}

// fakeChecker reports a fixed completion verdict for any date.
type fakeChecker struct {
	done bool
	err  error
}

func (f fakeChecker) AllGamesFinalOn(time.Time) (bool, error) { return f.done, f.err }

func ymd(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestCompletedMatchupWeeks(t *testing.T) {
	weeks := fakeWeeks{bounds: [][2]time.Time{
		{ymd("2026-05-25"), ymd("2026-05-31")}, // week 1 — fully past
		{ymd("2026-06-01"), ymd("2026-06-07")}, // week 2 — ends today
		{ymd("2026-06-08"), ymd("2026-06-14")}, // week 3 — future
	}}
	today := ymd("2026-06-07")

	t.Run("today's games not all final → exclude today's week", func(t *testing.T) {
		got, err := completedMatchupWeeks(weeks, fakeChecker{done: false}, today)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].n != 1 {
			t.Fatalf("want only week 1, got %+v", got)
		}
	})

	t.Run("today's games all final → include today's week", func(t *testing.T) {
		got, err := completedMatchupWeeks(weeks, fakeChecker{done: true}, today)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[1].n != 2 {
			t.Fatalf("want weeks 1 and 2, got %+v", got)
		}
	})

	t.Run("schedule error → conservatively exclude today's week", func(t *testing.T) {
		got, err := completedMatchupWeeks(weeks, fakeChecker{err: errBoom}, today)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("want only week 1 on schedule error, got %+v", got)
		}
	})
}

var errBoom = errBoomType("boom")

type errBoomType string

func (e errBoomType) Error() string { return string(e) }

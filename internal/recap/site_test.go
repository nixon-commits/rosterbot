package recap

import (
	"context"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeChecker reports a fixed completion verdict for any date.
type fakeChecker struct {
	done bool
	err  error
}

func (f fakeChecker) AllGamesFinalOn(context.Context, time.Time) (bool, error) { return f.done, f.err }

func ymd(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// The site enumerates the LEAGUE's weekly periods, regular season and
// playoff rounds alike, not the operator's own matchup weeks: a bracket round
// the operator was eliminated before still gets its page.
func TestCompletedMatchupWeeks(t *testing.T) {
	weeks := []fantrax.ScoringPeriod{
		{Number: 1, Caption: "Scoring Period 1", StartDate: ymd("2026-05-25"), EndDate: ymd("2026-05-31")},                  // fully past
		{Number: 2, Caption: "Scoring Period 2", StartDate: ymd("2026-06-01"), EndDate: ymd("2026-06-07")},                  // ends today
		{Number: 3, Caption: "Playoffs - Round 1", Playoff: true, StartDate: ymd("2026-06-08"), EndDate: ymd("2026-06-14")}, // future
	}
	today := ymd("2026-06-07")

	t.Run("a finished playoff round is a week with Fantrax's own caption", func(t *testing.T) {
		got, err := completedMatchupWeeks(t.Context(), weeks, fakeChecker{done: false}, ymd("2026-06-20"))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[2].n != 3 || got[2].label != "Playoffs - Round 1" || got[0].label != "Week 1" {
			t.Fatalf("want three weeks with the round captioned, got %+v", got)
		}
	})

	t.Run("today's games not all final → exclude today's week", func(t *testing.T) {
		got, err := completedMatchupWeeks(t.Context(), weeks, fakeChecker{done: false}, today)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].n != 1 {
			t.Fatalf("want only week 1, got %+v", got)
		}
	})

	t.Run("today's games all final → include today's week", func(t *testing.T) {
		got, err := completedMatchupWeeks(t.Context(), weeks, fakeChecker{done: true}, today)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[1].n != 2 {
			t.Fatalf("want weeks 1 and 2, got %+v", got)
		}
	})

	t.Run("schedule error → conservatively exclude today's week", func(t *testing.T) {
		got, err := completedMatchupWeeks(t.Context(), weeks, fakeChecker{err: errBoom}, today)
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

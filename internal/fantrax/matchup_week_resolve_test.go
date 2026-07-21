package fantrax

import (
	"errors"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// fakeBounder answers GetMatchupWeekBounds from a fixed list of week ranges:
// the first range containing the queried date wins, mirroring how the real
// client resolves a date to its matchup week.
type fakeBounder struct {
	weeks []dateRange
	err   error
	calls []time.Time
}

func (f *fakeBounder) GetMatchupWeekBounds(date, seasonStart time.Time) (time.Time, time.Time, error) {
	f.calls = append(f.calls, date)
	if f.err != nil {
		return time.Time{}, time.Time{}, f.err
	}
	for _, w := range f.weeks {
		if !date.Before(w.start) && !date.After(w.end) {
			return w.start, w.end, nil
		}
	}
	return time.Time{}, time.Time{}, nil
}

// Two adjacent full weeks, used by most cases below.
func twoWeeks() []dateRange {
	return []dateRange{
		{start: day("2026-07-06"), end: day("2026-07-12")},
		{start: day("2026-07-13"), end: day("2026-07-19")},
	}
}

func TestLastCompletedMatchupWeek_TodayIsFirstDayOfNewWeek(t *testing.T) {
	// Yesterday (07-19) was the final day of week 2, so week 2 is complete.
	fb := &fakeBounder{weeks: twoWeeks()}
	start, end, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-07-20"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-07-13"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-07-19"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
	if len(fb.calls) != 1 {
		t.Errorf("expected a single bounds lookup, got %d", len(fb.calls))
	}
}

func TestLastCompletedMatchupWeek_TodayInsideWeekStepsBack(t *testing.T) {
	// Today (07-16) sits mid-week-2, so week 2 is still running: step back to
	// week 1. This is the case both cmd copies existed to handle.
	fb := &fakeBounder{weeks: twoWeeks()}
	start, end, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-07-16"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-07-06"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-07-12"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
	// Second lookup must probe the day *before* the running week's start.
	if len(fb.calls) != 2 {
		t.Fatalf("expected 2 bounds lookups, got %d", len(fb.calls))
	}
	if got, want := fb.calls[1].Format("2006-01-02"), "2026-07-12"; got != want {
		t.Errorf("step-back probe = %s, want %s", got, want)
	}
}

func TestLastCompletedMatchupWeek_TodayIsFinalDayStepsBack(t *testing.T) {
	// Today (07-19) is the week's last day — the week is not complete until it
	// is over, so the helper still steps back. (Callers that want same-day
	// completion, e.g. recap, check MLB game finality before calling this.)
	fb := &fakeBounder{weeks: twoWeeks()}
	start, _, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-07-19"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-07-06"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
}

func TestLastCompletedMatchupWeek_MergedAllStarBreakWeek(t *testing.T) {
	// Fantrax merges the All-Star break into one wide period. Today inside the
	// merged week must still step back to the week before it, not land inside.
	fb := &fakeBounder{weeks: []dateRange{
		{start: day("2026-07-06"), end: day("2026-07-12")},
		{start: day("2026-07-13"), end: day("2026-07-26")},
	}}
	start, end, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-07-21"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-07-06"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-07-12"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

func TestLastCompletedMatchupWeek_NoWeekForYesterday(t *testing.T) {
	fb := &fakeBounder{weeks: nil}
	_, _, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-07-20"))
	if err == nil {
		t.Fatal("expected an error when yesterday has no matchup week")
	}
}

func TestLastCompletedMatchupWeek_NoPriorWeek(t *testing.T) {
	// Only the season's first week exists and today is inside it — there is no
	// prior week to step back to.
	fb := &fakeBounder{weeks: []dateRange{
		{start: day("2026-03-26"), end: day("2026-04-01")},
	}}
	_, _, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-03-30"))
	if err == nil {
		t.Fatal("expected an error when no prior matchup week exists")
	}
}

func TestLastCompletedMatchupWeek_BoundsError(t *testing.T) {
	want := errors.New("upstream boom")
	fb := &fakeBounder{err: want}
	_, _, err := LastCompletedMatchupWeek(fb, day("2026-03-26"), day("2026-07-20"))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to wrap %v", err, want)
	}
}

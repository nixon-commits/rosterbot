package backtest

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

type weekRange struct{ start, end time.Time }

// fakeBounder resolves a date to the first configured week containing it.
type fakeBounder struct {
	weeks []weekRange
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

func fourWeeks() []weekRange {
	return []weekRange{
		{day("2026-06-22"), day("2026-06-28")},
		{day("2026-06-29"), day("2026-07-05")},
		{day("2026-07-06"), day("2026-07-12")},
		{day("2026-07-13"), day("2026-07-19")},
	}
}

func TestResolveRange_ExplicitDatesWin(t *testing.T) {
	fb := &fakeBounder{weeks: fourWeeks()}
	start, end, err := ResolveRange(fb, RangeOptions{
		Today:         day("2026-07-20"),
		SeasonStart:   day("2026-03-26"),
		ExplicitStart: day("2026-05-01"),
		ExplicitEnd:   day("2026-05-07"),
		Weeks:         3, // ignored: explicit dates take precedence
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-05-01"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-05-07"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
	if len(fb.calls) != 0 {
		t.Errorf("explicit dates should not consult matchup-week bounds, got %d calls", len(fb.calls))
	}
}

func TestResolveRange_DefaultIsLastCompletedWeek(t *testing.T) {
	fb := &fakeBounder{weeks: fourWeeks()}
	start, end, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-07-20"),
		SeasonStart: day("2026-03-26"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-07-13"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-07-19"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

func TestResolveRange_WeeksWalksBack(t *testing.T) {
	// Today 07-20 → yesterday 07-19 is the last day of week 07-13..07-19.
	// Three weeks back starts at 06-29; the window always ends at yesterday.
	fb := &fakeBounder{weeks: fourWeeks()}
	start, end, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-07-20"),
		SeasonStart: day("2026-03-26"),
		Weeks:       3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-06-29"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-07-19"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

func TestResolveRange_WeeksNeverIncludesToday(t *testing.T) {
	// Today sits mid-week (07-16). The window must still end at yesterday, and
	// the partial current week counts as the first week walked back.
	fb := &fakeBounder{weeks: fourWeeks()}
	start, end, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-07-16"),
		SeasonStart: day("2026-03-26"),
		Weeks:       2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-07-06"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-07-15"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

func TestResolveRange_WeeksStopsAtSeasonStart(t *testing.T) {
	// Asking for more weeks than exist returns what is available rather than
	// erroring, as long as at least one week resolved.
	fb := &fakeBounder{weeks: fourWeeks()}
	start, _, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-07-20"),
		SeasonStart: day("2026-03-26"),
		Weeks:       99,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-06-22"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
}

func TestResolveRange_WeeksNoWeeksResolved(t *testing.T) {
	fb := &fakeBounder{weeks: nil}
	_, _, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-07-20"),
		SeasonStart: day("2026-03-26"),
		Weeks:       2,
	})
	if err == nil {
		t.Fatal("expected an error when no matchup week resolves")
	}
}

func TestResolveRange_WeeksBoundsError(t *testing.T) {
	want := errors.New("upstream boom")
	fb := &fakeBounder{err: want}
	_, _, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-07-20"),
		SeasonStart: day("2026-03-26"),
		Weeks:       2,
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to wrap %v", err, want)
	}
}

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

var (
	season2026Start = day("2026-03-26")
	season2026End   = day("2026-09-06")
)

// finalWeeks is the tail of the 2026 schedule: Period 21 and Period 22, the
// last matchup week, which closed on the season's final date.
func finalWeeks() []weekRange {
	return []weekRange{
		{day("2026-08-24"), day("2026-08-30")},
		{day("2026-08-31"), day("2026-09-06")},
	}
}

// Week-relative windows walk back from yesterday, so outside the season there
// is no week to grade. That has to be a distinguishable condition rather than
// the "no matchup week found" fault: the Monday 12:00Z backtest job exited 1
// and paged on 2026-09-14 and would have every Monday to the next opener
// (rosterbot-asy7). A zero week from the bounds lookup is ambiguous between
// the season boundaries and a genuine in-season schedule gap, so the gap and
// an unknown season end must keep erroring.
func TestResolveRange_OutOfSeasonClassification(t *testing.T) {
	cases := []struct {
		name         string
		today        time.Time
		seasonEnd    time.Time
		weeks        int
		wantOOS      bool
		beforeOpener bool
	}{
		{name: "default past the final date", today: day("2026-09-14"), seasonEnd: season2026End, wantOOS: true},
		{name: "--weeks past the final date", today: day("2026-09-14"), seasonEnd: season2026End, weeks: 3, wantOOS: true},
		{name: "opening day has no completed week yet", today: season2026Start, seasonEnd: season2026End, wantOOS: true, beforeOpener: true},
		{name: "zero season end stays unguarded", today: day("2026-09-14"), seasonEnd: time.Time{}, wantOOS: false},
		{name: "in-season schedule gap stays a fault", today: day("2026-07-15"), seasonEnd: season2026End, wantOOS: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBounder{weeks: finalWeeks()}
			_, _, err := ResolveRange(fb, RangeOptions{
				Today:       tc.today,
				SeasonStart: season2026Start,
				SeasonEnd:   tc.seasonEnd,
				Weeks:       tc.weeks,
			})
			if err == nil {
				t.Fatal("err = nil, want an error: there is no week to grade")
			}
			var oos *OutOfSeasonError
			if got := errors.As(err, &oos); got != tc.wantOOS {
				t.Fatalf("errors.As(%v, *OutOfSeasonError) = %v, want %v", err, got, tc.wantOOS)
			}
			if tc.wantOOS && oos.BeforeOpener() != tc.beforeOpener {
				t.Errorf("BeforeOpener() = %v, want %v", oos.BeforeOpener(), tc.beforeOpener)
			}
		})
	}
}

// The Monday after the final date is the one post-season run that still has a
// week to grade: yesterday IS the final date, so Period 22 resolves. The guard
// is strictly past the end, as the lineup guard is (rosterbot-pjqw); a
// non-strict one would have skipped grading the last week of the season.
func TestResolveRange_MondayAfterFinalDateStillGradesTheFinalWeek(t *testing.T) {
	fb := &fakeBounder{weeks: finalWeeks()}
	start, end, err := ResolveRange(fb, RangeOptions{
		Today:       day("2026-09-07"),
		SeasonStart: season2026Start,
		SeasonEnd:   season2026End,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := start.Format("2006-01-02"), "2026-08-31"; got != want {
		t.Errorf("start = %s, want %s", got, want)
	}
	if got, want := end.Format("2006-01-02"), "2026-09-06"; got != want {
		t.Errorf("end = %s, want %s", got, want)
	}
}

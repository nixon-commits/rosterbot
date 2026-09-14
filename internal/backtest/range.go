package backtest

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// RangeOptions selects the date window to grade. Precedence is explicit dates,
// then Weeks, then the default of the last completed matchup week.
//
// ExplicitStart/ExplicitEnd are pre-parsed by the caller rather than passed as
// a flag string: the CLI's --dates syntax (ranges, "all") is a cmd-layer
// concern, while which *matchup weeks* a window covers is the policy that
// belongs here.
type RangeOptions struct {
	Today       time.Time
	SeasonStart time.Time

	// SeasonEnd is the season's final day, used only to recognize a
	// week-relative window with no week to grade. Zero means the boundary is
	// unknown, which is not the same as out of season, so it stays unguarded.
	SeasonEnd time.Time

	// ExplicitStart/ExplicitEnd, when both non-zero, are used verbatim.
	ExplicitStart time.Time
	ExplicitEnd   time.Time

	// Weeks > 0 walks back that many matchup-week boundaries from yesterday.
	Weeks int
}

// OutOfSeasonError reports that a week-relative window (the default,
// --matchup, --weeks) has nothing to grade because yesterday falls outside
// the season. It is a distinguishable condition rather than a fault: the
// caller ends the run cleanly on it instead of exiting non-zero.
//
// It exists because both season boundaries land on the same zero week from
// GetMatchupWeekBounds as a genuine in-season schedule gap does, and the two
// deserve opposite responses. The 2026 season ended 2026-09-06 and the weekly
// Monday backtest job then exited 1 and paged (rosterbot-asy7); the identical
// branch fires on opening day, before any week has completed.
type OutOfSeasonError struct{ Yesterday, Start, End time.Time }

func (e *OutOfSeasonError) Error() string {
	if e.BeforeOpener() {
		return fmt.Sprintf("season starts %s — no completed matchup week to grade yet", e.Start.Format("2006-01-02"))
	}
	return fmt.Sprintf("season ended %s — no completed matchup week to grade", e.End.Format("2006-01-02"))
}

// BeforeOpener distinguishes the two boundaries.
func (e *OutOfSeasonError) BeforeOpener() bool { return e.Yesterday.Before(e.Start) }

// ResolveRange picks the [start, end] window to grade from opts, consulting wb
// for matchup-week boundaries when the window is week-relative.
func ResolveRange(wb fantrax.WeekBounder, opts RangeOptions) (time.Time, time.Time, error) {
	if !opts.ExplicitStart.IsZero() && !opts.ExplicitEnd.IsZero() {
		return opts.ExplicitStart, opts.ExplicitEnd, nil
	}

	// Both week-relative branches walk back from yesterday, so outside the
	// season there is no week to grade. Decided from the season range rather
	// than from the zero week the bounds lookup returns, which cannot be told
	// apart from an in-season schedule gap; below this point a zero week still
	// means a fault. Strictly past the end, so the Monday after the final date
	// grades the final week. Explicit dates are above this on purpose: a past
	// range is regraded off season. A zero SeasonEnd is an unknown boundary,
	// not an off-season one, so it stays unguarded.
	yesterday := opts.Today.AddDate(0, 0, -1)
	if !opts.SeasonEnd.IsZero() && (yesterday.Before(opts.SeasonStart) || yesterday.After(opts.SeasonEnd)) {
		return time.Time{}, time.Time{}, &OutOfSeasonError{Yesterday: yesterday, Start: opts.SeasonStart, End: opts.SeasonEnd}
	}

	if opts.Weeks > 0 {
		return lastNMatchupWeeks(wb, opts.SeasonStart, opts.Today, opts.Weeks)
	}

	// Default (and --matchup): the most recently completed matchup week.
	return fantrax.LastCompletedMatchupWeek(wb, opts.SeasonStart, opts.Today)
}

// lastNMatchupWeeks walks back n matchup-week boundaries from yesterday and
// returns [start of the nth week back, yesterday].
//
// The end is always yesterday, so a still-running current week is inherently
// clipped and today is never graded. (The pre-extraction copy in cmd carried an
// explicit i==0 clip for this, but it was dead code: it assigned to a curEnd
// that the next statement unconditionally overwrote, and to a weekEnd local
// that was never read again.)
func lastNMatchupWeeks(wb fantrax.WeekBounder, seasonStart, today time.Time, n int) (time.Time, time.Time, error) {
	yesterday := today.AddDate(0, 0, -1)

	curEnd := yesterday
	var start time.Time
	for i := 0; i < n; i++ {
		weekStart, _, err := wb.GetMatchupWeekBounds(curEnd, seasonStart)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if weekStart.IsZero() {
			break
		}
		start = weekStart
		// Step back one day before that week's start.
		curEnd = weekStart.AddDate(0, 0, -1)
	}
	if start.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("could not resolve %d matchup week(s)", n)
	}
	return start, yesterday, nil
}

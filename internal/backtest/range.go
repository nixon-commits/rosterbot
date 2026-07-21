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

	// ExplicitStart/ExplicitEnd, when both non-zero, are used verbatim.
	ExplicitStart time.Time
	ExplicitEnd   time.Time

	// Weeks > 0 walks back that many matchup-week boundaries from yesterday.
	Weeks int
}

// ResolveRange picks the [start, end] window to grade from opts, consulting wb
// for matchup-week boundaries when the window is week-relative.
func ResolveRange(wb fantrax.WeekBounder, opts RangeOptions) (time.Time, time.Time, error) {
	if !opts.ExplicitStart.IsZero() && !opts.ExplicitEnd.IsZero() {
		return opts.ExplicitStart, opts.ExplicitEnd, nil
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

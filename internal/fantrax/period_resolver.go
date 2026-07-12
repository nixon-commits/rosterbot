package fantrax

import "time"

// ResolvePeriod is the single entry point for mapping a calendar date to its
// Fantrax scoring period number. It replaces the four independent
// derivations that used to exist across the codebase (day math, current-day
// anchoring, matchup-week positional counting, and Fantrax's own caption
// text) with one three-tier strategy, most-authoritative first:
//
//  1. Exact containment in periods — Fantrax's own per-period date ranges
//     (from GetScoringPeriodsAndTeams), immune to mid-season inserted daily
//     periods (doubleheaders, postponed-game makeups). Used whenever periods
//     is non-empty and one of its entries covers date.
//  2. AnchorPeriodForDate(today, currentPeriod, date) when currentPeriod is
//     known (>0) — exact within an insertion-free window around today.
//  3. PeriodForDate(seasonStart, date) — plain season-start day math, the
//     last-resort fallback when neither of the above is available.
func ResolvePeriod(periods []ScoringPeriod, currentPeriod int, seasonStart, today, date time.Time) int {
	if sp := FindCurrentPeriod(periods, date); sp != nil {
		return sp.Number
	}
	if currentPeriod > 0 {
		return AnchorPeriodForDate(today, currentPeriod, date)
	}
	return PeriodForDate(seasonStart, date)
}

package fantrax

import "time"

// gsPeriodWalk returns the scoring period number for each calendar day from
// sp.StartDate through the last completed day (today's yesterday, capped at
// sp.EndDate), resolved via ResolvePeriod rather than bare anchor arithmetic.
// This matters because sp can itself be a merged multi-day scoring period
// (e.g. the All-Star break, period 16 spanning 14 calendar days under one
// number) — day-by-day anchor arithmetic assumes one period per calendar
// day and mis-derives every day in a merged span except the one nearest the
// anchor. Returns nil if the period hasn't started yet (yesterday is before
// sp.StartDate).
func gsPeriodWalk(sp ScoringPeriod, periods []ScoringPeriod, currentPeriod int, seasonStart, today time.Time) []int {
	yesterday := today.Truncate(24*time.Hour).AddDate(0, 0, -1)
	if yesterday.Before(sp.StartDate) {
		return nil
	}
	if yesterday.After(sp.EndDate) {
		yesterday = sp.EndDate
	}
	var out []int
	for d := sp.StartDate; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		out = append(out, ResolvePeriod(periods, currentPeriod, seasonStart, today, d))
	}
	return out
}

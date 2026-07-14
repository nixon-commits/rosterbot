package fantrax

import "time"

// DailyPeriodFor returns the *daily* scoring-period number for a calendar date,
// anchored on Fantrax's authoritative current daily period when known, else
// season-start day math.
//
// GetTeamGS (and its sibling GetTeamPitcherStarts) reconstruct per-day GS by
// diffing consecutive *daily* YTD roster snapshots — getPlayerGSSnapshotForPeriod
// is keyed by the daily period, which advances one number per calendar day. So
// the walk needs the daily number (e.g. 104,105,…,110 across a week), NOT the
// weekly matchup "Scoring Period" number that ResolvePeriod returns (a single 15
// for the whole week). Passing the weekly number makes every day fetch the same
// snapshot, collapsing all day-over-day deltas to zero and under-counting the
// tally to ~one day's worth (the rosterbot-uv6 regression).
//
// Any other caller needing a *daily* period number for a specific calendar date
// — not "which weekly matchup period contains this date" — should use this
// directly instead of ResolvePeriod. ResolvePeriod's tier 1 trusts the periods
// list from GetScoringPeriodsAndTeams, which is weekly-matchup-keyed (see
// TestFindCurrentPeriod_MergedAllStarBreakPeriod); since virtually every
// in-season date falls inside some weekly period's date range, tier 1 wins for
// almost every call and hands back the wrong (weekly) number. internal/lineuprun's
// per-date lineup-apply loop hit exactly this: every date inside the merged
// All-Star-break weekly period (16, 2026-07-13..07-26) resolved to the same
// period 16, so ApplyLineup/GetHitterRosterForPeriod for 11 different calendar
// dates all read/wrote the same (wrong) daily snapshot — the same day's diff
// never resolved, so the same lineup swap re-applied and re-notified every
// hourly run.
func DailyPeriodFor(currentPeriod int, seasonStart, today, date time.Time) int {
	if currentPeriod > 0 {
		return AnchorPeriodForDate(today, currentPeriod, date)
	}
	return PeriodForDate(seasonStart, date)
}

// gsPeriodWalk returns the daily scoring-period number for each calendar day
// from sp.StartDate through the last completed day (today's yesterday, capped at
// sp.EndDate). Returns nil if the period hasn't started yet (yesterday is before
// sp.StartDate). See DailyPeriodFor for why this is the daily numbering, not the
// weekly one.
func gsPeriodWalk(sp ScoringPeriod, currentPeriod int, seasonStart, today time.Time) []int {
	yesterday := today.Truncate(24*time.Hour).AddDate(0, 0, -1)
	if yesterday.Before(sp.StartDate) {
		return nil
	}
	if yesterday.After(sp.EndDate) {
		yesterday = sp.EndDate
	}
	var out []int
	for d := sp.StartDate; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		out = append(out, DailyPeriodFor(currentPeriod, seasonStart, today, d))
	}
	return out
}

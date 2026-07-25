package fantrax

import "time"

// DailyPeriodFor returns the *daily* scoring-period number for a calendar date.
// This is the single resolver for the daily axis — see the Period Map section
// in CONTEXT.md. Resolution order: (1) the authoritative periodList date map
// (see period_date_map.go) when c has one available and it covers date, else
// (2) season-start day math.
//
// rosterbot-xyk collapsed what used to be two near-twin resolvers (this one and
// the unexported dailyPeriodForDate) that differed only in whether a middle
// "anchor on GetCurrentPeriod" tier was consulted. Measured 2026-07-25 against
// the live periodList map — 187 entries covering the whole 2026 season — the
// daily axis is strictly 1:1 with calendar days: zero merged or inserted
// periods across 122 elapsed days, and day math agrees with the authoritative
// map on all 187 dates. The anchor tier therefore never once supplied an answer
// day math wouldn't have, while its own input (GetCurrentPeriod) lagged a full
// calendar day on 2026-07-16 and produced the rosterbot-48z silent apply
// failure. It was deleted rather than demoted: a tier that has only ever been
// wrong is not a safety net.
//
// Note the merging Fantrax *does* do — the All-Star break, where weekly period
// 16 spans 2026-07-13..07-26 — happens on the WEEKLY axis only. See
// ScoringPeriod / MatchupWeek in CONTEXT.md.
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
//
// rosterbot-2ax: the anchor-only version of this function assumed
// GetCurrentPeriod() is always exactly "today's period," which broke live on
// 2026-07-16 — GetCurrentPeriod() lagged a full day behind the authoritative
// periodList map (113 vs the map's 114 for that date), so lineup-apply
// submitted changes against an already-closed period; Fantrax silently
// rejected them as "already locked," masked as success by the locked-player
// retry path. Consulting the map first made that self-correcting; rosterbot-xyk
// then removed the lagging anchor from the fallback chain entirely.
//
// c may be nil (hermetic tests), in which case the map is skipped.
func (c *Client) DailyPeriodFor(seasonStart, date time.Time) DailyPeriod {
	if c != nil {
		if m, err := c.periodDateMap(seasonStart); err == nil {
			if p, ok := m[date.Format("2006-01-02")]; ok {
				return p
			}
		}
	}
	return PeriodForDate(seasonStart, date)
}

// gsPeriodWalk returns the daily scoring-period number for each calendar day
// from sp.StartDate through the last completed day (today's yesterday, capped at
// sp.EndDate). Returns nil if the period hasn't started yet (yesterday is before
// sp.StartDate). See DailyPeriodFor for why this is the daily numbering, not the
// weekly one, and for why c (may be nil, e.g. hermetic tests) is consulted first.
func gsPeriodWalk(c *Client, sp ScoringPeriod, seasonStart, today time.Time) []DailyPeriod {
	yesterday := today.Truncate(24*time.Hour).AddDate(0, 0, -1)
	if yesterday.Before(sp.StartDate) {
		return nil
	}
	if yesterday.After(sp.EndDate) {
		yesterday = sp.EndDate
	}
	var out []DailyPeriod
	for d := sp.StartDate; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		out = append(out, c.DailyPeriodFor(seasonStart, d))
	}
	return out
}

package fantrax

import (
	"fmt"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// DatedPitcherStart records a single SP game start with its date, FPts, and
// the pitcher's MLB team. Unlike PitcherStart (which gscheck uses for
// deduction), this is a complete list of all active-slot starts for use in
// award/highlight reports.
type DatedPitcherStart struct {
	PitcherName string    `json:"pitcher_name"`
	Date        time.Time `json:"date"`
	FPts        float64   `json:"fpts"`
	MLBTeam     string    `json:"mlb_team"`
}

// PitcherDay is one pitcher's per-day roster fact: he appeared on the team's
// frozen snapshot for that date, listed under MLBTeam, and either did or did
// not take an active-slot start.
//
// It exists because a rate needs a DENOMINATOR and DatedPitcherStart only
// carries the numerator. Counting a pitcher's starts against every day in a
// window reads a mid-window acquisition as an arm that declined to start on
// days he was not ours — the same error class rosterbot-6tw fixed for the
// hitter recency window, where absent days contributed no row at all and were
// indistinguishable from zeroes.
type PitcherDay struct {
	PitcherName string    `json:"pitcher_name"`
	Date        time.Time `json:"date"`
	MLBTeam     string    `json:"mlb_team"`
	// FPts is the day's fantasy-point delta, zeroed on a first appearance so
	// pre-window production is never credited as same-day points. Meaningful
	// only where Started is true.
	FPts    float64 `json:"fpts"`
	Started bool    `json:"started"`
}

// GetTeamPitcherStarts returns every active-slot SP start a team made within
// [start, end] (inclusive). It is exactly the Started subset of
// GetTeamPitcherDays — a filter over that one walk rather than a second copy
// of the YTD-diff logic, because a duplicated derivation is how the two
// pitcher-GS cache-key builders drifted for four days without an error
// (rosterbot-sza). TestGetTeamPitcherStarts_IsTheStartedSubsetOfTheDayWalk
// pins them together.
//
// See GetTeamPitcherDays for the caching contract.
func (c *Client) GetTeamPitcherStarts(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]DatedPitcherStart, error) {
	days, err := c.GetTeamPitcherDays(teamID, start, end, seasonStart, cacheDir, cacheTTL)
	if err != nil {
		return nil, err
	}
	var starts []DatedPitcherStart
	for _, d := range days {
		if !d.Started {
			continue
		}
		starts = append(starts, DatedPitcherStart{
			PitcherName: d.PitcherName,
			Date:        d.Date,
			FPts:        d.FPts,
			MLBTeam:     d.MLBTeam,
		})
	}
	return starts, nil
}

// GetTeamPitcherDays returns one row per (pitcher, day) the team's frozen
// per-day snapshots cover within [start, end] (inclusive). Mirrors the per-day
// diff logic from GetTeamGS but without the gsMax filter — we want all starts,
// not just overage. Caller supplies seasonStart so periods can be derived.
//
// cacheDir/cacheTTL enable per-period snapshot caching (key
// `fantrax-pitcher-gs-<teamID>-<season>-<period>`). Past-period snapshots are
// immutable, so a long TTL (fantrax.PastPeriodTTL) lets the recap pipeline
// reuse data across runs and avoid re-hitting Fantrax for completed weeks. The
// TTL is narrowed per period by snapshotTTL, so passing the long one does not
// pin the live period. Pass
// cacheDir="" or cacheTTL=0 to bypass — used by the live gscheck path,
// which always wants fresh data. The 200ms throttle between days only
// fires when the upstream API was actually hit; cache-only days don't
// pace themselves.
func (c *Client) GetTeamPitcherDays(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]PitcherDay, error) {
	if end.Before(start) {
		return nil, fmt.Errorf("end %s before start %s", end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	// One cache per period, not one for the window: the caller's TTL is the long
	// immutable-past one, and snapshotTTL narrows it to todayTTL for any period
	// that can still change. Before rosterbot-qoa this was a single flat-TTL
	// cache, which was survivable at 30 days and is not at PastPeriodTTL — the
	// live period's snapshot would be pinned for the rest of the season.
	curPeriod := c.DailyPeriodFor(seasonStart, time.Now().UTC())
	season := seasonStart.Year()
	snapCacheFor := func(period DailyPeriod) *cache.FileCache[map[string]playerGSSnapshot] {
		ttl := snapshotTTL(cacheTTL, period, curPeriod)
		if cacheDir == "" || ttl == 0 {
			return nil
		}
		return cache.New[map[string]playerGSSnapshot](cacheDir, ttl)
	}

	// Baseline YTD from the day before `start` so the first day yields a
	// single-day delta. Same logic as DailyFantasyPoints baseline handling.
	prevGS := map[string]int{}
	prevFPts := map[string]float64{}
	dayBefore := start.AddDate(0, 0, -1)
	if !dayBefore.Before(seasonStart) {
		basePeriod := c.DailyPeriodFor(seasonStart, dayBefore)
		if basePeriod >= 1 {
			info, _, err := c.getPlayerGSSnapshotForPeriodCached(snapCacheFor(basePeriod), teamID, season, basePeriod)
			if err != nil {
				return nil, fmt.Errorf("baseline pitcher snapshot period %d: %w", basePeriod, err)
			}
			for pid, snap := range info {
				prevGS[pid] = snap.GS
				prevFPts[pid] = snap.FPts
			}
		}
	}

	var out []PitcherDay
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		period := c.DailyPeriodFor(seasonStart, d)
		info, hitNetwork, err := c.getPlayerGSSnapshotForPeriodCached(snapCacheFor(period), teamID, season, period)
		if err != nil {
			return nil, fmt.Errorf("pitcher snapshot %s (period %d): %w", d.Format("2006-01-02"), period, err)
		}

		for pid, snap := range info {
			row := PitcherDay{
				PitcherName: snap.Name,
				Date:        d,
				MLBTeam:     snap.MLBTeam,
			}
			// Only count active-slot starts so a benched/IL pitcher's outing
			// doesn't appear (mirrors GetTeamGS semantics). The row itself is
			// still emitted: he was rostered that day, which is the fact the
			// denominator needs.
			if snap.Active {
				prev, existed := prevGS[pid]
				delta := snap.GS - prev
				// First-appearance cap: a pitcher cannot earn more than one GS
				// per day, so cap an unseen pitcher's first delta at 1 to avoid
				// counting pre-period or hitter-slot starts. Mirrors GetTeamGS.
				if !existed && delta > 1 {
					delta = 1
				}
				if delta > 0 {
					row.Started = true
					row.FPts = snap.FPts - prevFPts[pid]
					// On first appearance, the prevFPts baseline is zero so the
					// delta would be the YTD total. Zero it so we don't credit
					// pre-window production as same-day points. Mirrors
					// DailyFantasyPoints's first-appearance handling.
					if !existed {
						row.FPts = 0
					}
				}
			}
			out = append(out, row)
			// Retain latest YTD regardless of active status so future days diff
			// against the real prior YTD (handles two-way players, IL trips).
			prevGS[pid] = snap.GS
			prevFPts[pid] = snap.FPts
		}

		if hitNetwork {
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Map iteration above is randomized, so a stable order is not optional:
	// GetTeamPitcherStarts's output feeds the recap's rendered start list, and
	// this package owes idempotency (see buildGSForecast's confirmed-starter
	// sort for the same reasoning).
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].PitcherName < out[j].PitcherName
	})
	return out, nil
}

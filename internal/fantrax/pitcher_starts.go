package fantrax

import (
	"fmt"
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

// GetTeamPitcherStarts returns every active-slot SP start a team made within
// [start, end] (inclusive). Mirrors the per-day diff logic from GetTeamGS but
// without the gsMax filter — we want all starts, not just overage. Caller
// supplies seasonStart so periods can be derived.
//
// cacheDir/cacheTTL enable per-period snapshot caching (key
// `fantrax-pitcher-gs-<teamID>-<period>`). Past-period snapshots are
// immutable, so a long TTL (e.g. 30d) lets the recap pipeline reuse data
// across runs and avoid re-hitting Fantrax for completed weeks. Pass
// cacheDir="" or cacheTTL=0 to bypass — used by the live gscheck path,
// which always wants fresh data. The 200ms throttle between days only
// fires when the upstream API was actually hit; cache-only days don't
// pace themselves.
func (c *Client) GetTeamPitcherStarts(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]DatedPitcherStart, error) {
	if end.Before(start) {
		return nil, fmt.Errorf("end %s before start %s", end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	var snapCache *cache.FileCache[map[string]playerGSSnapshot]
	if cacheDir != "" && cacheTTL > 0 {
		snapCache = cache.New[map[string]playerGSSnapshot](cacheDir, cacheTTL)
	}

	// Baseline YTD from the day before `start` so the first day yields a
	// single-day delta. Same logic as DailyFantasyPoints baseline handling.
	prevGS := map[string]int{}
	prevFPts := map[string]float64{}
	dayBefore := start.AddDate(0, 0, -1)
	if !dayBefore.Before(seasonStart) {
		basePeriod := PeriodForDate(seasonStart, dayBefore)
		if basePeriod >= 1 {
			info, _, err := c.getPlayerGSSnapshotForPeriodCached(snapCache, teamID, basePeriod)
			if err != nil {
				return nil, fmt.Errorf("baseline pitcher snapshot period %d: %w", basePeriod, err)
			}
			for pid, snap := range info {
				prevGS[pid] = snap.GS
				prevFPts[pid] = snap.FPts
			}
		}
	}

	var starts []DatedPitcherStart
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		period := PeriodForDate(seasonStart, d)
		info, hitNetwork, err := c.getPlayerGSSnapshotForPeriodCached(snapCache, teamID, period)
		if err != nil {
			return nil, fmt.Errorf("pitcher snapshot %s (period %d): %w", d.Format("2006-01-02"), period, err)
		}

		for pid, snap := range info {
			// Only count active-slot starts so a benched/IL pitcher's outing
			// doesn't appear (mirrors GetTeamGS semantics).
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
					fptsDelta := snap.FPts - prevFPts[pid]
					// On first appearance, the prevFPts baseline is zero so the
					// delta would be the YTD total. Zero it so we don't credit
					// pre-window production as same-day points. Mirrors
					// DailyFantasyPoints's first-appearance handling.
					if !existed {
						fptsDelta = 0
					}
					starts = append(starts, DatedPitcherStart{
						PitcherName: snap.Name,
						Date:        d,
						FPts:        fptsDelta,
						MLBTeam:     snap.MLBTeam,
					})
				}
			}
			// Retain latest YTD regardless of active status so future days diff
			// against the real prior YTD (handles two-way players, IL trips).
			prevGS[pid] = snap.GS
			prevFPts[pid] = snap.FPts
		}

		if hitNetwork {
			time.Sleep(200 * time.Millisecond)
		}
	}

	return starts, nil
}

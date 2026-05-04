package fantrax

import (
	"fmt"
	"time"
)

// DatedPitcherStart records a single SP game start with its date and FPts.
// Unlike PitcherStart (which gscheck uses for deduction), this is a complete
// list of all active-slot starts for use in award/highlight reports.
type DatedPitcherStart struct {
	PitcherName string    `json:"pitcher_name"`
	Date        time.Time `json:"date"`
	FPts        float64   `json:"fpts"`
}

// GetTeamPitcherStarts returns every active-slot SP start a team made within
// [start, end] (inclusive). Mirrors the per-day diff logic from GetTeamGS but
// without the gsMax filter — we want all starts, not just overage. Caller
// supplies seasonStart so periods can be derived.
func (c *Client) GetTeamPitcherStarts(teamID string, start, end, seasonStart time.Time) ([]DatedPitcherStart, error) {
	if end.Before(start) {
		return nil, fmt.Errorf("end %s before start %s", end.Format("2006-01-02"), start.Format("2006-01-02"))
	}

	// Baseline YTD from the day before `start` so the first day yields a
	// single-day delta. Same logic as DailyFantasyPoints baseline handling.
	prevGS := map[string]int{}
	prevFPts := map[string]float64{}
	dayBefore := start.AddDate(0, 0, -1)
	if !dayBefore.Before(seasonStart) {
		basePeriod := PeriodForDate(seasonStart, dayBefore)
		if basePeriod >= 1 {
			info, err := c.getPlayerGSSnapshotForPeriod(teamID, basePeriod)
			if err != nil {
				return nil, fmt.Errorf("baseline pitcher snapshot period %d: %w", basePeriod, err)
			}
			for pid, snap := range info {
				prevGS[pid] = snap.gs
				prevFPts[pid] = snap.fpts
			}
		}
	}

	var starts []DatedPitcherStart
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		period := PeriodForDate(seasonStart, d)
		info, err := c.getPlayerGSSnapshotForPeriod(teamID, period)
		if err != nil {
			return nil, fmt.Errorf("pitcher snapshot %s (period %d): %w", d.Format("2006-01-02"), period, err)
		}

		for pid, snap := range info {
			// Only count active-slot starts so a benched/IL pitcher's outing
			// doesn't appear (mirrors GetTeamGS semantics).
			if snap.active {
				prev, existed := prevGS[pid]
				delta := snap.gs - prev
				// First-appearance cap: a pitcher cannot earn more than one GS
				// per day, so cap an unseen pitcher's first delta at 1 to avoid
				// counting pre-period or hitter-slot starts. Mirrors GetTeamGS.
				if !existed && delta > 1 {
					delta = 1
				}
				if delta > 0 {
					fptsDelta := snap.fpts - prevFPts[pid]
					// On first appearance, the prevFPts baseline is zero so the
					// delta would be the YTD total (e.g., a two-way player's
					// hitter accumulation). Cap to DefaultMaxDailyFP, which is
					// the same cap used by DailyFantasyPoints for the same
					// reason (suppress pre-period YTD baselines).
					if !existed && fptsDelta > DefaultMaxDailyFP {
						fptsDelta = DefaultMaxDailyFP
					}
					starts = append(starts, DatedPitcherStart{
						PitcherName: snap.name,
						Date:        d,
						FPts:        fptsDelta,
					})
				}
			}
			// Retain latest YTD regardless of active status so future days diff
			// against the real prior YTD (handles two-way players, IL trips).
			prevGS[pid] = snap.gs
			prevFPts[pid] = snap.fpts
		}

		time.Sleep(200 * time.Millisecond)
	}

	return starts, nil
}

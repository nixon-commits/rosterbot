package lineuprun

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// Trailing-window recency parameters for the hitter blend. rosterbot-2nd
// validated (backtest --recency-experiment, full season + 8 weeks) that a
// bounded 30-day window beats unbounded season-to-date by ~1 pt/game/day of
// realized hitter points: raw YTD double-counts the in-season signal already
// regressed into the depthcharts-ros base. recencyLookbackDays fetches a few
// extra days so the 30-day window is fully populated at the edges.
const (
	recencyWindowDays   = 30
	recencyLookbackDays = 35
)

// windowedHitterRecent builds the trailing-recencyWindowDays hitter recency
// signal (FP/game + games-in-window) from the daily FPts series, as of today.
// It replaces the former unbounded season-to-date snapshot (GetRecentStats):
// each player's RecentStat now reflects only games within the window, so both
// the blended value AND the blend weight (driven by games-in-window) track
// recent form rather than the whole season. Past periods are cached at 30d TTL,
// so warm runs only refetch the last day or two.
func windowedHitterRecent(ft recentStatsClient, teamID string, today, seasonStart time.Time, noCache bool) (map[string]fantrax.RecentStat, error) {
	if seasonStart.IsZero() {
		s, _, err := ft.GetSeasonDateRange()
		if err != nil {
			return nil, fmt.Errorf("season range: %w", err)
		}
		seasonStart = s
	}
	start := today.AddDate(0, 0, -recencyLookbackDays)
	if start.Before(seasonStart) {
		start = seasonStart
	}
	// End at yesterday: today's period is incomplete, and WeightedRecent's
	// leakage guard excludes the as-of day anyway.
	yesterday := today.AddDate(0, 0, -1)
	if yesterday.Before(start) {
		return nil, fmt.Errorf("no completed days before %s", today.Format("2006-01-02"))
	}
	days, err := ft.DailyFantasyPoints(teamID, start, yesterday, seasonStart, cacheDir, cacheTTL(noCache, 30*24*time.Hour))
	if err != nil {
		return nil, err
	}
	// Backfill is best-effort (soft-fails per player); a window value from
	// un-backfilled data is still better than dropping the whole signal.
	_ = ft.BackfillDailyFPts(days)
	return projections.WindowedRecent(days, today, projections.WindowWeight(recencyWindowDays), false), nil
}

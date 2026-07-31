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

// hitterWindowBounds returns the inclusive date range to fetch the daily FPts
// series over. Pure: it depends on nothing but the two dates.
//
// The fetch reaches back recencyLookbackDays rather than recencyWindowDays so
// the 30-day window is fully populated at its far edge, and it stops at
// yesterday because today's period is still open (WindowedRecent's leakage
// guard would exclude the as-of day regardless). Clamping to seasonStart is
// what keeps an early-April run from asking Fantrax for preseason periods.
func hitterWindowBounds(today, seasonStart time.Time) (start, end time.Time, err error) {
	start = today.AddDate(0, 0, -recencyLookbackDays)
	if start.Before(seasonStart) {
		start = seasonStart
	}
	end = today.AddDate(0, 0, -1)
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("no completed days before %s", today.Format("2006-01-02"))
	}
	return start, end, nil
}

// collapseHitterWindow reduces an already-fetched per-day FPts series to the
// per-player recency signal the blend consumes: FP/game and games-in-window,
// counting only games inside the trailing recencyWindowDays as of asOf.
//
// This is the pure core of the hitter recency path — no client, no cache, no
// dates to fetch (rosterbot-6om criterion 2). It is worth naming separately
// because a mistake here is invisible: it does not fail, it silently shifts
// every hitter's Expected Points, and therefore the lineup.
func collapseHitterWindow(days []fantrax.DayRoster, asOf time.Time) map[string]fantrax.RecentStat {
	return projections.WindowedRecent(days, asOf, projections.WindowWeight(recencyWindowDays), false)
}

// windowedHitterRecent builds the trailing-recencyWindowDays hitter recency
// signal (FP/game + games-in-window) from the daily FPts series, as of today.
// It replaces the former unbounded season-to-date snapshot (GetRecentStats):
// each player's RecentStat now reflects only games within the window, so both
// the blended value AND the blend weight (driven by games-in-window) track
// recent form rather than the whole season. Settled past periods are cached at
// fantrax.PastPeriodTTL, so warm runs only refetch the last day or two.
//
// It is the thin fetch wrapper around the two pure functions above: bounds,
// fetch, collapse.
func windowedHitterRecent(ft recentStatsClient, teamID string, today, seasonStart time.Time, noCache bool) (map[string]fantrax.RecentStat, error) {
	if seasonStart.IsZero() {
		s, _, err := ft.GetSeasonDateRange()
		if err != nil {
			return nil, fmt.Errorf("season range: %w", err)
		}
		seasonStart = s
	}
	start, end, err := hitterWindowBounds(today, seasonStart)
	if err != nil {
		return nil, err
	}
	// DailyFantasyPoints resolves the MLB-statsapi backfill internally
	// (best-effort, soft-fails per player), so the window sees real same-day
	// FPts for mid-window first appearances instead of placeholder zeros.
	days, err := ft.DailyFantasyPoints(teamID, start, end, seasonStart, cacheDir, cacheTTL(noCache, fantrax.PastPeriodTTL))
	if err != nil {
		return nil, err
	}
	return collapseHitterWindow(days, today), nil
}

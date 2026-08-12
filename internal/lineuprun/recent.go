package lineuprun

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
	return coverImportedHitters(ft, days, start, end, today), nil
}

// shortCoverageHitters returns the hitters whose roster-derived series starts
// after the trailing window does — the signature of a mid-window acquisition.
//
// The threshold is the start of the 30-day WINDOW, not of the 35-day fetch: the
// extra lookback days exist only to populate the window's far edge, so a player
// first seen at today-32 is fully covered for scoring purposes and must not
// trigger a lookup.
func shortCoverageHitters(days []fantrax.DayRoster, fetchStart, asOf time.Time) []fantrax.MLBPlayerRef {
	coverageStart := asOf.AddDate(0, 0, -recencyWindowDays)
	if fetchStart.After(coverageStart) {
		coverageStart = fetchStart
	}

	firstSeen := map[string]time.Time{}
	names := map[string]string{}
	for _, d := range days {
		for _, p := range d.Players {
			if p.IsPitcher {
				continue
			}
			if seen, ok := firstSeen[p.PlayerID]; !ok || d.Date.Before(seen) {
				firstSeen[p.PlayerID] = d.Date
				names[p.PlayerID] = p.Name
			}
		}
	}

	var out []fantrax.MLBPlayerRef
	for id, first := range firstSeen {
		if first.After(coverageStart) {
			out = append(out, fantrax.MLBPlayerRef{PlayerID: id, Name: names[id]})
		}
	}
	// Deterministic order: the refs reach an upstream fetch and appear in logs.
	sort.Slice(out, func(i, j int) bool { return out[i].PlayerID < out[j].PlayerID })
	return out
}

// coverImportedHitters collapses the roster-derived series, then replaces the
// entry of any mid-window acquisition with one rebuilt from that player's MLB
// game log, so their recency reflects the games they actually played rather
// than the days we happened to roster them (rosterbot-6tw).
//
// The MLB entry REPLACES rather than merges with the roster-derived one: the
// game log already covers the overlapping days, so splicing would double-count
// them.
//
// SOFT-FAIL by design, matching DailyFantasyPoints' own backfill contract — a
// statsapi hiccup leaves the (truncated but real) roster signal standing rather
// than failing the lineup run.
func coverImportedHitters(ft recentStatsClient, days []fantrax.DayRoster, start, end, asOf time.Time) map[string]fantrax.RecentStat {
	recent := collapseHitterWindow(days, asOf)

	refs := shortCoverageHitters(days, start, asOf)
	if len(refs) == 0 {
		return recent
	}
	mlbDays, err := ft.MLBDailyFPts(refs, start, end)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: MLB recency coverage for %d newly acquired hitter(s) unavailable (%v) — using roster-tenure window\n", len(refs), err)
		return recent
	}
	fromMLB := collapseHitterWindow(mlbDays, asOf)
	var covered int
	var unmatched []string
	for _, r := range refs {
		if rs, ok := fromMLB[r.PlayerID]; ok {
			recent[r.PlayerID] = rs
			covered++
		} else {
			unmatched = append(unmatched, r.Name)
		}
	}
	// Reported unconditionally, including the zero case: a silently truncated
	// window is precisely what made rosterbot-6tw invisible for a season.
	fmt.Fprintf(os.Stderr, "mlb recency coverage: %d newly acquired hitter(s), %d covered from game log, %d unmatched\n",
		len(refs), covered, len(unmatched))
	// Naming them is the difference between "some player is on a truncated
	// window" and knowing whether that is a prospect with no MLB games (correct)
	// or a name the MLBAM resolver missed (a real gap) — the same reason
	// teamvalue prints its unmatched roster names rather than only a ratio.
	if len(unmatched) > 0 {
		fmt.Fprintf(os.Stderr, "  unmatched (kept roster-tenure window): %s\n", strings.Join(unmatched, ", "))
	}
	return recent
}

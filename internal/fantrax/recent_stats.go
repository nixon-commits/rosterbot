package fantrax

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/models"
)

// RecentStat holds season-to-date fantasy-points-per-game and games-played for
// a player, extracted from a single Fantrax roster snapshot.
type RecentStat struct {
	FPtsPerGame float64
	GamesPlayed int
}

// extractHitterStats extracts per-player batting stats from a single roster snapshot.
// The Fantrax getTeamRosterInfo API returns cumulative season-to-date stats regardless
// of which period is requested (the period parameter only controls the roster arrangement).
// Nil Stats / Batting / FantasyPointsPerGame are skipped safely.
func extractHitterStats(roster []models.RosterPlayer) map[string]RecentStat {
	result := make(map[string]RecentStat)

	for _, rp := range roster {
		if rp.Stats == nil || rp.Stats.Batting == nil {
			continue
		}
		b := rp.Stats.Batting
		if b.GamesPlayed == nil {
			continue
		}

		gp := *b.GamesPlayed
		stat := RecentStat{GamesPlayed: gp}

		if b.FantasyPointsPerGame != nil && gp > 0 {
			stat.FPtsPerGame = *b.FantasyPointsPerGame
		}

		result[rp.PlayerID] = stat
	}

	return result
}

// GetCurrentPeriod returns the current Fantrax scoring period number.
// Cached under fantrax-current-period-<leagueID>-<YYYY-MM-DD> with a 15m
// TTL when SetCache has been called.
func (c *Client) GetCurrentPeriod() (int, error) {
	if c.cacheDir == "" {
		return c.auth.GetCurrentPeriod()
	}
	fc := cache.New[int](c.cacheDir, c.todayTTL)
	key := cache.Key(keyCurrentPeriod, c.leagueID, time.Now().UTC().Format("2006-01-02"))
	return fc.Get(key, func() (int, error) {
		return c.auth.GetCurrentPeriod()
	})
}

// seasonDateRange describes the season's first and last dates.
type seasonDateRange struct {
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// GetSeasonDateRange returns the first and last dates of the Fantrax season
// by using the scoring periods endpoint which has actual start/end dates.
// Cached under fantrax-season-range-<leagueID> with a 7d TTL when SetCache
// has been called — the season schedule is set at draft time and doesn't
// shift mid-season.
func (c *Client) GetSeasonDateRange() (time.Time, time.Time, error) {
	if c.cacheDir == "" {
		return c.fetchSeasonDateRange()
	}
	fc := cache.New[seasonDateRange](c.cacheDir, c.stableTTL)
	key := cache.Key(keySeasonRange, c.leagueID)
	r, err := fc.Get(key, func() (seasonDateRange, error) {
		first, last, err := c.fetchSeasonDateRange()
		return seasonDateRange{First: first, Last: last}, err
	})
	return r.First, r.Last, err
}

// fetchSeasonDateRange is the uncached upstream fetch.
func (c *Client) fetchSeasonDateRange() (time.Time, time.Time, error) {
	periods, _, _, err := c.GetScoringPeriodsAndTeams()
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get scoring periods: %w", err)
	}
	if len(periods) == 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("no scoring periods found")
	}

	first := periods[0].StartDate
	last := periods[0].EndDate
	for _, p := range periods[1:] {
		if p.StartDate.Before(first) {
			first = p.StartDate
		}
		if p.EndDate.After(last) {
			last = p.EndDate
		}
	}
	return first, last, nil
}

// parseMatchupDate parses the date string from a Matchup (e.g. "Sat Apr 19, 2025").
func parseMatchupDate(s string) (time.Time, error) {
	return time.Parse("Mon Jan 2, 2006", s)
}

// AnchorPeriodForDate maps a calendar date to its daily scoring period using an
// authoritative (anchorPeriod @ anchorDate) pair: period(date) = anchorPeriod +
// whole calendar days from anchorDate to date.
//
// Prefer this over PeriodForDate for dates near "today", anchored on Fantrax's
// authoritative current period (GetCurrentPeriod). Fantrax inserts extra daily
// scoring periods mid-season (doubleheaders / postponed-game makeups), so naive
// season-start day counting drifts behind by the number of inserted periods.
// Anchoring on the current period is exact for any date in an insertion-free
// span containing anchorDate; for dates separated from anchorDate by an
// insertion it drifts by that insertion count (acceptable for the near-today
// callers — deep-historical mapping is out of scope here).
func AnchorPeriodForDate(anchorDate time.Time, anchorPeriod int, date time.Time) int {
	days := int(date.Truncate(24*time.Hour).Sub(anchorDate.Truncate(24*time.Hour)).Hours() / 24)
	return anchorPeriod + days
}

// PeriodForDate returns the daily scoring period number for the given date,
// anchored on the season start (period 1 = seasonStart). This assumes exactly
// one scoring period per calendar day, which Fantrax violates when it inserts
// extra daily periods mid-season — see AnchorPeriodForDate. Retained as the
// fallback anchor when the authoritative current period is unavailable.
func PeriodForDate(seasonStart, date time.Time) int {
	return AnchorPeriodForDate(seasonStart, 1, date)
}

// Note: the former GetRecentStats (unbounded season-to-date hitter FP/G from a
// single roster snapshot) was retired when the optimizer switched to a bounded
// trailing-30-day recency window (see cmd/optimize.go windowedHitterRecent,
// rosterbot-2nd). The window is built from the daily FPts series instead of one
// cumulative snapshot. extractHitterStats is retained for its unit tests and as
// the shared snapshot→RecentStat adapter.

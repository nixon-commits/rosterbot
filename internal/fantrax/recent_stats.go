package fantrax

import (
	"fmt"
	"strconv"
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
	key := cache.Key("fantrax-current-period", c.leagueID, time.Now().UTC().Format("2006-01-02"))
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
	key := cache.Key("fantrax-season-range", c.leagueID)
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

// PeriodForDate returns the daily scoring period number for the given date.
// Periods are 1-indexed days from the season start (period 1 = seasonStart).
func PeriodForDate(seasonStart, date time.Time) int {
	days := int(date.Truncate(24*time.Hour).Sub(seasonStart.Truncate(24*time.Hour)).Hours() / 24)
	return days + 1 // period 1 = day 0
}

// GetRecentStats fetches the most recent completed period's roster and returns
// the cumulative season-to-date batting stats for each player.
//
// The Fantrax getTeamRosterInfo API returns YTD stats regardless of which period
// is requested — the period parameter only controls the roster snapshot. We fetch
// the latest completed period (currentPeriod-1) to get current YTD stats.
//
// Cached under fantrax-recent-stats-hitter-<teamID>-<period> with a TTL
// determined by ttlForPeriod (30d for past, todayTTL otherwise).
func (c *Client) GetRecentStats(currentPeriod, _ int) (map[string]RecentStat, error) {
	period := currentPeriod - 1
	if period < 1 {
		return nil, fmt.Errorf("no completed periods (current=%d)", currentPeriod)
	}

	if c.cacheDir == "" {
		return c.fetchRecentStats(period)
	}
	fc := cache.New[map[string]RecentStat](c.cacheDir, c.ttlForPeriod(period))
	key := cache.Key("fantrax-recent-stats-hitter", c.teamID, strconv.Itoa(period))
	return fc.Get(key, func() (map[string]RecentStat, error) {
		return c.fetchRecentStats(period)
	})
}

func (c *Client) fetchRecentStats(period int) (map[string]RecentStat, error) {
	roster, err := c.auth.GetTeamRosterInfo(strconv.Itoa(period), c.teamID)
	if err != nil {
		return nil, fmt.Errorf("fetch roster for period %d: %w", period, err)
	}

	players := append(roster.ActiveRoster, roster.ReserveRoster...)
	return extractHitterStats(players), nil
}

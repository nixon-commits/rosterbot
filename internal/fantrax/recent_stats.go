package fantrax

import (
	"fmt"
	"strconv"
	"time"

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
func (c *Client) GetCurrentPeriod() (int, error) {
	return c.auth.GetCurrentPeriod()
}

// GetSeasonDateRange returns the first and last dates of the Fantrax season
// by using the scoring periods endpoint which has actual start/end dates.
func (c *Client) GetSeasonDateRange() (time.Time, time.Time, error) {
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
func (c *Client) GetRecentStats(currentPeriod, _ int) (map[string]RecentStat, error) {
	period := currentPeriod - 1
	if period < 1 {
		return nil, fmt.Errorf("no completed periods (current=%d)", currentPeriod)
	}

	roster, err := c.auth.GetTeamRosterInfo(strconv.Itoa(period), c.teamID)
	if err != nil {
		return nil, fmt.Errorf("fetch roster for period %d: %w", period, err)
	}

	players := append(roster.ActiveRoster, roster.ReserveRoster...)
	return extractHitterStats(players), nil
}

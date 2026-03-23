package fantrax

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/pmurley/go-fantrax/models"
	"golang.org/x/sync/errgroup"
)

// RecentStat holds aggregated fantasy-point and games-played totals for a player
// across one or more recent scoring periods.
type RecentStat struct {
	TotalFP     float64
	GamesPlayed int
}

// aggregateRecentStats combines per-player stats across multiple periods.
// Each element of periods is a flat slice of RosterPlayer entries for that period.
// Nil Stats / Batting / FantasyPointsPerGame are skipped safely.
func aggregateRecentStats(periods [][]models.RosterPlayer) map[string]RecentStat {
	result := make(map[string]RecentStat)

	for _, period := range periods {
		for _, rp := range period {
			if rp.Stats == nil || rp.Stats.Batting == nil {
				continue
			}
			b := rp.Stats.Batting
			if b.GamesPlayed == nil {
				continue
			}

			stat := result[rp.PlayerID]

			gp := *b.GamesPlayed
			stat.GamesPlayed += gp

			if b.FantasyPointsPerGame != nil && gp > 0 {
				stat.TotalFP += *b.FantasyPointsPerGame * float64(gp)
			}

			result[rp.PlayerID] = stat
		}
	}

	return result
}

// GetCurrentPeriod returns the current Fantrax scoring period number.
func (c *Client) GetCurrentPeriod() (int, error) {
	return c.auth.GetCurrentPeriod()
}

// GetSeasonDateRange returns the first and last dates of the Fantrax season
// by inspecting all scoring period matchups.
func (c *Client) GetSeasonDateRange() (time.Time, time.Time, error) {
	result, err := c.auth.GetAllMatchups()
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get all matchups: %w", err)
	}

	var first, last time.Time
	for _, m := range result.Matchups {
		t, err := parseMatchupDate(m.Date)
		if err != nil {
			continue
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if last.IsZero() || t.After(last) {
			last = t
		}
	}
	if first.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("no scoring periods found")
	}
	// Last period date is the start of the last period; add 6 days as buffer.
	// The MLB schedule API will naturally skip off-days.
	last = last.AddDate(0, 0, 6)
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

// GetRecentStats fetches roster data for the last numPeriods scoring periods
// and aggregates per-player stats. Periods are fetched in parallel via errgroup.
func (c *Client) GetRecentStats(currentPeriod, numPeriods int) (map[string]RecentStat, error) {
	// Collect valid period numbers (count backwards, skip <= 0).
	var periodNums []int
	for p := currentPeriod - 1; p >= currentPeriod-numPeriods && p > 0; p-- {
		periodNums = append(periodNums, p)
	}

	results := make([][]models.RosterPlayer, len(periodNums))

	var g errgroup.Group
	for i, p := range periodNums {
		i, p := i, p // capture loop vars
		g.Go(func() error {
			roster, err := c.auth.GetTeamRosterInfo(strconv.Itoa(p), c.teamID)
			if err != nil {
				log.Printf("warning: failed to fetch roster for period %d: %v", p, err)
				return nil // non-fatal; leave results[i] as nil
			}
			results[i] = append(roster.ActiveRoster, roster.ReserveRoster...)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Filter out nil slices from failed periods.
	var periods [][]models.RosterPlayer
	for _, r := range results {
		if r != nil {
			periods = append(periods, r)
		}
	}

	return aggregateRecentStats(periods), nil
}

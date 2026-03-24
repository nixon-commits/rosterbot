package fantrax

import (
	"sort"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

// MatchupWeekBounds returns the inclusive [start, end] calendar dates of the
// matchup week that contains date for the given fantasy teamID.
// It groups consecutive scoring periods where teamID faces the same opponent.
// Returns zero times if no matchup week contains the date.
func MatchupWeekBounds(
	matchups []auth_client.Matchup,
	teamID string,
	seasonStart time.Time,
	date time.Time,
) (weekStart, weekEnd time.Time) {
	type entry struct {
		period   int
		opponent string
		date     time.Time
	}

	var mine []entry
	for _, m := range matchups {
		var opp string
		if m.AwayTeam.TeamID == teamID {
			opp = m.HomeTeam.TeamID
		} else if m.HomeTeam.TeamID == teamID {
			opp = m.AwayTeam.TeamID
		} else {
			continue
		}
		t, err := parseMatchupDate(m.Date)
		if err != nil {
			continue
		}
		mine = append(mine, entry{m.ScoringPeriod, opp, t})
	}

	sort.Slice(mine, func(i, j int) bool { return mine[i].period < mine[j].period })

	targetPeriod := PeriodForDate(seasonStart, date)

	// Walk sorted entries and group consecutive same-opponent runs.
	i := 0
	for i < len(mine) {
		j := i + 1
		for j < len(mine) && mine[j].opponent == mine[i].opponent && mine[j].period == mine[i].period+(j-i) {
			j++
		}
		// Run is [i, j). Check if targetPeriod falls in this run.
		if targetPeriod >= mine[i].period && targetPeriod <= mine[j-1].period {
			return mine[i].date, mine[j-1].date
		}
		i = j
	}
	return time.Time{}, time.Time{}
}

// GetMatchupWeekBounds is a convenience method that fetches all matchups and
// returns the week boundaries for the given date.
func (c *Client) GetMatchupWeekBounds(date time.Time, seasonStart time.Time) (weekStart, weekEnd time.Time, err error) {
	result, err := c.auth.GetAllMatchups()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	ws, we := MatchupWeekBounds(result.Matchups, c.teamID, seasonStart, date)
	return ws, we, nil
}

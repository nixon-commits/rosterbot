package fantrax

import (
	"sort"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

// matchupWeekRanges returns the [start, end] bounds of every matchup week the
// team has, ordered by start date (earliest first). Each entry is one
// continuous run of same-opponent matchup entries.
//
// The upstream Fantrax SCHEDULE response can contain a row for the same date
// from both the completed (H2hPointsBased3) and future (H2hPointsBased2)
// tables, which the simple grouping algorithm interprets as two adjacent
// runs and produces a zero-day "phantom" between every real week. We dedupe
// (date, opponent) pairs before grouping and drop any zero-day ranges that
// remain so the resulting list has clean 1:1 numbering with real weeks.
func matchupWeekRanges(matchups []auth_client.Matchup, teamID string) []dateRange {
	type entry struct {
		opponent string
		date     time.Time
	}

	seen := map[string]bool{}
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
		key := t.Format("2006-01-02") + "|" + opp
		if seen[key] {
			continue
		}
		seen[key] = true
		mine = append(mine, entry{opp, t})
	}

	sort.Slice(mine, func(i, j int) bool { return mine[i].date.Before(mine[j].date) })

	var ranges []dateRange
	i := 0
	for i < len(mine) {
		j := i + 1
		for j < len(mine) && mine[j].opponent == mine[i].opponent {
			j++
		}
		runStart := mine[i].date
		var runEnd time.Time
		if j < len(mine) {
			runEnd = mine[j].date.AddDate(0, 0, -1)
		} else {
			runEnd = mine[j-1].date.AddDate(0, 0, 6)
		}
		// Drop zero/negative-day ranges that survived dedup (defensive).
		if !runEnd.Before(runStart) {
			ranges = append(ranges, dateRange{start: runStart, end: runEnd})
		}
		i = j
	}
	return ranges
}

type dateRange struct{ start, end time.Time }

// MatchupWeekBounds returns the inclusive [start, end] calendar dates of the
// matchup week that contains date for the given fantasy teamID.
// It groups consecutive same-opponent matchup entries (which are weekly, not daily)
// and uses date ranges to determine which week the target date falls in.
// Returns zero times if no matchup week contains the date.
func MatchupWeekBounds(
	matchups []auth_client.Matchup,
	teamID string,
	seasonStart time.Time,
	date time.Time,
) (weekStart, weekEnd time.Time) {
	dateYMD := date.Format("2006-01-02")
	for _, r := range matchupWeekRanges(matchups, teamID) {
		if dateYMD >= r.start.Format("2006-01-02") && dateYMD <= r.end.Format("2006-01-02") {
			return r.start, r.end
		}
	}
	return time.Time{}, time.Time{}
}

// MatchupWeekByNumber returns the [start, end] bounds of the n-th matchup week
// (1-indexed) for the given team. Returns zero times if n is out of range.
func MatchupWeekByNumber(matchups []auth_client.Matchup, teamID string, n int) (weekStart, weekEnd time.Time) {
	if n < 1 {
		return time.Time{}, time.Time{}
	}
	ranges := matchupWeekRanges(matchups, teamID)
	if n > len(ranges) {
		return time.Time{}, time.Time{}
	}
	r := ranges[n-1]
	return r.start, r.end
}

// MatchupWeekNumberForDate returns the 1-indexed matchup week containing the
// given date for the team. Returns 0 if the date isn't in any week. Use this
// instead of arithmetic on seasonStart — Fantrax's week boundaries don't
// always sit on a 7-day grid (mid-week starts, doubleheader days, etc.).
func MatchupWeekNumberForDate(matchups []auth_client.Matchup, teamID string, date time.Time) int {
	dateYMD := date.Format("2006-01-02")
	for i, r := range matchupWeekRanges(matchups, teamID) {
		if dateYMD >= r.start.Format("2006-01-02") && dateYMD <= r.end.Format("2006-01-02") {
			return i + 1
		}
	}
	return 0
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

// GetMatchupWeekByNumber fetches all matchups and returns the bounds of the
// n-th matchup week (1-indexed) for the configured team.
func (c *Client) GetMatchupWeekByNumber(n int) (weekStart, weekEnd time.Time, err error) {
	result, err := c.auth.GetAllMatchups()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	ws, we := MatchupWeekByNumber(result.Matchups, c.teamID, n)
	return ws, we, nil
}

// GetMatchupWeekNumberForDate returns the 1-indexed matchup week that contains
// the given date for the configured team. Returns 0 if the date isn't in any
// week (or before the season).
func (c *Client) GetMatchupWeekNumberForDate(date time.Time) (int, error) {
	result, err := c.auth.GetAllMatchups()
	if err != nil {
		return 0, err
	}
	return MatchupWeekNumberForDate(result.Matchups, c.teamID, date), nil
}

// MatchupEntry is a thin H2H pairing record extracted from the upstream
// matchups API. Use GetAllMatchupEntries to fetch them in a form that doesn't
// leak the auth_client type.
type MatchupEntry struct {
	ScoringPeriod int
	Date          string
	HomeID        string
	AwayID        string
}

// GetAllMatchupEntries returns all matchup pairings for the season.
func (c *Client) GetAllMatchupEntries() ([]MatchupEntry, error) {
	result, err := c.auth.GetAllMatchups()
	if err != nil {
		return nil, err
	}
	out := make([]MatchupEntry, 0, len(result.Matchups))
	for _, m := range result.Matchups {
		out = append(out, MatchupEntry{
			ScoringPeriod: m.ScoringPeriod,
			Date:          m.Date,
			HomeID:        m.HomeTeam.TeamID,
			AwayID:        m.AwayTeam.TeamID,
		})
	}
	return out, nil
}

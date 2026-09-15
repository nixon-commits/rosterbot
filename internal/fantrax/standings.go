package fantrax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/auth_client"
)

// StandingRow is one team's line in Fantrax's own standings table.
type StandingRow struct {
	Rank          int
	TeamID        string
	TeamName      string
	Wins          int
	Losses        int
	Ties          int
	WinPct        float64
	GamesBack     float64
	PointsFor     float64
	PointsAgainst float64
	Streak        string
}

// fetchStandingsFn is the seam tests use to stub the standings fetch.
var fetchStandingsFn = (*Client).fetchStandings

// GetStandings returns Fantrax's standings table as Fantrax renders it — rank,
// record, points for and against — never recomputed from matchups. Cached at
// tierToday (fantrax-standings-<leagueID>).
//
// It reads the table by HEADER KEY, not column position. The fork's
// positional ProcessStandings assumes a ten-column table and on this league's
// nine-column one it puts games-back in the division column and zero in
// points-for (measured 2026-09-14), which would have rendered the year-end
// standings confidently wrong.
func (c *Client) GetStandings() ([]StandingRow, error) {
	return cached(c, cache.Key(keyStandings, c.leagueID), tierToday,
		func() ([]StandingRow, error) { return fetchStandingsFn(c) })
}

// fetchStandings posts getStandings for the default (COMBINED) view and parses
// its standings table.
func (c *Client) fetchStandings() ([]StandingRow, error) {
	fullRequest := auth_client.BuildFullRequest(
		[]auth_client.FantraxMessage{{
			Method: "getStandings",
			Data:   map[string]string{"leagueId": c.leagueID, "view": string(auth_client.StandingsViewCombined)},
		}},
		fmt.Sprintf("https://www.fantrax.com/fantasy/league/%s/standings", c.leagueID),
	)
	jsonStr, err := json.Marshal(fullRequest)
	if err != nil {
		return nil, fmt.Errorf("marshal standings request: %w", err)
	}
	// context.Background() for the same reason GetScoringPeriodsAndTeams
	// gives: *Client's method surface carries no ctx.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, standingsURL+"?leagueId="+c.leagueID, bytes.NewBuffer(jsonStr))
	if err != nil {
		return nil, fmt.Errorf("create standings request: %w", err)
	}
	resp, err := c.auth.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send standings request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("standings API returned status %d", resp.StatusCode)
	}
	body, err := auth_client.ReadBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read standings response: %w", err)
	}
	var sr auth_client.StandingsResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("unmarshal standings response: %w", err)
	}
	if len(sr.Responses) == 0 {
		return nil, fmt.Errorf("no response data in standings")
	}
	return parseStandings(sr.Responses[0].Data)
}

// parseStandings reads the H2hPointsBased1 table by header key. A response
// with no such table is an error: an empty standings must never render as a
// clean board.
func parseStandings(data auth_client.ResponseData) ([]StandingRow, error) {
	for _, tbl := range data.TableList {
		if tbl.TableType != "H2hPointsBased1" {
			continue
		}
		col := map[string]int{}
		for i, h := range tbl.Header.Cells {
			col[h.Key] = i
		}
		cell := func(row auth_client.Row, key string) string {
			i, ok := col[key]
			if !ok || i >= len(row.Cells) {
				return ""
			}
			return strings.TrimSpace(row.Cells[i].Content)
		}
		num := func(s string) float64 {
			f, _ := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
			return f
		}
		var rows []StandingRow
		for _, r := range tbl.Rows {
			if len(r.FixedCells) < 2 || r.FixedCells[1].TeamID == "" {
				continue
			}
			name := r.FixedCells[1].Content
			if info, ok := data.FantasyTeamInfo[r.FixedCells[1].TeamID]; ok && info.Name != "" {
				name = info.Name
			}
			rows = append(rows, StandingRow{
				Rank:          int(num(r.FixedCells[0].Content)),
				TeamID:        r.FixedCells[1].TeamID,
				TeamName:      name,
				Wins:          int(num(cell(r, "win"))),
				Losses:        int(num(cell(r, "loss"))),
				Ties:          int(num(cell(r, "tie"))),
				WinPct:        num(cell(r, "winpc")),
				GamesBack:     num(cell(r, "gamesback")),
				PointsFor:     num(cell(r, "pointsFor")),
				PointsAgainst: num(cell(r, "pointsAgainst")),
				Streak:        cell(r, "streak"),
			})
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("standings table has no team rows")
		}
		return rows, nil
	}
	return nil, fmt.Errorf("no standings table (H2hPointsBased1) in response")
}

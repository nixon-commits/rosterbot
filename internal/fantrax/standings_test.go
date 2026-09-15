package fantrax

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

// The standings table is read by HEADER KEY, never by column position: the
// fork's positional parser puts games-back in the division column and zero
// in points-for on this league, whose table has nine cells in a different
// order. The fixture below deliberately shuffles the columns so a positional
// read cannot pass by luck.
const standingsFixture = `{"responses":[{"data":{
 "fantasyTeamInfo":{"h":{"name":"Houston Swang and Bang"},"j":{"name":"jimmydyl"}},
 "tableList":[{"tableType":"H2hPointsBased1","caption":"Standings",
  "fixedHeader":{"cells":[{"key":"rank"},{"key":"team"}]},
  "header":{"cells":[{"key":"pointsFor"},{"key":"streak"},{"key":"win"},{"key":"gamesback"},{"key":"loss"},{"key":"pointsAgainst"},{"key":"tie"},{"key":"winpc"},{"key":"wwOrder"}]},
  "rows":[
   {"fixedCells":[{"content":"1"},{"content":"Houston Swang and Bang","teamId":"h"}],
    "cells":[{"content":"14798.5"},{"content":"W3"},{"content":"32"},{"content":"0"},{"content":"12"},{"content":"13000"},{"content":"0"},{"content":".727"},{"content":"9"}]},
   {"fixedCells":[{"content":"2"},{"content":"jimmydyl","teamId":"j"}],
    "cells":[{"content":"14023"},{"content":"L1"},{"content":"26"},{"content":"6.0"},{"content":"18"},{"content":"13500"},{"content":"0"},{"content":".591"},{"content":"4"}]}
  ]}]}}]}`

func TestParseStandings_ByHeaderKey(t *testing.T) {
	var resp auth_client.StandingsResponse
	if err := json.Unmarshal([]byte(standingsFixture), &resp); err != nil {
		t.Fatal(err)
	}
	rows, err := parseStandings(resp.Responses[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	h := rows[0]
	if h.Rank != 1 || h.TeamID != "h" || h.TeamName != "Houston Swang and Bang" {
		t.Errorf("row 0 identity = %+v", h)
	}
	if h.Wins != 32 || h.Losses != 12 || h.Ties != 0 || h.WinPct != 0.727 || h.GamesBack != 0 || h.PointsFor != 14798.5 || h.PointsAgainst != 13000 || h.Streak != "W3" {
		t.Errorf("row 0 stats = %+v, want 32-12-0 .727 gb 0 PF 14798.5 PA 13000 W3", h)
	}
	if j := rows[1]; j.GamesBack != 6 || j.Wins != 26 || j.Rank != 2 {
		t.Errorf("row 1 = %+v, want rank 2, 26 wins, 6 GB", j)
	}
}

// A response without a standings table is an error, never an empty
// standings — the year-end page must not render ten missing teams as a
// clean board.
func TestParseStandings_MissingTableIsAnError(t *testing.T) {
	var resp auth_client.StandingsResponse
	if err := json.Unmarshal([]byte(`{"responses":[{"data":{"tableList":[]}}]}`), &resp); err != nil {
		t.Fatal(err)
	}
	if _, err := parseStandings(resp.Responses[0].Data); err == nil {
		t.Fatal("want an error for a response with no H2hPointsBased1 table")
	}
}

// GetStandings reads through the cache like every other Fantrax read.
func TestGetStandings_CachedAndErrorsNotCached(t *testing.T) {
	c := &Client{leagueID: "lg1", cacheDir: t.TempDir()}
	calls := 0
	orig := fetchStandingsFn
	fetchStandingsFn = func(*Client) ([]StandingRow, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("fantrax down")
		}
		return []StandingRow{{Rank: 1, TeamID: "h", TeamName: "Houston"}}, nil
	}
	t.Cleanup(func() { fetchStandingsFn = orig })
	if _, err := c.GetStandings(); err == nil {
		t.Fatal("first call should surface the fetch error")
	}
	for i := 0; i < 2; i++ {
		rows, err := c.GetStandings()
		if err != nil || len(rows) != 1 || rows[0].TeamID != "h" {
			t.Fatalf("call %d = (%+v, %v)", i, rows, err)
		}
	}
	if calls != 2 {
		t.Errorf("fetched %d times, want 2 (error not cached, success cached)", calls)
	}
}

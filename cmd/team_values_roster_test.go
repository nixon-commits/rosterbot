package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/pmurley/go-fantrax/models"
)

// buildRosterValues is a mapping, and the two things a mapping can get wrong
// are pinned here: the pool fields it forwards (PosShortNames, not
// MultiPositions — see TestPositionDisplay for why) and where a team's name
// comes from (standings first, the pool's own name when standings failed).
func TestBuildRosterValues_MapsPoolFieldsAndPrefersStandingsNames(t *testing.T) {
	pool := []models.PoolPlayer{
		{PlayerID: "p1", Name: "Bobby Witt Jr.", MLBTeamShortName: "KC", PosShortNames: "SS,<b>INF</b>",
			FantasyTeamID: "t1", FantasyTeamName: "Pool Name One"},
		{PlayerID: "p2", Name: "Deep Farmhand", MLBTeamShortName: "TB", PosShortNames: "OF",
			FantasyTeamID: "t2", FantasyTeamName: "Pool Name Two", MinorsEligible: true},
		{PlayerID: "p3", Name: "Nobody Owns Me", MLBTeamShortName: "NYY", PosShortNames: "C"},
	}
	hkbPlayers := []hkb.Player{
		{Name: "Bobby Witt Jr.", Team: "KC", Value: 9000, Rank: 1, Positions: []string{"SS"}},
	}
	rows := []teamvalue.Row{
		{TeamID: "t1", TeamName: "Pool Name One"},
		{TeamID: "t2", TeamName: "Pool Name Two"},
	}
	teamNames := map[string]string{"t1": "Standings Name One"} // t2 missing: standings failed for it

	tables := buildRosterValues(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), pool, hkbPlayers, rows, teamNames)

	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2 (the free agent produces none)", len(tables))
	}
	t1, t2 := tables["t1"], tables["t2"]
	if t1.TeamName != "Standings Name One" {
		t.Errorf("t1 name = %q, want the standings name", t1.TeamName)
	}
	if t2.TeamName != "Pool Name Two" {
		t.Errorf("t2 name = %q, want the pool's own name as the fallback", t2.TeamName)
	}
	if t1.HKBAsOf != "2026-09-02" {
		t.Errorf("hkb_as_of = %q", t1.HKBAsOf)
	}
	witt := t1.MLB[0]
	if witt.ID != "p1" || witt.MLBTeam != "KC" || witt.HKBValue == nil || *witt.HKBValue != 9000 {
		t.Errorf("witt row = %+v", witt)
	}
	if len(witt.FantraxPos) != 2 || witt.FantraxPos[0] != "SS" || witt.FantraxPos[1] != "INF" {
		t.Errorf("fantrax_pos = %v, want [SS INF] from PosShortNames with markup stripped", witt.FantraxPos)
	}
	if len(t2.Prospects) != 1 || t2.Prospects[0].Name != "Deep Farmhand" {
		t.Errorf("minors eligibility must reach the builder: %+v", t2)
	}
}

package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func testLeague() *sleeper.League {
	return &sleeper.League{
		RosterPositions: []string{"QB", "RB", "WR", "BN", "BN"},
		Settings:        map[string]int{"draft_rounds": 5},
	}
}

func TestAggregatePlayers_MatchedJoinsAllFourFormats(t *testing.T) {
	rosters := []sleeper.Roster{
		{RosterID: 1, OwnerID: "u1", Starters: []string{"9509"}, Players: []string{"9509", "4984"}},
	}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Jon", Metadata: struct {
		TeamName string `json:"team_name"`
	}{TeamName: "Palm Trees"}}}
	players := map[string]sleeper.Player{
		"9509": {PlayerID: "9509", FirstName: "Bijan", LastName: "Robinson", Position: "RB"},
		"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen", Position: "QB"},
	}
	bundle := &statsguy.Bundle{Players: map[string]statsguy.Player{
		"9509": {ID: "9509", Name: "Bijan Robinson", Position: "RB",
			Value: statsguy.FormatValues{SFDynasty: 10000, NonSFDynasty: 10000, SFRedraft: 9772, NonSFRedraft: 9996}},
		"4984": {ID: "4984", Name: "Josh Allen", Position: "QB",
			Value: statsguy.FormatValues{SFDynasty: 9480, NonSFDynasty: 5047, SFRedraft: 9905, NonSFRedraft: 4306}},
	}}

	rows, coverage := AggregatePlayers(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), testLeague(), rosters, users, players, bundle)

	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	var bijan, allen *Row
	for i := range rows {
		switch rows[i].AssetID {
		case "9509":
			bijan = &rows[i]
		case "4984":
			allen = &rows[i]
		}
	}
	if bijan == nil || allen == nil {
		t.Fatalf("rows = %+v, missing expected assets", rows)
	}
	if bijan.TeamName != "Palm Trees" || bijan.TeamID != "1" || bijan.AssetType != "player" {
		t.Errorf("bijan = %+v, unexpected team fields", bijan)
	}
	if bijan.ValueSFDynasty != 10000 || bijan.ValueSFRedraft != 9772 {
		t.Errorf("bijan value = %+v, missing formats", bijan)
	}
	if !bijan.IsStarter {
		t.Error("bijan should be tagged IsStarter (in roster.Starters)")
	}
	if allen.IsStarter {
		t.Error("allen should NOT be tagged IsStarter (bench)")
	}

	if len(coverage) != 1 {
		t.Fatalf("len(coverage) = %d, want 1", len(coverage))
	}
	if coverage[0].RosteredCount != 2 || coverage[0].MatchedCount != 2 || len(coverage[0].Unmatched) != 0 {
		t.Errorf("coverage[0] = %+v, want rostered=2 matched=2 unmatched=0", coverage[0])
	}
}

func TestAggregatePlayers_UnmatchedTracksCoverageNotRow(t *testing.T) {
	rosters := []sleeper.Roster{
		{RosterID: 1, OwnerID: "u1", Starters: []string{}, Players: []string{"9999"}},
	}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Jon"}}
	players := map[string]sleeper.Player{
		"9999": {PlayerID: "9999", FirstName: "No", LastName: "Value", Position: "K"},
	}
	bundle := &statsguy.Bundle{Players: map[string]statsguy.Player{}} // no match

	rows, coverage := AggregatePlayers(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), testLeague(), rosters, users, players, bundle)

	if len(rows) != 0 {
		t.Errorf("rows = %+v, want 0 (unvalued player produces no row)", rows)
	}
	if len(coverage) != 1 || coverage[0].RosteredCount != 1 || coverage[0].MatchedCount != 0 {
		t.Fatalf("coverage = %+v, want rostered=1 matched=0", coverage)
	}
	if len(coverage[0].Unmatched) != 1 || coverage[0].Unmatched[0] != "No Value" {
		t.Errorf("Unmatched = %v, want [\"No Value\"]", coverage[0].Unmatched)
	}
}

func TestAggregatePlayers_TeamNameFallsBackToDisplayName(t *testing.T) {
	rosters := []sleeper.Roster{{RosterID: 1, OwnerID: "u1", Players: []string{}}}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Jon"}} // no metadata.team_name

	rows, coverage := AggregatePlayers(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), testLeague(), rosters, users, nil, &statsguy.Bundle{})
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want 0", rows)
	}
	if len(coverage) != 1 || coverage[0].TeamName != "Jon" {
		t.Errorf("coverage = %+v, want TeamName=Jon (display_name fallback)", coverage)
	}
}

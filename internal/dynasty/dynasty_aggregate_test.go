package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func TestAggregate_CombinesPlayersAndPicks(t *testing.T) {
	league := &sleeper.League{
		RosterPositions: []string{"QB", "BN"},
		Settings:        map[string]int{"draft_rounds": 1},
	}
	rosters := []sleeper.Roster{
		{RosterID: 1, OwnerID: "u1", Starters: []string{"4984"}, Players: []string{"4984"}},
	}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Team1"}}
	players := map[string]sleeper.Player{"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen"}}
	bundle := &statsguy.Bundle{
		Players: map[string]statsguy.Player{
			"4984": {ID: "4984", Name: "Josh Allen", Value: statsguy.FormatValues{SFDynasty: 9480}},
		},
		Picks: []statsguy.Pick{
			{Year: 2027, Round: 1, Variant: "mid", Value: statsguy.FormatValues{SFDynasty: 3000}},
		},
	}

	rows, coverage := Aggregate(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), league, rosters, users, nil, players, bundle)

	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (1 player + 1 pick)", len(rows))
	}
	types := map[string]int{}
	for _, r := range rows {
		types[r.AssetType]++
	}
	if types["player"] != 1 || types["pick"] != 1 {
		t.Errorf("asset type counts = %+v, want player=1 pick=1", types)
	}
	if len(coverage) != 1 || coverage[0].MatchedCount != 1 {
		t.Errorf("coverage = %+v, want one team, matched=1", coverage)
	}
}

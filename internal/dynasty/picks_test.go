package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func testBundleWithPicks() *statsguy.Bundle {
	return &statsguy.Bundle{
		Players: map[string]statsguy.Player{},
		Picks: []statsguy.Pick{
			{ID: "pick:2027:1:early", Year: 2027, Round: 1, Variant: "early", Value: statsguy.FormatValues{SFDynasty: 4000}},
			{ID: "pick:2027:1:mid", Year: 2027, Round: 1, Variant: "mid", Value: statsguy.FormatValues{SFDynasty: 3000}},
			{ID: "pick:2027:1:late", Year: 2027, Round: 1, Variant: "late", Value: statsguy.FormatValues{SFDynasty: 2000}},
			{ID: "pick:2027:1.01", Year: 2027, Round: 1, Slot: 1, Value: statsguy.FormatValues{SFDynasty: 5000}}, // resolved slot, not "mid" -- must be ignored
			{ID: "pick:2027:2:mid", Year: 2027, Round: 2, Variant: "mid", Value: statsguy.FormatValues{SFDynasty: 500}},
		},
	}
}

func TestAggregatePicks_DefaultOwnershipIsIdentity(t *testing.T) {
	league := &sleeper.League{Settings: map[string]int{"draft_rounds": 2}}
	rosters := []sleeper.Roster{{RosterID: 1, OwnerID: "u1"}, {RosterID: 2, OwnerID: "u2"}}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Team1"}, {UserID: "u2", DisplayName: "Team2"}}

	rows := AggregatePicks(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), league, rosters, users, nil, testBundleWithPicks())

	// 2 teams x 1 year (2027, the only year in the bundle) x 2 rounds = 4 picks.
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4; rows=%+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.AssetType != "pick" || !r.Estimated {
			t.Errorf("row %+v: want asset_type=pick, estimated=true", r)
		}
	}
}

func TestAggregatePicks_PricedFromMidVariantOnly(t *testing.T) {
	league := &sleeper.League{Settings: map[string]int{"draft_rounds": 1}}
	rosters := []sleeper.Roster{{RosterID: 1, OwnerID: "u1"}}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Team1"}}

	rows := AggregatePicks(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), league, rosters, users, nil, testBundleWithPicks())

	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].ValueSFDynasty != 3000 {
		t.Errorf("ValueSFDynasty = %d, want 3000 (mid variant, not early/late/slot)", rows[0].ValueSFDynasty)
	}
}

func TestAggregatePicks_TradedPickMovesOwnership(t *testing.T) {
	league := &sleeper.League{Settings: map[string]int{"draft_rounds": 1}}
	rosters := []sleeper.Roster{{RosterID: 1, OwnerID: "u1"}, {RosterID: 2, OwnerID: "u2"}}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Team1"}, {UserID: "u2", DisplayName: "Team2"}}
	traded := []sleeper.TradedPick{
		{Season: "2027", Round: 1, RosterID: 1, PreviousOwnerID: 1, OwnerID: 2}, // team1's 2027 R1 now belongs to team2
	}

	rows := AggregatePicks(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), league, rosters, users, traded, testBundleWithPicks())

	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	team1Picks, team2Picks := 0, 0
	for _, r := range rows {
		switch r.TeamID {
		case "1":
			team1Picks++
		case "2":
			team2Picks++
		}
	}
	if team1Picks != 0 {
		t.Errorf("team1Picks = %d, want 0 (traded away)", team1Picks)
	}
	if team2Picks != 2 {
		t.Errorf("team2Picks = %d, want 2 (own + acquired)", team2Picks)
	}
}

func TestAggregatePicks_UnpricedRoundIsSkippedNotCrashed(t *testing.T) {
	league := &sleeper.League{Settings: map[string]int{"draft_rounds": 3}} // round 3 has no bundle entry at all
	rosters := []sleeper.Roster{{RosterID: 1, OwnerID: "u1"}}
	users := []sleeper.User{{UserID: "u1", DisplayName: "Team1"}}

	rows := AggregatePicks(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), league, rosters, users, nil, testBundleWithPicks())

	// Only rounds 1 and 2 are priced in the fixture; round 3 must be silently skipped.
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (round 3 unpriced, skipped)", len(rows))
	}
}

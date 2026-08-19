package dynasty

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

func TestTradeSide_RawSumsPricedAssetsOnly(t *testing.T) {
	s := TradeSide{Assets: []TradeAsset{
		{Name: "A", Value: 100, Priced: true},
		{Name: "B", Value: 9999, Priced: false},
		{Name: "C", Value: 50, Priced: true},
	}}
	if got := s.Raw(); got != 150 {
		t.Errorf("Raw = %d, want 150", got)
	}
	priced, total := s.Coverage()
	if priced != 2 || total != 3 {
		t.Errorf("Coverage = %d/%d, want 2/3", priced, total)
	}
}

func TestGradeTrade_FullyPricedTwoSided_Favors(t *testing.T) {
	sides := []TradeSide{
		{TeamID: "1", TeamName: "A", Assets: []TradeAsset{{Name: "p1", Value: 5000, Priced: true}}},
		{TeamID: "2", TeamName: "B", Assets: []TradeAsset{{Name: "p2", Value: 1000, Priced: true}}},
	}
	v := GradeTrade(sides)
	if v.Status != TradeFavors {
		t.Fatalf("Status = %q, want %q", v.Status, TradeFavors)
	}
	if v.FavoredTeamID != "1" {
		t.Errorf("FavoredTeamID = %q, want 1", v.FavoredTeamID)
	}
	// symmetric pct diff: |5000-1000| / mean(5000,1000) = 4000/3000 = 133.33%
	if v.Pct < 133 || v.Pct > 134 {
		t.Errorf("Pct = %.2f, want ~133.33", v.Pct)
	}
}

func TestGradeTrade_UnpricedAsset_Incomplete(t *testing.T) {
	sides := []TradeSide{
		{TeamID: "1", Assets: []TradeAsset{{Name: "p1", Value: 5000, Priced: true}}},
		{TeamID: "2", Assets: []TradeAsset{{Name: "unresolved pick", Priced: false}}},
	}
	v := GradeTrade(sides)
	if v.Status != TradeIncomplete {
		t.Fatalf("Status = %q, want %q", v.Status, TradeIncomplete)
	}
	if v.UnpricedAssets != 1 {
		t.Errorf("UnpricedAssets = %d, want 1", v.UnpricedAssets)
	}
	if v.FavoredTeamID != "" {
		t.Errorf("FavoredTeamID = %q, want empty on incomplete verdict", v.FavoredTeamID)
	}
}

// A trade whose every side carries zero assets (e.g. every asset was an
// unmodeled kind, or the transaction genuinely moved nothing this system
// tracks) must not fall through to a spurious 0-0 "favors" tie -- that is
// the live-league bug this pins: a 3-way trade with no players or picks
// modeled read "favors Tha Mobb (+0%)" instead of admitting it had nothing
// to compare.
func TestGradeTrade_AllSidesEmpty_Incomplete(t *testing.T) {
	sides := []TradeSide{{TeamID: "1"}, {TeamID: "2"}, {TeamID: "3"}}
	v := GradeTrade(sides)
	if v.Status != TradeIncomplete {
		t.Fatalf("Status = %q, want %q (0-0 must not declare a winner)", v.Status, TradeIncomplete)
	}
	if v.FavoredTeamID != "" {
		t.Errorf("FavoredTeamID = %q, want empty", v.FavoredTeamID)
	}
}

func TestGradeTrade_FewerThanTwoSides_Incomplete(t *testing.T) {
	for _, sides := range [][]TradeSide{nil, {{TeamID: "1"}}} {
		if v := GradeTrade(sides); v.Status != TradeIncomplete {
			t.Errorf("GradeTrade(%d sides) = %q, want %q", len(sides), v.Status, TradeIncomplete)
		}
	}
}

func TestGradeTrade_TiedSides_StableTiebreak(t *testing.T) {
	sides := []TradeSide{
		{TeamID: "2", Assets: []TradeAsset{{Value: 500, Priced: true}}},
		{TeamID: "1", Assets: []TradeAsset{{Value: 500, Priced: true}}},
	}
	first := GradeTrade(sides)
	for i := 0; i < 10; i++ {
		if got := GradeTrade(sides); got != first {
			t.Fatalf("verdict not stable: %+v then %+v", first, got)
		}
	}
	if first.FavoredTeamID != "1" {
		t.Errorf("tie broke to %q, want 1 (lexical on team id)", first.FavoredTeamID)
	}
}

func TestBuildTradeSides_GroupsPlayersAndPicksByReceivingRoster(t *testing.T) {
	txn := sleeper.Transaction{
		TransactionID: "t1",
		Type:          "trade",
		Status:        "complete",
		RosterIDs:     []int{1, 2},
		Adds:          map[string]int{"4984": 1, "9509": 2},
		DraftPicks: []sleeper.TransactionDraftPick{
			{Season: "2027", Round: 1, RosterID: 1, PreviousOwnerID: 1, OwnerID: 2},
		},
	}
	players := map[string]sleeper.Player{
		"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen"},
		"9509": {PlayerID: "9509", FirstName: "Bijan", LastName: "Robinson"},
	}
	bundle := &statsguy.Bundle{
		Players: map[string]statsguy.Player{
			"4984": {ID: "4984", Value: statsguy.FormatValues{SFDynasty: 9480}},
			"9509": {ID: "9509", Value: statsguy.FormatValues{SFDynasty: 10000}},
		},
		Picks: []statsguy.Pick{
			{Year: 2027, Round: 1, Variant: "mid", Value: statsguy.FormatValues{SFDynasty: 3000}},
		},
	}
	names := map[int]string{1: "Team1", 2: "Team2"}

	sides := BuildTradeSides(txn, players, bundle, names, "sf_dynasty")

	if len(sides) != 2 {
		t.Fatalf("len(sides) = %d, want 2", len(sides))
	}
	var team1, team2 *TradeSide
	for i := range sides {
		switch sides[i].TeamID {
		case "1":
			team1 = &sides[i]
		case "2":
			team2 = &sides[i]
		}
	}
	if team1 == nil || team2 == nil {
		t.Fatalf("sides = %+v, missing expected teams", sides)
	}
	// Adds: "4984" (Josh Allen) -> roster 1, "9509" (Bijan Robinson) -> roster 2.
	if len(team1.Assets) != 1 || team1.Assets[0].Value != 9480 {
		t.Errorf("team1.Assets = %+v, want [Josh Allen 9480]", team1.Assets)
	}
	// Team2 received Bijan Robinson (9509) + the 2027 R1 pick (originally team1's).
	if len(team2.Assets) != 2 {
		t.Fatalf("team2.Assets = %+v, want 2 assets (player + pick)", team2.Assets)
	}
	var gotPlayer, gotPick bool
	for _, a := range team2.Assets {
		if a.Value == 10000 {
			gotPlayer = true
		}
		if a.Value == 3000 {
			gotPick = true
		}
	}
	if !gotPlayer || !gotPick {
		t.Errorf("team2.Assets = %+v, want Bijan Robinson (10000) + priced pick (3000)", team2.Assets)
	}
}

func TestBuildTradeSides_FAABIsAnUnpricedAsset(t *testing.T) {
	// StatsGuy prices players and picks, not cash -- a FAAB-only or
	// FAAB-plus trade must not silently vanish (the real live-league bug
	// that motivated this: a trade with an empty side, because FAAB was the
	// unmodeled other half).
	txn := sleeper.Transaction{
		RosterIDs:    []int{1, 2},
		WaiverBudget: []sleeper.WaiverBudgetTransfer{{Sender: 2, Receiver: 1, Amount: 15}},
	}
	sides := BuildTradeSides(txn, nil, &statsguy.Bundle{}, map[int]string{1: "A", 2: "B"}, "sf_dynasty")

	var team1 *TradeSide
	for i := range sides {
		if sides[i].TeamID == "1" {
			team1 = &sides[i]
		}
	}
	if team1 == nil || len(team1.Assets) != 1 || team1.Assets[0].Priced {
		t.Fatalf("team1 = %+v, want one unpriced FAAB asset", team1)
	}
}

func TestBuildTradeSides_UnpricedPickIsUnpricedAsset(t *testing.T) {
	txn := sleeper.Transaction{
		RosterIDs: []int{1, 2},
		DraftPicks: []sleeper.TransactionDraftPick{
			{Season: "2027", Round: 99, RosterID: 1, OwnerID: 2}, // round 99: no bundle entry
		},
	}
	bundle := &statsguy.Bundle{Picks: []statsguy.Pick{
		{Year: 2027, Round: 1, Variant: "mid", Value: statsguy.FormatValues{SFDynasty: 3000}},
	}}
	sides := BuildTradeSides(txn, nil, bundle, map[int]string{1: "A", 2: "B"}, "sf_dynasty")

	var team2 *TradeSide
	for i := range sides {
		if sides[i].TeamID == "2" {
			team2 = &sides[i]
		}
	}
	if team2 == nil || len(team2.Assets) != 1 || team2.Assets[0].Priced {
		t.Fatalf("team2 = %+v, want one unpriced asset", team2)
	}
}

// Every asset priced, every side worth zero -- which is the ORDINARY case for a
// pick-for-pick trade in either redraft format, since a future draft pick has no
// redraft value. Declaring a winner here is the same non-verdict as the
// all-sides-empty branch, and it only became reachable when the trade log
// started grading all four formats rather than the alert's one.
func TestGradeTrade_AllSidesWorthZero_IsNotAVerdict(t *testing.T) {
	sides := []TradeSide{
		{TeamID: "4", TeamName: "BigZ4", Assets: []TradeAsset{{Name: "2027 Round 3 Pick", Value: 0, Priced: true}}},
		{TeamID: "6", TeamName: "CeeDee Top", Assets: []TradeAsset{{Name: "Seth McGowan", Value: 0, Priced: true}}},
	}
	v := GradeTrade(sides)
	if v.Status != TradeIncomplete {
		t.Fatalf("Status = %q, want %q (0-vs-0 must not crown a winner at +0%%)", v.Status, TradeIncomplete)
	}
	if v.FavoredTeamID != "" {
		t.Errorf("FavoredTeamID = %q, want empty", v.FavoredTeamID)
	}
	// Nothing failed to price, so the view must not blame an unpriced asset.
	if v.UnpricedAssets != 0 {
		t.Errorf("UnpricedAssets = %d, want 0 — this is 'no value in this format', not 'could not be priced'", v.UnpricedAssets)
	}
}

// One side at zero against a side with value is still a real, if lopsided,
// verdict — a genuine giveaway, not an absence of data.
func TestGradeTrade_OneSideWorthZero_StillFavorsTheOther(t *testing.T) {
	sides := []TradeSide{
		{TeamID: "1", TeamName: "A", Assets: []TradeAsset{{Value: 500, Priced: true}}},
		{TeamID: "2", TeamName: "B", Assets: []TradeAsset{{Value: 0, Priced: true}}},
	}
	v := GradeTrade(sides)
	if v.Status != TradeFavors || v.FavoredTeamID != "1" {
		t.Fatalf("verdict = %+v, want favors team 1", v)
	}
	if v.Pct < 199 || v.Pct > 201 {
		t.Errorf("Pct = %.2f, want ~200 (symmetric diff of 500 vs 0)", v.Pct)
	}
}

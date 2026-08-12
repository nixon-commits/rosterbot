package dynasty

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestStarterSlotCountExcludesBenchTaxiIR(t *testing.T) {
	positions := []string{"QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "SUPER_FLEX", "BN", "BN", "BN", "BN", "BN", "BN", "TAXI", "TAXI", "IR"}
	if got, want := StarterSlotCount(positions), 8; got != want {
		t.Errorf("StarterSlotCount = %d, want %d", got, want)
	}
}

func TestSelectStartersUsesRosterStartersWhenPresent(t *testing.T) {
	roster := sleeper.Roster{Starters: []string{"4984", "9509", "0", "1234"}, Players: []string{"4984", "9509", "1234", "5555"}}
	got := SelectStarters(roster, 4, nil)
	want := map[string]bool{"4984": true, "9509": true, "1234": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("missing expected starter %q in %v", id, got)
		}
	}
	// "0" is Sleeper's empty-slot placeholder, must never appear as a starter.
	if got["0"] {
		t.Error("got contains placeholder \"0\" as a starter")
	}
}

func TestSelectStartersFallsBackToSearchRankWhenStartersEmpty(t *testing.T) {
	roster := sleeper.Roster{Starters: []string{"0", "0"}, Players: []string{"a", "b", "c", "d"}}
	players := map[string]sleeper.Player{
		"a": {PlayerID: "a", SearchRank: 300},
		"b": {PlayerID: "b", SearchRank: 10},
		"c": {PlayerID: "c", SearchRank: 9999999}, // undrafted sentinel
		"d": {PlayerID: "d", SearchRank: 50},
	}
	got := SelectStarters(roster, 2, players)
	want := map[string]bool{"b": true, "d": true} // two lowest SearchRank
	if len(got) != 2 {
		t.Fatalf("got %v, want exactly 2 starters", got)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("expected %q selected by lowest search_rank, got %v", id, got)
		}
	}
}

package cmd

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestLeagueContexts_SkipsCompleteAndSortsByName(t *testing.T) {
	in := []sleeper.League{
		{LeagueID: "3", Name: "Zeta", Status: "in_season", Settings: map[string]int{"type": 0}},
		{LeagueID: "1", Name: "Done", Status: "complete", Settings: map[string]int{"type": 2}},
		{LeagueID: "2", Name: "Alpha", Status: "pre_draft", Settings: map[string]int{"type": 2}, RosterPositions: []string{"SUPER_FLEX"}},
	}
	got := leagueContexts(in, nil)
	if len(got) != 2 || got[0].League.Name != "Alpha" || got[1].League.Name != "Zeta" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Profile.Kind != dynasty.KindDynasty || !got[0].Profile.Superflex {
		t.Errorf("profile not derived: %+v", got[0].Profile)
	}
}

func TestLeagueContexts_AppliesOverrides(t *testing.T) {
	in := []sleeper.League{{LeagueID: "9", Name: "K", Status: "in_season", Settings: map[string]int{"type": 1}}}
	got := leagueContexts(in, map[string]string{"9": "non_sf_redraft"})
	if got[0].Profile.Format != "non_sf_redraft" || got[0].Profile.FormatSource != "override" {
		t.Errorf("override not applied: %+v", got[0].Profile)
	}
}

package dynasty

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// The six leagues the operator was measured in on 2026-09-21. settings.type is
// undocumented; these are the values Sleeper returned for each.
func liveLeagueFixtures() []sleeper.League {
	sf := []string{"QB", "RB", "RB", "WR", "WR", "WR", "TE", "FLEX", "FLEX", "SUPER_FLEX", "BN"}
	std := []string{"QB", "RB", "RB", "WR", "WR", "TE", "FLEX", "K", "DEF", "BN"}
	return []sleeper.League{
		{LeagueID: "1312135439800356864", Name: "Palm Trees & Promethazine", Status: "in_season", RosterPositions: sf, Settings: map[string]int{"type": 2}},
		{LeagueID: "1312113686978007040", Name: "Mad Lux Euphoria Utopia League", Status: "in_season", RosterPositions: sf, Settings: map[string]int{"type": 2}},
		{LeagueID: "1312135540388171776", Name: "Camp Roberts FFL", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 1}},
		{LeagueID: "1388008384137007104", Name: "Mad Luxurious 2.0", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 0}},
		{LeagueID: "1312176751115259904", Name: "No Punt Intended", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 0}},
		{LeagueID: "1389013496645058560", Name: "Mad Lux Chopped 6.9", Status: "in_season", RosterPositions: std, Settings: map[string]int{"type": 3}},
	}
}

func TestDeriveProfile_LiveLeagues(t *testing.T) {
	want := map[string]struct {
		kind      LeagueKind
		superflex bool
		format    string
	}{
		"1312135439800356864": {KindDynasty, true, "sf_dynasty"},
		"1312113686978007040": {KindDynasty, true, "sf_dynasty"},
		"1312135540388171776": {KindKeeper, false, "non_sf_redraft"}, // operator's call — see formatFor
		"1388008384137007104": {KindRedraft, false, "non_sf_redraft"},
		"1312176751115259904": {KindRedraft, false, "non_sf_redraft"},
		"1389013496645058560": {KindGuillotine, false, "non_sf_redraft"},
	}
	for _, lg := range liveLeagueFixtures() {
		p := DeriveProfile(lg, nil)
		w := want[lg.LeagueID]
		if p.Kind != w.kind || p.Superflex != w.superflex || p.Format != w.format {
			t.Errorf("%s: got kind=%s sf=%v format=%s, want kind=%s sf=%v format=%s",
				lg.Name, p.Kind, p.Superflex, p.Format, w.kind, w.superflex, w.format)
		}
		if p.FormatSource != "derived" {
			t.Errorf("%s: FormatSource = %q, want derived", lg.Name, p.FormatSource)
		}
		if p.LeagueID != lg.LeagueID || p.Name != lg.Name {
			t.Errorf("%s: identity not carried", lg.Name)
		}
	}
}

func TestDeriveProfile_OverrideWinsAndIsLabelled(t *testing.T) {
	lg := liveLeagueFixtures()[2] // keeper
	p := DeriveProfile(lg, map[string]string{lg.LeagueID: "non_sf_redraft"})
	if p.Format != "non_sf_redraft" || p.FormatSource != "override" {
		t.Errorf("got %s/%s, want non_sf_redraft/override", p.Format, p.FormatSource)
	}
	if p.Kind != KindKeeper {
		t.Errorf("override must not rewrite Kind; got %s", p.Kind)
	}
}

func TestDeriveProfile_UnknownTypeIsNamedNotGuessed(t *testing.T) {
	lg := sleeper.League{LeagueID: "x", Name: "X", RosterPositions: []string{"QB", "SUPER_FLEX"}, Settings: map[string]int{"type": 9}}
	if p := DeriveProfile(lg, nil); p.Kind != KindUnknown {
		t.Errorf("type 9 → Kind = %s, want unknown", p.Kind)
	}
	// A league with no settings at all is also unknown, not redraft-by-zero-value.
	lg.Settings = nil
	if p := DeriveProfile(lg, nil); p.Kind != KindUnknown {
		t.Errorf("no settings → Kind = %s, want unknown", p.Kind)
	}
}

func TestParseFormatOverrides(t *testing.T) {
	got, err := ParseFormatOverrides(" 1=sf_dynasty, 2=non_sf_redraft ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "sf_dynasty" || got["2"] != "non_sf_redraft" || len(got) != 2 {
		t.Errorf("got %v", got)
	}
	if got, err := ParseFormatOverrides(""); err != nil || len(got) != 0 {
		t.Errorf("empty: got %v, %v", got, err)
	}
	for _, bad := range []string{"1", "1=", "=sf_dynasty", "1=bogus", "1=sf_dynasty,1=sf_redraft"} {
		if _, err := ParseFormatOverrides(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

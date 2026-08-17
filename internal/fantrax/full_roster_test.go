package fantrax

import (
	"testing"

	"github.com/pmurley/go-fantrax/models"

	"github.com/nixon-commits/rosterbot/internal/positions"
)

// A pitcher stranded in an IL slot is the whole point of the roster-alert scan,
// and the hitters-only filter meant CheckRoster could never see one.
func TestCollectFullRoster_IncludesPitchers(t *testing.T) {
	roster := &models.TeamRoster{
		ActiveRoster: []models.RosterPlayer{
			{PlayerID: "h1", Name: "Bobby Witt Jr.", Positions: []string{positions.SS}, Status: "Active"},
		},
		InjuredReserve: []models.RosterPlayer{
			{PlayerID: "p1", Name: "Jacob deGrom", Positions: []string{positions.SP}, Status: "Injured Reserve"},
		},
	}

	players := collectFullRoster(roster)

	if len(players) != 2 {
		t.Fatalf("expected 2 players (1 hitter + 1 pitcher), got %d", len(players))
	}
	var foundPitcher bool
	for _, p := range players {
		if p.ID == "p1" {
			foundPitcher = true
		}
	}
	if !foundPitcher {
		t.Error("IL pitcher p1 missing from the full roster")
	}
}

// All four roster sections must be represented; the IL and Minors sections are
// where every existing alert type looks.
func TestCollectFullRoster_CoversEverySection(t *testing.T) {
	roster := &models.TeamRoster{
		ActiveRoster:   []models.RosterPlayer{{PlayerID: "a", Positions: []string{positions.SS}}},
		ReserveRoster:  []models.RosterPlayer{{PlayerID: "r", Positions: []string{positions.SP}}},
		InjuredReserve: []models.RosterPlayer{{PlayerID: "i", Positions: []string{positions.RP}}},
		MinorsRoster:   []models.RosterPlayer{{PlayerID: "m", Positions: []string{positions.OF}}},
	}

	players := collectFullRoster(roster)

	got := make(map[string]bool, len(players))
	for _, p := range players {
		got[p.ID] = true
	}
	for _, want := range []string{"a", "r", "i", "m"} {
		if !got[want] {
			t.Errorf("player %q missing from the full roster", want)
		}
	}
}

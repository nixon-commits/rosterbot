package claims

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

func TestLookupHKB_MatchesNormalizedName(t *testing.T) {
	players := []hkb.Player{
		{Name: "Bobby Witt Jr.", Value: 9000, Rank: 1, ValueChange30Days: 50, Level: "MLB",
			HitterStats: &hkb.HitterStats{OPS: 0.910}},
	}
	lookup := buildHKBLookup(players)

	sp := lookupHKB("Bobby Witt Jr", "SS", lookup)
	if !sp.Ranked {
		t.Fatal("expected ranked match")
	}
	if sp.Value != 9000 || sp.Rank != 1 || sp.Trend30D != 50 {
		t.Errorf("unexpected HKB fields: %+v", sp)
	}
	if sp.IsPitcher || !sp.HasStats || sp.OPS != 0.910 {
		t.Errorf("expected hitter stats, got %+v", sp)
	}

	miss := lookupHKB("Nobody Here", "OF", lookup)
	if miss.Ranked {
		t.Error("expected unranked for unknown player")
	}
	if miss.Name != "Nobody Here" || miss.Position != "OF" {
		t.Errorf("unranked player should keep name/pos: %+v", miss)
	}
}

func TestLookupHKB_UnrankedPitcherInferredFromPosition(t *testing.T) {
	lookup := buildHKBLookup(nil)
	sp := lookupHKB("Some Reliever", "RP", lookup)
	if sp.Ranked {
		t.Fatal("expected unranked")
	}
	if !sp.IsPitcher {
		t.Error("unranked pitcher should be inferred as pitcher from position 'RP'")
	}
}

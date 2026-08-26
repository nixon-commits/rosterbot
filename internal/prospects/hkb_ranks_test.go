package prospects

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// TestHKBRanks_DeclinesContestedNames pins the fix for the last-write-wins
// collision this map used to share with the pre-rosterbot-5z7 HKB join — the
// bug class behind the Team Value Store's Namesake Re-baseline. Two prospects
// collapsing to one normalized key must yield NO entry: a miss renders as
// unranked, while a wrong match renders as a confident number and counts as
// coverage.
func TestHKBRanks_DeclinesContestedNames(t *testing.T) {
	m := hkbRanks([]hkb.Player{
		{Name: "Luis Garcia", AssetType: "PLAYER", Level: "AAA", Rank: 40},
		{Name: "Luis García", AssetType: "PLAYER", Level: "AA", Rank: 90}, // same key after normalization
		{Name: "Solo Prospect", AssetType: "PLAYER", Level: "AA", Rank: 12},
	})

	if rank, ok := m[projections.NormalizeName("Luis Garcia")]; ok {
		t.Fatalf("contested key resolved to rank %d — must be declined, whichever row scraped last", rank)
	}
	if got := m[projections.NormalizeName("Solo Prospect")]; got != 12 {
		t.Fatalf("uncontested prospect rank = %d, want 12", got)
	}
}

// TestHKBRanks_MLBNamesakeDoesNotContestAProspect pins the behavior that makes
// the Level filter load-bearing: an MLB veteran sharing a prospect's
// normalized name is filtered out BEFORE collision detection, so the prospect
// still resolves. Building the lookup over the full list instead would turn
// this correct answer into a miss — the regression this test exists to block.
func TestHKBRanks_MLBNamesakeDoesNotContestAProspect(t *testing.T) {
	m := hkbRanks([]hkb.Player{
		{Name: "Luis García Jr.", AssetType: "PLAYER", Level: "MLB", Rank: 3},
		{Name: "Luis Garcia Jr", AssetType: "PLAYER", Level: "A", Rank: 55},
	})

	if got := m[projections.NormalizeName("Luis Garcia Jr")]; got != 55 {
		t.Fatalf("prospect rank = %d, want 55 — the MLB namesake must not contest a filtered-set key", got)
	}
}

// TestHKBRanks_FilterStillHolds pins the pre-existing inclusion rules: only
// PLAYER assets, only non-MLB levels, only ranked rows.
func TestHKBRanks_FilterStillHolds(t *testing.T) {
	m := hkbRanks([]hkb.Player{
		{Name: "Big Leaguer", AssetType: "PLAYER", Level: "MLB", Rank: 1},
		{Name: "Unranked Kid", AssetType: "PLAYER", Level: "AA", Rank: 0},
		{Name: "Draft Pick", AssetType: "PICK", Level: "AA", Rank: 7},
	})
	if len(m) != 0 {
		t.Fatalf("filter admitted %v, want an empty map", m)
	}
}

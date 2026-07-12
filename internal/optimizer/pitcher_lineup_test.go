package optimizer

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/pmurley/go-fantrax/auth_client"
)

// stubPitcherSource is a test projection source.
type stubPitcherSource struct {
	data map[string]*projections.PitcherProjection
}

func (s *stubPitcherSource) GetPitcherProjection(name, _ string) (*projections.PitcherProjection, bool) {
	p, ok := s.data[projections.NormalizeName(name)]
	return p, ok
}

func makeSlots(names ...string) []fantrax.Slot {
	nameToID := map[string]string{
		"SP": auth_client.PosSP, "RP": auth_client.PosRP, "P": auth_client.PosP,
	}
	var slots []fantrax.Slot
	for _, n := range names {
		slots = append(slots, fantrax.Slot{PosID: nameToID[n], PosName: n})
	}
	return slots
}

func TestOptimizePitcherLineup_NonStartingSPFillsEmptySlot(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Ace Pitcher", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true}
	probables := map[string]string{"someone else": "NYY"} // different pitcher is probable
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"ace pitcher": {G: 30, GS: 30, IP: 180, K: 200, W: 15},
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0}
	slots := makeSlots("P")

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	// Non-starting SP whose team plays should fill an empty slot (at reduced value).
	if len(result.ToActivate) != 1 {
		t.Errorf("expected 1 activation (non-starting SP fills empty slot), got %d", len(result.ToActivate))
	}
	for _, sp := range result.Scored {
		if sp.Player.ID == "p1" && sp.IsStarter {
			t.Error("non-probable SP should not be marked as starter")
		}
	}
}

func TestOptimizePitcherLineup_RPPreferredOverNonStartingSP(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Ace SP", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Reserve"},
		{ID: "r1", Name: "Closer", MLBTeam: "BOS", Positions: []string{auth_client.PosRP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true, "BOS": true}
	probables := map[string]string{"someone else": "NYY"} // Ace SP is not starting
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"ace sp": {G: 30, GS: 30, IP: 180, K: 200, W: 15}, // high value but not starting
		"closer": {G: 60, IP: 65, K: 70, SV: 30},          // lower value but guaranteed to pitch
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0, "SV": 5.0}
	slots := makeSlots("P") // only 1 slot

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.ToActivate))
	}
	// RP should be preferred over non-starting SP because SP's value is discounted.
	if result.ToActivate[0].PlayerID != "r1" {
		t.Errorf("expected RP (r1) preferred over non-starting SP, got %s", result.ToActivate[0].PlayerID)
	}
}

func TestOptimizePitcherLineup_SPStartedWhenProbable(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Ace Pitcher", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true}
	probables := map[string]string{"ace pitcher": "NYY"}
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"ace pitcher": {G: 30, GS: 30, IP: 180, K: 200, W: 15},
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0}
	slots := makeSlots("P")

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation (SP is probable), got %d", len(result.ToActivate))
	}
	if result.ToActivate[0].PlayerID != "p1" {
		t.Errorf("expected p1 activated, got %s", result.ToActivate[0].PlayerID)
	}
}

func TestOptimizePitcherLineup_RPStartedWhenTeamPlays(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "r1", Name: "Setup Man", MLBTeam: "BOS", Positions: []string{auth_client.PosRP}, Status: "Reserve"},
	}
	playing := map[string]bool{"BOS": true}
	probables := map[string]string{} // no probable data needed for RPs
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"setup man": {G: 60, IP: 65, K: 70, SV: 0, HLD: 20},
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "HLD": 3.0, "IP": 1.0}
	slots := makeSlots("RP")

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation (RP team plays), got %d", len(result.ToActivate))
	}
	if result.ToActivate[0].PosID != auth_client.PosRP {
		t.Errorf("expected RP slot, got %s", result.ToActivate[0].PosID)
	}
}

func TestOptimizePitcherLineup_PSlotPicksBestAvailable(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Star SP", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Reserve"},
		{ID: "r1", Name: "Good RP", MLBTeam: "BOS", Positions: []string{auth_client.PosRP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true, "BOS": true}
	probables := map[string]string{"star sp": "NYY"}
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"star sp": {G: 30, GS: 30, IP: 180, K: 200, W: 15}, // high per-game value
		"good rp": {G: 60, IP: 65, K: 70, SV: 0, HLD: 20},  // lower per-game, discounted by 0.55
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0, "HLD": 3.0}
	slots := makeSlots("P") // only 1 P slot

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.ToActivate))
	}
	// SP on start day should beat discounted RP.
	if result.ToActivate[0].PlayerID != "p1" {
		t.Errorf("expected SP (p1) to be preferred in P slot, got %s", result.ToActivate[0].PlayerID)
	}
}

// TestOptimizePitcherLineup_NonStarterDiscountDoesNotOutrankModestRP is the
// regression test for rosterbot-8xd: at the old 0.10x discount, a
// high-projection non-starting SP's discounted value could exceed a real,
// modest RP's full value. At NonStarterSPDiscount (0.05x), a 20pt/G SP
// discounts to 1.0 — below this RP's 1.1pt/G — so the RP wins the slot.
func TestOptimizePitcherLineup_NonStarterDiscountDoesNotOutrankModestRP(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "sp1", Name: "Big Name SP", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Reserve"},
		{ID: "rp1", Name: "Modest RP", MLBTeam: "BOS", Positions: []string{auth_client.PosRP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true, "BOS": true}
	probables := map[string]string{"someone else": "NYY"} // Big Name SP is NOT starting today
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"big name sp": {G: 30, IP: 600}, // 20.0 pts/G at IP-only weight
		"modest rp":   {G: 60, IP: 66},  // 1.1 pts/G, full value (RPs aren't discounted)
	}}
	scoring := fantrax.ScoringWeights{"IP": 1.0}
	slots := makeSlots("P") // only 1 slot — forces direct competition

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.ToActivate))
	}
	if result.ToActivate[0].PlayerID != "rp1" {
		t.Errorf("expected modest RP (1.1pt/G) to beat a high-projection non-starting SP discounted to %.2fx (%.2f pts/G), got %s activated instead",
			NonStarterSPDiscount, 20.0*NonStarterSPDiscount, result.ToActivate[0].PlayerID)
	}
}

func TestOptimizePitcherLineup_NoProbableDataDefaultsToStart(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Ace Pitcher", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true}
	probables := map[string]string{} // empty = no data available (future date)
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"ace pitcher": {G: 30, GS: 30, IP: 180, K: 200, W: 15},
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0}
	slots := makeSlots("P")

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	// With no probable data, SPs should default to starting.
	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation (default start), got %d", len(result.ToActivate))
	}
}

func TestOptimizePitcherLineup_Idempotent(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Ace", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, Status: "Active", RosterPosition: auth_client.PosP},
		{ID: "r1", Name: "Setup", MLBTeam: "BOS", Positions: []string{auth_client.PosRP}, Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true, "BOS": true}
	probables := map[string]string{"ace": "NYY"}
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"ace":   {G: 30, GS: 30, IP: 180, K: 200, W: 15},
		"setup": {G: 60, IP: 65, K: 70, HLD: 20},
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0, "HLD": 3.0}
	slots := makeSlots("P")

	// First run.
	r1 := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, nil)

	// Second run with the result applied: Ace is already in P slot.
	if len(r1.ToActivate) == 0 && len(r1.ToBench) == 0 {
		// Already optimal — no changes.
		return
	}

	// Apply result to roster for second run.
	roster2 := make([]fantrax.Player, len(roster))
	copy(roster2, roster)
	assigned := make(map[string]string)
	for _, ps := range r1.ToActivate {
		assigned[ps.PlayerID] = ps.PosID
	}
	for i := range roster2 {
		if posID, ok := assigned[roster2[i].ID]; ok {
			roster2[i].Status = "Active"
			roster2[i].RosterPosition = posID
		}
	}
	for _, id := range r1.ToBench {
		for i := range roster2 {
			if roster2[i].ID == id {
				roster2[i].Status = "Reserve"
				roster2[i].RosterPosition = ""
			}
		}
	}

	r2 := OptimizePitcherLineup(roster2, playing, probables, src, scoring, slots, nil)
	if len(r2.ToActivate) != 0 || len(r2.ToBench) != 0 {
		t.Errorf("second run should produce no changes, got %d activations, %d benches",
			len(r2.ToActivate), len(r2.ToBench))
	}
}

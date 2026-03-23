package optimizer

import (
	"testing"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
)

// stubProjection implements projections.Source with fixed data.
type stubProjection struct {
	data map[string]*projections.Projection
}

func (s *stubProjection) GetProjection(name, _ string) (*projections.Projection, bool) {
	p, ok := s.data[name]
	return p, ok
}

func newStubProj(entries map[string]*projections.Projection) projections.Source {
	return &stubProjection{data: entries}
}

func TestOptimizeLineup_BasicRanking(t *testing.T) {
	scoring := fantrax.ScoringWeights{
		"H":   1.0,
		"HR":  4.0,
		"RBI": 1.0,
		"R":   1.0,
		"BB":  1.0,
		"SB":  2.0,
	}

	// Star hitter: ~0.05 HR/game, good all-around
	// Bench bat:   minimal stats
	proj := newStubProj(map[string]*projections.Projection{
		"Star Hitter": {PA: 600, H: 160, HR: 30, RBI: 90, R: 90, BB: 60, SB: 15},
		"Bench Bat":   {PA: 200, H: 40, HR: 5, RBI: 20, R: 20, BB: 10, SB: 1},
	})

	roster := []fantrax.Player{
		{ID: "p1", Name: "Star Hitter", MLBTeam: "NYY", Positions: []string{"1B", "OF"}, Status: "Active"},
		{ID: "p2", Name: "Bench Bat", MLBTeam: "BOS", Positions: []string{"1B"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{
		{PosID: "002", PosName: "1B"},
	}

	playingToday := map[string]bool{"NYY": true, "BOS": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.ToActivate))
	}
	if result.ToActivate[0].PlayerID != "p1" {
		t.Errorf("expected Star Hitter (p1) activated, got %s", result.ToActivate[0].PlayerID)
	}
}

func TestOptimizeLineup_NoGame_BenchesPlayer(t *testing.T) {
	scoring := fantrax.ScoringWeights{"H": 1.0, "HR": 4.0}

	proj := newStubProj(map[string]*projections.Projection{
		"Active No Game": {PA: 600, H: 180, HR: 35},
		"Reserve Has Game": {PA: 400, H: 100, HR: 10},
	})

	roster := []fantrax.Player{
		{ID: "p1", Name: "Active No Game", MLBTeam: "NYY", Positions: []string{"OF"}, Status: "Active"},
		{ID: "p2", Name: "Reserve Has Game", MLBTeam: "BOS", Positions: []string{"OF"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{
		{PosID: "012", PosName: "OF"},
	}

	// NYY not playing, BOS is
	playingToday := map[string]bool{"BOS": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots)

	// Reserve player with a game should be activated over better active player without a game.
	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.ToActivate))
	}
	if result.ToActivate[0].PlayerID != "p2" {
		t.Errorf("expected Reserve Has Game (p2) activated, got %s", result.ToActivate[0].PlayerID)
	}
	// Active player without game should be benched.
	if len(result.ToBench) != 1 || result.ToBench[0] != "p1" {
		t.Errorf("expected p1 benched, got %v", result.ToBench)
	}
}

func TestOptimizeLineup_PositionEligibility(t *testing.T) {
	scoring := fantrax.ScoringWeights{"H": 1.0, "HR": 4.0}

	proj := newStubProj(map[string]*projections.Projection{
		"Catcher":    {PA: 400, H: 90, HR: 15},
		"Outfielder": {PA: 550, H: 155, HR: 25},
	})

	roster := []fantrax.Player{
		{ID: "c1", Name: "Catcher", MLBTeam: "NYY", Positions: []string{"C"}, Status: "Active"},
		{ID: "of1", Name: "Outfielder", MLBTeam: "NYY", Positions: []string{"OF"}, Status: "Active"},
	}

	slots := []fantrax.Slot{
		{PosID: "001", PosName: "C"},
		{PosID: "012", PosName: "OF"},
	}

	playingToday := map[string]bool{"NYY": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots)

	// Both should be activated in their correct positions.
	if len(result.ToActivate) != 2 {
		t.Fatalf("expected 2 activations, got %d", len(result.ToActivate))
	}
	// Verify catcher goes to C slot, outfielder to OF slot.
	slotMap := make(map[string]string)
	for _, ps := range result.ToActivate {
		slotMap[ps.PlayerID] = ps.PosID
	}
	if slotMap["c1"] != "001" {
		t.Errorf("catcher should be in C slot (001), got %s", slotMap["c1"])
	}
	if slotMap["of1"] != "012" {
		t.Errorf("outfielder should be in OF slot (012), got %s", slotMap["of1"])
	}
}

func TestOptimizeLineup_UtilFillsAnyHitter(t *testing.T) {
	scoring := fantrax.ScoringWeights{"H": 1.0}

	proj := newStubProj(map[string]*projections.Projection{
		"Second Baseman": {PA: 500, H: 140},
	})

	roster := []fantrax.Player{
		{ID: "2b1", Name: "Second Baseman", MLBTeam: "CHC", Positions: []string{"2B"}, Status: "Reserve"},
	}

	// Only a UTIL slot — 2B should fill it.
	slots := []fantrax.Slot{
		{PosID: "014", PosName: "UTIL"},
	}

	playingToday := map[string]bool{"CHC": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots)

	if len(result.ToActivate) != 1 || result.ToActivate[0].PlayerID != "2b1" {
		t.Errorf("expected 2b1 in UTIL, got %+v", result.ToActivate)
	}
}

func TestExpectedPts_Calculation(t *testing.T) {
	proj := &projections.Projection{
		PA:  600, // 150 games equivalent
		H:   150,
		HR:  30,
		RBI: 90,
	}
	scoring := fantrax.ScoringWeights{
		"H":   1.0,
		"HR":  4.0,
		"RBI": 1.0,
	}

	pts := expectedPts(proj, scoring)

	// Per game: 1 H + 0.2 HR*4 + 0.6 RBI = 1 + 0.8 + 0.6 = 2.4
	if pts < 2.3 || pts > 2.5 {
		t.Errorf("expected ~2.4 pts/game, got %.4f", pts)
	}
}

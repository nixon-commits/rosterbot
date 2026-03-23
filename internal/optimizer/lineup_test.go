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
		"Star Hitter": {G: 150, PA: 600, H: 160, HR: 30, RBI: 90, R: 90, BB: 60, SB: 15},
		"Bench Bat":   {G: 50, PA: 200, H: 40, HR: 5, RBI: 20, R: 20, BB: 10, SB: 1},
	})

	roster := []fantrax.Player{
		{ID: "p1", Name: "Star Hitter", MLBTeam: "NYY", Positions: []string{"002", "012", "014"}, Status: "Active"},
		{ID: "p2", Name: "Bench Bat", MLBTeam: "BOS", Positions: []string{"002", "014"}, Status: "Reserve"},
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
		"Active No Game":   {G: 150, PA: 600, H: 180, HR: 35},
		"Reserve Has Game": {G: 100, PA: 400, H: 100, HR: 10},
	})

	roster := []fantrax.Player{
		{ID: "p1", Name: "Active No Game", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Active"},
		{ID: "p2", Name: "Reserve Has Game", MLBTeam: "BOS", Positions: []string{"012", "014"}, Status: "Reserve"},
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
		"Catcher":    {G: 100, PA: 400, H: 90, HR: 15},
		"Outfielder": {G: 138, PA: 550, H: 155, HR: 25},
	})

	roster := []fantrax.Player{
		{ID: "c1", Name: "Catcher", MLBTeam: "NYY", Positions: []string{"001", "014"}, Status: "Active"},
		{ID: "of1", Name: "Outfielder", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Active"},
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
		"Second Baseman": {G: 125, PA: 500, H: 140},
	})

	roster := []fantrax.Player{
		{ID: "2b1", Name: "Second Baseman", MLBTeam: "CHC", Positions: []string{"003", "014"}, Status: "Reserve"},
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

func TestOptimizeLineup_BacktrackingBeatsGreedy(t *testing.T) {
	// Greedy would assign PlayerA (6.0) to SS, leaving only PlayerC (5.0) for OF.
	// Optimal: PlayerA→OF, PlayerB→SS = 6.0 + 5.5 = 11.5 vs greedy's 6.0 + 5.0 = 11.0.
	scoring := fantrax.ScoringWeights{"HR": 1.0}

	proj := newStubProj(map[string]*projections.Projection{
		"PlayerA": {G: 100, PA: 400, HR: 600},  // 6.0 pts/g — eligible SS, OF
		"PlayerB": {G: 100, PA: 400, HR: 550},  // 5.5 pts/g — eligible SS only
		"PlayerC": {G: 100, PA: 400, HR: 500},  // 5.0 pts/g — eligible OF only
	})

	roster := []fantrax.Player{
		{ID: "a", Name: "PlayerA", MLBTeam: "NYY", Positions: []string{"005", "012", "014"}, Status: "Reserve"},
		{ID: "b", Name: "PlayerB", MLBTeam: "NYY", Positions: []string{"005", "014"}, Status: "Reserve"},
		{ID: "c", Name: "PlayerC", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{
		{PosID: "005", PosName: "SS"},
		{PosID: "012", PosName: "OF"},
	}

	playingToday := map[string]bool{"NYY": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots)

	slotMap := make(map[string]string) // playerID → posID
	for _, ps := range result.ToActivate {
		slotMap[ps.PlayerID] = ps.PosID
	}

	if slotMap["b"] != "005" {
		t.Errorf("expected PlayerB in SS (005), got %s", slotMap["b"])
	}
	if slotMap["a"] != "012" {
		t.Errorf("expected PlayerA in OF (012), got %s", slotMap["a"])
	}
}

// stubBlendedSource implements both Source and PtsPerGameSource.
type stubBlendedSource struct {
	projData map[string]*projections.Projection
	ptsData  map[string]float64
}

func (s *stubBlendedSource) GetProjection(name, _ string) (*projections.Projection, bool) {
	p, ok := s.projData[name]
	return p, ok
}

func (s *stubBlendedSource) GetPtsPerGame(name, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	pts, ok := s.ptsData[name]
	return pts, ok
}

func TestOptimizeLineup_UsesBlendedPtsPerGame(t *testing.T) {
	src := &stubBlendedSource{
		projData: map[string]*projections.Projection{
			"Player A": {G: 100, HR: 10}, // expectedPts with HR=4 would be 0.4
			"Player B": {G: 100, HR: 20}, // expectedPts with HR=4 would be 0.8
		},
		ptsData: map[string]float64{
			"Player A": 5.0, // blended overrides to 5.0
			"Player B": 3.0, // blended overrides to 3.0
		},
	}

	scoring := fantrax.ScoringWeights{"HR": 4.0}

	roster := []fantrax.Player{
		{ID: "a", Name: "Player A", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
		{ID: "b", Name: "Player B", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{{PosID: "012", PosName: "OF"}}
	playingToday := map[string]bool{"NYY": true}

	result := OptimizeLineup(roster, playingToday, src, scoring, slots)

	// Player A should be chosen (5.0 > 3.0) even though Player B has more HR in Steamer
	if len(result.Scored) < 2 {
		t.Fatalf("expected 2 scored players, got %d", len(result.Scored))
	}
	if result.Scored[0].Player.Name != "Player A" {
		t.Errorf("expected Player A ranked first, got %s", result.Scored[0].Player.Name)
	}
	if result.Scored[0].ExpectedPts != 5.0 {
		t.Errorf("expected 5.0 pts, got %.2f", result.Scored[0].ExpectedPts)
	}
}

func TestOptimizeLineup_ILPlayerNotActivated(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 4.0}

	proj := newStubProj(map[string]*projections.Projection{
		"IL Player":      {G: 150, PA: 600, HR: 40}, // best hitter but on IL
		"Healthy Player": {G: 100, PA: 400, HR: 10},
	})

	roster := []fantrax.Player{
		{ID: "il1", Name: "IL Player", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Injured Reserve"},
		{ID: "h1", Name: "Healthy Player", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{{PosID: "012", PosName: "OF"}}
	playingToday := map[string]bool{"NYY": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots)

	if len(result.ToActivate) != 1 || result.ToActivate[0].PlayerID != "h1" {
		t.Errorf("expected healthy player activated, got %+v", result.ToActivate)
	}
}

func TestExpectedPts_Calculation(t *testing.T) {
	// 150 games, 30 HR, 90 RBI, 120 singles
	proj := &projections.Projection{
		G:       150,
		PA:      600,
		H:       150,
		Singles: 120,
		HR:      30,
		RBI:     90,
	}
	scoring := fantrax.ScoringWeights{
		"1B":  1.0,
		"HR":  4.0,
		"RBI": 1.0,
	}

	pts := expectedPts(proj, scoring)

	// Per game: (120/150)*1 + (30/150)*4 + (90/150)*1 = 0.8 + 0.8 + 0.6 = 2.2
	if pts < 2.1 || pts > 2.3 {
		t.Errorf("expected ~2.2 pts/game, got %.4f", pts)
	}
}

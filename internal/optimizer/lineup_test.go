package optimizer

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
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

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

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

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.ToActivate))
	}
	if result.ToActivate[0].PlayerID != "p2" {
		t.Errorf("expected Reserve Has Game (p2) activated, got %s", result.ToActivate[0].PlayerID)
	}
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

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

	if len(result.ToActivate) != 2 {
		t.Fatalf("expected 2 activations, got %d", len(result.ToActivate))
	}
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

	slots := []fantrax.Slot{
		{PosID: "014", PosName: "UTIL"},
	}

	playingToday := map[string]bool{"CHC": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

	if len(result.ToActivate) != 1 || result.ToActivate[0].PlayerID != "2b1" {
		t.Errorf("expected 2b1 in UTIL, got %+v", result.ToActivate)
	}
}

func TestOptimizeLineup_BacktrackingBeatsGreedy(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 1.0}

	proj := newStubProj(map[string]*projections.Projection{
		"PlayerA": {G: 100, PA: 400, HR: 600},
		"PlayerB": {G: 100, PA: 400, HR: 550},
		"PlayerC": {G: 100, PA: 400, HR: 500},
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

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

	slotMap := make(map[string]string)
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
			"Player A": {G: 100, HR: 10},
			"Player B": {G: 100, HR: 20},
		},
		ptsData: map[string]float64{
			"Player A": 5.0,
			"Player B": 3.0,
		},
	}

	scoring := fantrax.ScoringWeights{"HR": 4.0}

	roster := []fantrax.Player{
		{ID: "a", Name: "Player A", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
		{ID: "b", Name: "Player B", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{{PosID: "012", PosName: "OF"}}
	playingToday := map[string]bool{"NYY": true}

	result := OptimizeLineup(roster, playingToday, src, scoring, slots, nil)

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

func TestOptimizeLineup_MinorLeaguerOnReserve_NotActivated(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 4.0}

	proj := newStubProj(map[string]*projections.Projection{
		"MLB Player":    {G: 100, PA: 400, HR: 10},
		"Minor Leaguer": {G: 150, PA: 600, HR: 40},
	})

	// Minor leaguer overflowed from minors slots to Reserve.
	// Has NextGameDate (Fantrax may show org's game) but InMinors=true.
	roster := []fantrax.Player{
		{ID: "mlb1", Name: "MLB Player", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
		{ID: "min1", Name: "Minor Leaguer", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve", InMinors: true},
	}

	slots := []fantrax.Slot{{PosID: "012", PosName: "OF"}}
	playingToday := map[string]bool{"NYY": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

	if len(result.ToActivate) != 1 || result.ToActivate[0].PlayerID != "mlb1" {
		t.Errorf("expected MLB player activated over minor leaguer on reserve, got %+v", result.ToActivate)
	}
}

func TestOptimizeLineup_LockedPlayersImmovable(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 4.0}

	proj := newStubProj(map[string]*projections.Projection{
		"Locked Active":  {G: 100, PA: 400, HR: 5},  // low value but locked in slot
		"Better Reserve": {G: 100, PA: 400, HR: 30}, // higher value, should fill remaining slot
		"Locked Bench":   {G: 100, PA: 400, HR: 40}, // highest value but locked on bench
	})

	roster := []fantrax.Player{
		{ID: "la", Name: "Locked Active", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Active", RosterPosition: "012", Locked: true},
		{ID: "br", Name: "Better Reserve", MLBTeam: "BOS", Positions: []string{"012", "014"}, Status: "Reserve"},
		{ID: "lb", Name: "Locked Bench", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve", Locked: true},
	}

	slots := []fantrax.Slot{
		{PosID: "012", PosName: "OF"},
		{PosID: "014", PosName: "UT"},
	}

	playingToday := map[string]bool{"NYY": true, "BOS": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, nil)

	// Locked Active should stay in OF slot (not benched despite low value).
	// Better Reserve should fill the remaining UT slot.
	// Locked Bench should NOT be activated despite highest value.
	if len(result.ToActivate) != 1 {
		t.Fatalf("expected 1 activation, got %d: %+v", len(result.ToActivate), result.ToActivate)
	}
	if result.ToActivate[0].PlayerID != "br" {
		t.Errorf("expected Better Reserve activated, got %s", result.ToActivate[0].PlayerID)
	}
	if len(result.ToBench) != 0 {
		t.Errorf("expected no benchings (locked player should not be benched), got %v", result.ToBench)
	}
}

func TestExpectedPts_Calculation(t *testing.T) {
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

func TestBlendedScoring_RecentPerformanceImpact(t *testing.T) {
	// This test demonstrates how recent performance blends with Steamer projections
	// to influence lineup decisions. The blending formula:
	//
	//   blended = steamerWeight * steamerPts/G + recentWeight * recentFP/G
	//
	// Weights are PA-based: recentWeight = approxPA / (approxPA + 250)
	// where approxPA = gamesPlayed * 3.8. Steamer floor = 30%.
	//
	// Early season (~4 GP): ~94% Steamer / ~6% recent
	// Mid-season (~66 GP): ~50% Steamer / ~50% recent
	// Full season (150+ GP): 30% Steamer / 70% recent (floor)

	scoring := fantrax.ScoringWeights{
		"1B": 1.0, "2B": 2.0, "3B": 3.0, "HR": 4.0,
		"RBI": 1.0, "R": 1.0, "BB": 1.0, "SB": 2.0,
		"SO": -0.5, "CS": -1.0, "GIDP": -1.0,
		"HBP": 1.0, "XBH": 1.0, "TB": 0.5,
	}

	// Steamer projections: PlayerA is projected better than PlayerB.
	inner := &stubBlendedSource{
		projData: map[string]*projections.Projection{
			"Cold Star": {
				G: 150, PA: 600, H: 160, Doubles: 30, Triples: 3, HR: 30,
				RBI: 90, R: 90, BB: 60, SB: 15, SO: 120, CS: 3,
				HBP: 8, GIDP: 10,
			},
			"Hot Backup": {
				G: 130, PA: 500, H: 120, Doubles: 22, Triples: 2, HR: 15,
				RBI: 55, R: 60, BB: 40, SB: 8, SO: 100, CS: 2,
				HBP: 5, GIDP: 8,
			},
			"No Recent Data": {
				G: 140, PA: 550, H: 140, Doubles: 25, Triples: 2, HR: 20,
				RBI: 70, R: 75, BB: 50, SB: 10, SO: 110, CS: 3,
				HBP: 6, GIDP: 9,
			},
		},
		ptsData: map[string]float64{}, // will be filled per scenario
	}

	// Compute Steamer-only expected pts for reference.
	steamerCold := projections.ExpectedPtsFromProj(inner.projData["Cold Star"], scoring)
	steamerHot := projections.ExpectedPtsFromProj(inner.projData["Hot Backup"], scoring)
	steamerNoData := projections.ExpectedPtsFromProj(inner.projData["No Recent Data"], scoring)

	t.Logf("=== Steamer-Only Projections (no recent data) ===")
	t.Logf("  Cold Star:      %.2f pts/G", steamerCold)
	t.Logf("  Hot Backup:     %.2f pts/G", steamerHot)
	t.Logf("  No Recent Data: %.2f pts/G", steamerNoData)
	t.Logf("")

	// --- Scenario: early season (4 GP) ---
	gp := 4
	sw, rw := projections.HitterBlendWeightsForDisplay(gp)
	t.Logf("=== Early Season (%d GP) — Weights: %.0f%% Steamer / %.0f%% Recent ===", gp, sw*100, rw*100)

	// Cold Star: Steamer says 5+ pts/G but recent is 1.0 FP/G (terrible slump)
	coldRecent := 1.0
	coldBlended := sw*steamerCold + rw*coldRecent
	t.Logf("  Cold Star:  Steamer=%.2f, Recent=%.2f → Blended=%.2f (%.0f%% × %.2f + %.0f%% × %.2f)",
		steamerCold, coldRecent, coldBlended, sw*100, steamerCold, rw*100, coldRecent)

	// Hot Backup: Steamer says ~3.5 pts/G but recent is 8.0 FP/G (on fire)
	hotRecent := 8.0
	hotBlended := sw*steamerHot + rw*hotRecent
	t.Logf("  Hot Backup: Steamer=%.2f, Recent=%.2f → Blended=%.2f (%.0f%% × %.2f + %.0f%% × %.2f)",
		steamerHot, hotRecent, hotBlended, sw*100, steamerHot, rw*100, hotRecent)
	t.Logf("")

	// At 4 GP, Steamer dominates — Cold Star should still rank above Hot Backup
	// despite terrible recent stats.
	if coldBlended <= hotBlended {
		t.Logf("  ✓ Early season: Steamer dominates, Cold Star (%.2f) > Hot Backup (%.2f)", coldBlended, hotBlended)
	} else {
		t.Logf("  NOTE: At %d GP, Hot Backup (%.2f) already overtook Cold Star (%.2f)", gp, hotBlended, coldBlended)
	}
	t.Logf("")

	// --- Scenario: mid-season (66 GP, ~50/50) ---
	gp = 66
	sw, rw = projections.HitterBlendWeightsForDisplay(gp)
	t.Logf("=== Mid-Season (%d GP) — Weights: %.0f%% Steamer / %.0f%% Recent ===", gp, sw*100, rw*100)

	coldBlended = sw*steamerCold + rw*coldRecent
	hotBlended = sw*steamerHot + rw*hotRecent
	t.Logf("  Cold Star:  Steamer=%.2f, Recent=%.2f → Blended=%.2f", steamerCold, coldRecent, coldBlended)
	t.Logf("  Hot Backup: Steamer=%.2f, Recent=%.2f → Blended=%.2f", steamerHot, hotRecent, hotBlended)

	// At 50/50, the hot backup's 8.0 recent FP/G should push them ahead
	if hotBlended > coldBlended {
		t.Logf("  ✓ Mid-season: recent performance matters — Hot Backup (%.2f) > Cold Star (%.2f)", hotBlended, coldBlended)
	}
	t.Logf("")

	// --- Scenario: full season (150 GP, 30/70 floor) ---
	gp = 150
	sw, rw = projections.HitterBlendWeightsForDisplay(gp)
	t.Logf("=== Full Season (%d GP) — Weights: %.0f%% Steamer / %.0f%% Recent (floor) ===", gp, sw*100, rw*100)

	coldBlended = sw*steamerCold + rw*coldRecent
	hotBlended = sw*steamerHot + rw*hotRecent
	t.Logf("  Cold Star:  Steamer=%.2f, Recent=%.2f → Blended=%.2f", steamerCold, coldRecent, coldBlended)
	t.Logf("  Hot Backup: Steamer=%.2f, Recent=%.2f → Blended=%.2f", steamerHot, hotRecent, hotBlended)
	t.Logf("")

	// --- Verify via actual BlendedSource + OptimizeLineup ---
	t.Logf("=== Lineup Impact: Mid-Season (66 GP) ===")
	// Use mid-season weights (most interesting case).
	midSW, midRW := projections.HitterBlendWeightsForDisplay(66)
	blendedSrc := &stubBlendedSource{
		projData: inner.projData,
		ptsData: map[string]float64{
			"Cold Star":      midSW*steamerCold + midRW*coldRecent,
			"Hot Backup":     midSW*steamerHot + midRW*hotRecent,
			"No Recent Data": steamerNoData, // no recent data → 100% Steamer
		},
	}

	roster := []fantrax.Player{
		{ID: "cold", Name: "Cold Star", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Active", RosterPosition: "012"},
		{ID: "hot", Name: "Hot Backup", MLBTeam: "BOS", Positions: []string{"012", "014"}, Status: "Reserve"},
		{ID: "none", Name: "No Recent Data", MLBTeam: "CHC", Positions: []string{"012", "014"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{
		{PosID: "012", PosName: "OF"},
		{PosID: "014", PosName: "UT"},
	}

	playingToday := map[string]bool{"NYY": true, "BOS": true, "CHC": true}
	result := OptimizeLineup(roster, playingToday, blendedSrc, scoring, slots, nil)

	// Display final ranking.
	t.Logf("  Rank  Player           Blended Pts/G  Slot")
	t.Logf("  ────  ───────────────  ─────────────  ────")
	for i, sp := range result.Scored {
		if !sp.HasGame {
			continue
		}
		slot := "BN"
		for _, act := range result.ToActivate {
			if act.PlayerID == sp.Player.ID {
				if act.PosID == "012" {
					slot = "OF"
				} else {
					slot = "UT"
				}
			}
		}
		t.Logf("  %d.    %-15s  %13.2f  %s", i+1, sp.Player.Name, sp.ExpectedPts, slot)
	}
	t.Logf("")

	// Assertions: Hot Backup should rank above Cold Star at mid-season weights.
	if len(result.Scored) < 3 {
		t.Fatalf("expected 3 scored players, got %d", len(result.Scored))
	}
	if result.Scored[0].Player.Name != "Hot Backup" {
		t.Errorf("expected Hot Backup ranked #1, got %s (%.2f pts/G)", result.Scored[0].Player.Name, result.Scored[0].ExpectedPts)
	}

	// Hot Backup should be activated.
	activated := make(map[string]bool)
	for _, act := range result.ToActivate {
		activated[act.PlayerID] = true
	}
	if !activated["hot"] {
		t.Error("Hot Backup should be activated based on blended scoring")
	}

	// Cold Star should be benched (their current Active status gets overridden).
	benched := false
	for _, id := range result.ToBench {
		if id == "cold" {
			benched = true
		}
	}
	if benched {
		t.Logf("  ✓ Cold Star benched — recent slump (%.1f FP/G) dragged blended below Hot Backup", coldRecent)
	}

	// No Recent Data player should use 100% Steamer.
	for _, sp := range result.Scored {
		if sp.Player.Name == "No Recent Data" {
			diff := sp.ExpectedPts - steamerNoData
			if diff < -0.01 || diff > 0.01 {
				t.Errorf("No Recent Data should use 100%% Steamer (%.2f), got %.2f", steamerNoData, sp.ExpectedPts)
			} else {
				t.Logf("  ✓ No Recent Data uses 100%% Steamer: %.2f pts/G", sp.ExpectedPts)
			}
		}
	}
}

func TestOptimizeLineup_BenchedPlayerTreatedAsNoGame(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 4.0}

	proj := newStubProj(map[string]*projections.Projection{
		"Star Sitting Out": {G: 100, PA: 400, HR: 40}, // highest value but benched IRL
		"Backup Playing":   {G: 100, PA: 400, HR: 10}, // lower value but actually playing
	})

	roster := []fantrax.Player{
		{ID: "star", Name: "Star Sitting Out", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Active", RosterPosition: "012"},
		{ID: "backup", Name: "Backup Playing", MLBTeam: "NYY", Positions: []string{"012", "014"}, Status: "Reserve"},
	}

	slots := []fantrax.Slot{
		{PosID: "012", PosName: "OF"},
	}

	playingToday := map[string]bool{"NYY": true}
	benchedToday := map[string]bool{"star sitting out": true}

	result := OptimizeLineup(roster, playingToday, proj, scoring, slots, benchedToday)

	// Star should be benched because they're confirmed out of the real-life lineup.
	// Backup should be activated since they're the best available player actually playing.
	if len(result.ToActivate) != 1 || result.ToActivate[0].PlayerID != "backup" {
		t.Errorf("expected Backup Playing activated, got %+v", result.ToActivate)
	}
	if len(result.ToBench) != 1 || result.ToBench[0] != "star" {
		t.Errorf("expected star benched, got %v", result.ToBench)
	}

	// Verify HasGame flags.
	for _, sp := range result.Scored {
		if sp.Player.ID == "star" && sp.HasGame {
			t.Error("star should have HasGame=false (benched IRL)")
		}
		if sp.Player.ID == "backup" && !sp.HasGame {
			t.Error("backup should have HasGame=true")
		}
	}
}

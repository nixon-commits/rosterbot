package projections

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// stubPPSSource implements both Source and PtsPerGameSource for testing.
type stubPPSSource struct {
	proj map[string]*Projection
	pts  map[string]float64
}

func (s *stubPPSSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	p, ok := s.proj[NormalizeName(name)]
	return p, ok
}

func (s *stubPPSSource) GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	pts, ok := s.pts[NormalizeName(name)]
	return pts, ok
}

// defaultStubPPS returns a stubPPSSource with "test player" at 5.00 pts/game.
func defaultStubPPS() *stubPPSSource {
	return &stubPPSSource{
		proj: map[string]*Projection{
			"test player": {G: 100, HR: 10, RBI: 50, R: 40},
		},
		pts: map[string]float64{
			"test player": 5.00,
		},
	}
}

var defaultScoring = fantrax.ScoringWeights{"HR": 4.0, "RBI": 1.0, "R": 1.0}

// TestMatchupAdjusted_FavorablePlatoon: RHH vs LHP, FIP==avgFIP → no change.
func TestMatchupAdjusted_FavorablePlatoon(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "R"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("RHH vs LHP with neutral FIP: expected 5.00, got %.4f", pts)
	}
}

// TestMatchupAdjusted_UnfavorablePlatoon: LHH vs LHP, FIP==avgFIP → 0.93 mult.
func TestMatchupAdjusted_UnfavorablePlatoon(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "L"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	expected := 5.00 * 0.93
	if math.Abs(pts-expected) > 0.001 {
		t.Errorf("LHH vs LHP: expected %.4f, got %.4f", expected, pts)
	}
}

// TestMatchupAdjusted_SwitchHitter: "S" vs LHP → no platoon penalty.
func TestMatchupAdjusted_SwitchHitter(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "S"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("switch hitter: expected 5.00, got %.4f", pts)
	}
}

// TestMatchupAdjusted_AceSuppression: RHH vs LHP (favorable), FIP=2.80, avgFIP=4.00.
// quality = clamp(2.80/4.00=0.70, 0.85, 1.15) = 0.85. Expected 5.00*0.85=4.25.
func TestMatchupAdjusted_AceSuppression(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace Pitcher", Team: "BOS", Throws: "L", FIP: 2.80},
		},
		map[string]string{"test player": "R"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	expected := 5.00 * 0.85
	if math.Abs(pts-expected) > 0.001 {
		t.Errorf("ace suppression: expected %.4f, got %.4f", expected, pts)
	}
}

// TestMatchupAdjusted_BadPitcherBoost: LHH vs RHP (favorable), FIP=5.50, avgFIP=4.00.
// quality = clamp(5.50/4.00=1.375, 0.85, 1.15) = 1.15. Expected 5.00*1.15=5.75.
func TestMatchupAdjusted_BadPitcherBoost(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Bad Pitcher", Team: "BOS", Throws: "R", FIP: 5.50},
		},
		map[string]string{"test player": "L"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	// LHH vs RHP is favorable (no platoon penalty). Quality boost capped at 1.15.
	expected := 5.00 * 1.15
	if math.Abs(pts-expected) > 0.001 {
		t.Errorf("bad pitcher boost: expected %.4f, got %.4f", expected, pts)
	}
}

// TestMatchupAdjusted_CombinedCap: LHH vs LHP (unfavorable=0.93), FIP=2.80 (capped=0.85).
// combined = clamp(0.93*0.85=0.7905, 0.80, 1.15) = 0.80. Expected 5.00*0.80=4.00.
func TestMatchupAdjusted_CombinedCap(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace Pitcher", Team: "BOS", Throws: "L", FIP: 2.80},
		},
		map[string]string{"test player": "L"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	expected := 5.00 * 0.80
	if math.Abs(pts-expected) > 0.001 {
		t.Errorf("combined cap: expected %.4f, got %.4f", expected, pts)
	}
}

// TestMatchupAdjusted_NoOpposingPitcher: no opposing pitcher → no adjustment.
func TestMatchupAdjusted_NoOpposingPitcher(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{},
		map[string]string{"test player": "L"},
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("no opposing pitcher: expected 5.00, got %.4f", pts)
	}
}

// TestMatchupAdjusted_UnknownHitterHandedness: empty hitterBats map.
// Pitcher quality still applies; platoon mult defaults to 1.0.
func TestMatchupAdjusted_UnknownHitterHandedness(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{}, // no handedness data
		4.00,
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	// FIP==avgFIP → quality=1.0; no handedness → platoon=1.0; combined=1.0.
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("unknown handedness + neutral FIP: expected 5.00, got %.4f", pts)
	}
}

// TestMatchupAdjusted_TeamNotPlaying: opposing pitcher for NYY but hitter is on BOS.
func TestMatchupAdjusted_TeamNotPlaying(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Pitcher", Team: "BOS", Throws: "L", FIP: 2.80},
		},
		map[string]string{"test player": "L"},
		4.00,
	)

	// Hitter is on BOS, not NYY, so no opposing pitcher entry applies.
	pts, ok := src.GetPtsPerGame("Test Player", "BOS", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("team not playing: expected 5.00, got %.4f", pts)
	}
}

// TestMatchupAdjusted_ZeroLeagueAvgFIP: guard against division by zero.
func TestMatchupAdjusted_ZeroLeagueAvgFIP(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "R"},
		0, // leagueAvgFIP = 0
	)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", defaultScoring)
	if !ok {
		t.Fatal("expected true")
	}
	// Quality mult skipped (leagueAvgFIP=0); platoon=1.0 (RHH vs LHP); combined=1.0.
	if math.Abs(pts-5.00) > 0.001 {
		t.Errorf("zero leagueAvgFIP: expected 5.00, got %.4f", pts)
	}
}

// TestGetMatchupDetail_UnfavorablePlatoon: LHH vs LHP.
func TestGetMatchupDetail_UnfavorablePlatoon(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "L"},
		4.00,
	)

	d := src.GetMatchupDetail("Test Player", "NYY")
	if d.PlatoonMult != 0.93 {
		t.Errorf("expected platoon mult 0.93, got %.4f", d.PlatoonMult)
	}
	if d.Favorable == nil || *d.Favorable {
		t.Error("expected unfavorable platoon")
	}
	if math.Abs(d.QualityMult-1.0) > 0.001 {
		t.Errorf("expected quality mult 1.0 (neutral FIP), got %.4f", d.QualityMult)
	}
	if d.OpposingPitcher != "Ace Pitcher" {
		t.Errorf("expected opposing pitcher 'Ace Pitcher', got %q", d.OpposingPitcher)
	}
	if math.Abs(d.CombinedMult-0.93) > 0.001 {
		t.Errorf("expected combined mult 0.93, got %.4f", d.CombinedMult)
	}
}

// TestGetMatchupDetail_FavorablePlatoon: RHH vs LHP.
func TestGetMatchupDetail_FavorablePlatoon(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "R"},
		4.00,
	)

	d := src.GetMatchupDetail("Test Player", "NYY")
	if math.Abs(d.PlatoonMult-1.0) > 0.001 {
		t.Errorf("expected platoon mult 1.0, got %.4f", d.PlatoonMult)
	}
	if d.Favorable == nil || !*d.Favorable {
		t.Error("expected favorable platoon")
	}
}

// TestGetMatchupDetail_NoOpposingPitcher: returns neutral defaults.
func TestGetMatchupDetail_NoOpposingPitcher(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{},
		map[string]string{"test player": "L"},
		4.00,
	)

	d := src.GetMatchupDetail("Test Player", "NYY")
	if math.Abs(d.CombinedMult-1.0) > 0.001 {
		t.Errorf("expected combined mult 1.0 (no opposing pitcher), got %.4f", d.CombinedMult)
	}
	if d.Favorable != nil {
		t.Error("expected nil favorable when no opposing pitcher")
	}
}

// TestGetMatchupDetail_AceQuality: FIP=2.80, avgFIP=4.00 → quality=0.85.
func TestGetMatchupDetail_AceQuality(t *testing.T) {
	inner := defaultStubPPS()
	src := NewMatchupAdjustedSource(
		inner,
		map[string]OpposingPitcher{
			"NYY": {Name: "Ace", Team: "BOS", Throws: "L", FIP: 2.80},
		},
		map[string]string{"test player": "R"},
		4.00,
	)

	d := src.GetMatchupDetail("Test Player", "NYY")
	if math.Abs(d.QualityMult-0.85) > 0.001 {
		t.Errorf("expected quality mult 0.85 (clamped), got %.4f", d.QualityMult)
	}
}

// TestMatchupAdjusted_FullChainComposability: stubSource → BlendedSource → MatchupAdjustedSource.
// LHH vs LHP, neutral FIP (4.00==4.00), unfavorable platoon → 0.93 reduction on blended value.
func TestMatchupAdjusted_FullChainComposability(t *testing.T) {
	innerProj := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "R": 1.0, "RBI": 1.0}

	// Blended: no recent stats → falls back to 100% projection.
	blended := NewBlendedSource(innerProj, map[string]fantrax.RecentStat{}, scoring,
		map[string]string{}, 2)

	matchupAdj := NewMatchupAdjustedSource(
		blended,
		map[string]OpposingPitcher{
			"NYY": {Name: "LH Pitcher", Team: "BOS", Throws: "L", FIP: 4.00},
		},
		map[string]string{"test player": "L"},
		4.00,
	)

	blendedPts, _ := blended.GetPtsPerGame("Test Player", "NYY", scoring)
	matchupPts, ok := matchupAdj.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}

	expected := blendedPts * 0.93
	if math.Abs(matchupPts-expected) > 0.001 {
		t.Errorf("full chain: expected blendedPts*0.93=%.4f, got %.4f (blendedPts=%.4f)", expected, matchupPts, blendedPts)
	}
}

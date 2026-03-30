package projections

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func TestParkAdjustedSource_CoorsBoost(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30, SO: 80},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "2B": 2.0, "3B": 3.0, "R": 1.0, "RBI": 1.0, "BB": 1.0, "SO": -1.0, "XBH": 1.0, "TB": 1.0}

	// Coors-like park factors.
	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", HR: 1.06, H: 1.17, R: 1.28, BB: 1.01, SO: 0.90, H1B: 1.16, H2B: 1.19, H3B: 2.02},
	}
	venues := map[string]string{"NYY": "COL"} // NYY playing at COL

	src := NewParkAdjustedSource(inner, parkFactors, venues)

	basePts := ExpectedPtsFromProj(inner.proj["test player"], scoring)
	adjPts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	// Coors should boost the projection.
	if adjPts <= basePts {
		t.Errorf("expected Coors boost: adj=%.4f should be > base=%.4f", adjPts, basePts)
	}

	// Player has Doubles=20, Triples=5, HR=15 (xbh=40).
	// Coors: H2B=1.19, H3B=2.02, HR=1.06.
	// Player-weighted XBH factor = (20*1.19 + 5*2.02 + 15*1.06) / 40 = 49.8/40 = 1.245.
	// Old simple average = (1.19 + 2.02 + 1.06) / 3 = 1.4233.
	// The adjustment ratio with weighted XBH ≈ 1.2686; with simple average ≈ 1.2852.
	// adjPts should be basePts * ~1.2686 (weighted), NOT basePts * ~1.2852 (simple avg).
	expectedAdjPts := basePts * 1.2686
	if math.Abs(adjPts-expectedAdjPts) > 0.01 {
		t.Errorf("XBH factor should use player-weighted formula: adjPts=%.4f, want ~%.4f (weighted); simple-avg would give ~%.4f",
			adjPts, expectedAdjPts, basePts*1.2852)
	}
}

func TestParkAdjustedSource_PitcherPark_Suppresses(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30, SO: 80},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "R": 1.0, "RBI": 1.0, "SO": -1.0}

	// Pitcher park (suppressed offense).
	parkFactors := map[string]ParkFactors{
		"SEA": {Team: "SEA", HR: 0.93, H: 0.89, R: 0.83, BB: 0.97, SO: 1.17, H1B: 0.89, H2B: 0.89, H3B: 0.52},
	}
	venues := map[string]string{"NYY": "SEA"}

	src := NewParkAdjustedSource(inner, parkFactors, venues)

	basePts := ExpectedPtsFromProj(inner.proj["test player"], scoring)
	adjPts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if adjPts >= basePts {
		t.Errorf("expected pitcher park suppression: adj=%.4f should be < base=%.4f", adjPts, basePts)
	}
}

func TestParkAdjustedSource_NoVenue_ReturnsBase(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, HR: 20, RBI: 60},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "RBI": 1.0}

	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", HR: 1.06, R: 1.28},
	}
	// No venues — team not playing today.
	src := NewParkAdjustedSource(inner, parkFactors, map[string]string{})

	basePts := ExpectedPtsFromProj(inner.proj["test player"], scoring)
	adjPts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(adjPts-basePts) > 0.001 {
		t.Errorf("expected unadjusted (no venue): adj=%.4f, base=%.4f", adjPts, basePts)
	}
}

func TestParkAdjustedSource_NeutralPark_NoChange(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30, SO: 80},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "R": 1.0, "SO": -1.0}

	// Perfectly neutral park.
	parkFactors := map[string]ParkFactors{
		"NYY": {Team: "NYY", HR: 1.0, H: 1.0, R: 1.0, BB: 1.0, SO: 1.0, H1B: 1.0, H2B: 1.0, H3B: 1.0},
	}
	venues := map[string]string{"NYY": "NYY"}

	src := NewParkAdjustedSource(inner, parkFactors, venues)

	basePts := ExpectedPtsFromProj(inner.proj["test player"], scoring)
	adjPts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if math.Abs(adjPts-basePts) > 0.001 {
		t.Errorf("neutral park should produce same pts: adj=%.4f, base=%.4f", adjPts, basePts)
	}
}

func TestParkAdjustedSource_ParkFactor(t *testing.T) {
	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", R: 1.28},
		"SEA": {Team: "SEA", R: 0.83},
	}
	venues := map[string]string{"NYY": "COL", "BOS": "SEA", "COL": "COL"}

	src := NewParkAdjustedSource(&stubSource{}, parkFactors, venues)

	if pf := src.ParkFactor("NYY"); math.Abs(pf-1.28) > 0.001 {
		t.Errorf("expected 1.28 for NYY@COL, got %.2f", pf)
	}
	if pf := src.ParkFactor("BOS"); math.Abs(pf-0.83) > 0.001 {
		t.Errorf("expected 0.83 for BOS@SEA, got %.2f", pf)
	}
	if pf := src.ParkFactor("MIA"); math.Abs(pf-1.0) > 0.001 {
		t.Errorf("expected 1.0 for MIA (not playing), got %.2f", pf)
	}
}

func TestParkAdjustedSource_ComputeParkAdjustment(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30, SO: 80},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "2B": 2.0, "3B": 3.0, "R": 1.0, "RBI": 1.0, "BB": 1.0, "SO": -1.0, "XBH": 1.0, "TB": 1.0}

	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", HR: 1.06, H: 1.17, R: 1.28, BB: 1.01, SO: 0.90, H1B: 1.16, H2B: 1.19, H3B: 2.02},
	}
	venues := map[string]string{"NYY": "COL"}

	src := NewParkAdjustedSource(inner, parkFactors, venues)

	// ComputeParkAdjustment should return the same multiplier used internally.
	adj := src.ComputeParkAdjustment("Test Player", "NYY", scoring)
	if adj <= 1.0 {
		t.Errorf("expected Coors park adjustment > 1.0, got %.4f", adj)
	}

	// Verify it matches the ratio of adjusted/base pts.
	basePts := ExpectedPtsFromProj(inner.proj["test player"], scoring)
	adjPts, _ := src.GetPtsPerGame("Test Player", "NYY", scoring)
	expectedAdj := adjPts / basePts
	if math.Abs(adj-expectedAdj) > 0.0001 {
		t.Errorf("ComputeParkAdjustment=%.4f, but GetPtsPerGame ratio=%.4f", adj, expectedAdj)
	}
}

func TestParkAdjustedSource_ComputeParkAdjustment_NoVenue(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, HR: 20, RBI: 60},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "RBI": 1.0}

	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", HR: 1.06, R: 1.28},
	}
	src := NewParkAdjustedSource(inner, parkFactors, map[string]string{})

	adj := src.ComputeParkAdjustment("Test Player", "NYY", scoring)
	if adj != 1.0 {
		t.Errorf("expected 1.0 for missing venue, got %.4f", adj)
	}
}

func TestParkAdjustedSource_WithBlendedInner(t *testing.T) {
	innerProj := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, Singles: 60, Doubles: 20, Triples: 5, HR: 15, RBI: 50, R: 40, BB: 30},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "1B": 1.0, "R": 1.0, "RBI": 1.0}

	// Create a BlendedSource with recent stats.
	blended := NewBlendedSource(innerProj, map[string]fantrax.RecentStat{
		"player1": {FPtsPerGame: 3.0, GamesPlayed: 5},
	}, scoring, map[string]string{"test player": "player1"}, 2)

	parkFactors := map[string]ParkFactors{
		"COL": {Team: "COL", HR: 1.06, H: 1.17, R: 1.28, BB: 1.01, SO: 0.90, H1B: 1.16, H2B: 1.19, H3B: 2.02},
	}
	venues := map[string]string{"NYY": "COL"}

	src := NewParkAdjustedSource(blended, parkFactors, venues)

	blendedPts, _ := blended.GetPtsPerGame("Test Player", "NYY", scoring)
	adjPts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	// Should adjust the blended value, not just the base projection.
	if adjPts <= blendedPts {
		t.Errorf("expected Coors boost on blended: adj=%.4f should be > blended=%.4f", adjPts, blendedPts)
	}
}

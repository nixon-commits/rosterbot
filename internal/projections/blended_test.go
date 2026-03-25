package projections

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

type stubSource struct {
	proj map[string]*Projection
}

func (s *stubSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	p, ok := s.proj[NormalizeName(name)]
	return p, ok
}

func TestBlendedSource_WithRecentStats(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, H: 100, HR: 20, RBI: 60, R: 50, BB: 40},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0, "RBI": 1.0}
	// Steamer: (20/100)*4 + (60/100)*1 = 0.8 + 0.6 = 1.4
	// Recent: 10/5 = 2.0
	// 5 GP: approxPA = 19, seasonW = 19/269 ≈ 0.0706
	// steamerW = 0.9294, recentW = 0.0706
	// Blended ≈ 0.9294*1.4 + 0.0706*2.0 ≈ 1.4424

	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{
		"player1": {TotalFP: 10.0, GamesPlayed: 5},
	}, scoring, map[string]string{"test player": "player1"})

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if pts < 1.43 || pts > 1.46 {
		t.Errorf("expected ~1.44, got %.4f", pts)
	}
}

func TestBlendedSource_NoRecentStats_FallsBackToSteamer(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, HR: 20},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0}
	// Steamer only: (20/100)*4 = 0.8

	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{}, scoring,
		map[string]string{"test player": "player1"})

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if pts < 0.79 || pts > 0.81 {
		t.Errorf("expected ~0.8, got %.4f", pts)
	}
}

func TestBlendedSource_NoSteamer_ReturnsFalse(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{}}
	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{}, nil, map[string]string{})
	_, ok := src.GetPtsPerGame("Unknown Player", "NYY", fantrax.ScoringWeights{"HR": 4.0})
	if ok {
		t.Error("expected false for unknown player")
	}
}

func TestBlendedSource_GetProjection_Delegates(t *testing.T) {
	proj := &Projection{G: 100, HR: 20}
	inner := &stubSource{proj: map[string]*Projection{"test player": proj}}
	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{}, nil, nil)
	p, ok := src.GetProjection("Test Player", "NYY")
	if !ok {
		t.Fatal("expected projection found")
	}
	if p.HR != 20 {
		t.Errorf("expected HR=20, got %.0f", p.HR)
	}
}

func TestHitterBlendWeights_Progression(t *testing.T) {
	tests := []struct {
		gp           int
		expectSteam  float64 // approximate expected Steamer weight
		tolerance    float64
	}{
		{4, 0.94, 0.02},   // early season: ~94% Steamer
		{66, 0.50, 0.02},  // mid-June: ~50/50
		{150, 0.30, 0.01}, // September: hits 30% floor
		{162, 0.30, 0.01}, // full season: stays at 30% floor
	}

	for _, tt := range tests {
		sw, rw := HitterBlendWeightsForDisplay(tt.gp)
		if math.Abs(sw-tt.expectSteam) > tt.tolerance {
			t.Errorf("GP=%d: expected steamer ~%.2f, got %.4f", tt.gp, tt.expectSteam, sw)
		}
		if math.Abs((sw+rw)-1.0) > 1e-9 {
			t.Errorf("GP=%d: weights don't sum to 1.0: %.4f + %.4f = %.4f", tt.gp, sw, rw, sw+rw)
		}
	}
}

func TestHitterBlendWeights_SumToOne(t *testing.T) {
	for gp := 0; gp <= 162; gp++ {
		sw, rw := HitterBlendWeightsForDisplay(gp)
		sum := sw + rw
		if math.Abs(sum-1.0) > 1e-9 {
			t.Errorf("GP=%d: weights sum to %.10f, expected 1.0", gp, sum)
		}
	}
}

func TestHitterBlendWeights_SteamerFloor(t *testing.T) {
	for gp := 0; gp <= 200; gp++ {
		sw, _ := HitterBlendWeightsForDisplay(gp)
		if sw < hitterSteamerFloor-1e-9 {
			t.Errorf("GP=%d: steamer weight %.4f below floor %.2f", gp, sw, hitterSteamerFloor)
		}
	}
}

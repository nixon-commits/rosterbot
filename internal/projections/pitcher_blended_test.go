package projections

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
)

func TestPitcherBlendWeights_SP_Progression(t *testing.T) {
	tests := []struct {
		gp          int
		expectSteam float64
		tolerance   float64
	}{
		{4, 0.79, 0.02},  // early: 4/(4+15) ≈ 0.21 season → ~79% Steamer
		{15, 0.50, 0.01}, // stabilization point: 50/50
		{30, 0.35, 0.01}, // past stabilization: hits 35% floor
		{50, 0.35, 0.01}, // deep season: stays at floor
	}

	for _, tt := range tests {
		sw, rw := PitcherBlendWeightsForDisplay(tt.gp, true)
		if math.Abs(sw-tt.expectSteam) > tt.tolerance {
			t.Errorf("SP GP=%d: expected steamer ~%.2f, got %.4f", tt.gp, tt.expectSteam, sw)
		}
		if math.Abs((sw+rw)-1.0) > 1e-9 {
			t.Errorf("SP GP=%d: weights don't sum to 1.0: %.4f + %.4f", tt.gp, sw, rw)
		}
	}
}

func TestPitcherBlendWeights_RP_Progression(t *testing.T) {
	tests := []struct {
		gp          int
		expectSteam float64
		tolerance   float64
	}{
		{4, 0.86, 0.02},  // early: 4/(4+25) ≈ 0.14 season → ~86% Steamer
		{25, 0.50, 0.01}, // stabilization point: 50/50
		{50, 0.35, 0.01}, // past stabilization: hits 35% floor
		{75, 0.35, 0.01}, // deep season: stays at floor
	}

	for _, tt := range tests {
		sw, rw := PitcherBlendWeightsForDisplay(tt.gp, false)
		if math.Abs(sw-tt.expectSteam) > tt.tolerance {
			t.Errorf("RP GP=%d: expected steamer ~%.2f, got %.4f", tt.gp, tt.expectSteam, sw)
		}
		if math.Abs((sw+rw)-1.0) > 1e-9 {
			t.Errorf("RP GP=%d: weights don't sum to 1.0: %.4f + %.4f", tt.gp, sw, rw)
		}
	}
}

func TestPitcherBlendWeights_SteamerFloor(t *testing.T) {
	for gp := 0; gp <= 100; gp++ {
		for _, isSP := range []bool{true, false} {
			sw, _ := PitcherBlendWeightsForDisplay(gp, isSP)
			if sw < pitcherSteamerFloor-1e-9 {
				role := "RP"
				if isSP {
					role = "SP"
				}
				t.Errorf("%s GP=%d: steamer weight %.4f below floor %.2f", role, gp, sw, pitcherSteamerFloor)
			}
		}
	}
}

func TestPitcherBlendWeights_SumToOne(t *testing.T) {
	for gp := 0; gp <= 100; gp++ {
		for _, isSP := range []bool{true, false} {
			sw, rw := PitcherBlendWeightsForDisplay(gp, isSP)
			sum := sw + rw
			if math.Abs(sum-1.0) > 1e-9 {
				t.Errorf("GP=%d isSP=%v: weights sum to %.10f, expected 1.0", gp, isSP, sum)
			}
		}
	}
}

// --- GetPitcherBreakdown tests ---

type stubPitSrc struct {
	data map[string]*PitcherProjection
}

func (s *stubPitSrc) GetPitcherProjection(name, _ string) (*PitcherProjection, bool) {
	p, ok := s.data[NormalizeName(name)]
	return p, ok
}

func testPitScoring() fantrax.ScoringWeights {
	return fantrax.ScoringWeights{
		"K": 3, "W": 10, "L": -5, "QS": 5, "SV": 7, "HLD": 5,
		"ER": -2, "BB": -1, "H": -1, "HR": -2, "IP": 3,
		"BS": -3, "HBP": -1, "WP": -1, "BK": -1, "CG": 5, "SHO": 5, "PKO": 1,
	}
}

func TestGetPitcherBreakdown_NoRecentData(t *testing.T) {
	proj := &PitcherProjection{G: 30, GS: 30, K: 200, W: 15, IP: 180}
	inner := &stubPitSrc{data: map[string]*PitcherProjection{"ace pitcher": proj}}
	scoring := testPitScoring()

	src := NewPitcherBlendedSource(inner, nil, scoring, map[string]string{"ace pitcher": "p1"}, map[string][]string{"p1": {auth_client.PosSP}}, 2)
	bd := src.GetPitcherBreakdown("Ace Pitcher", "NYY", scoring)

	if bd == nil {
		t.Fatal("expected non-nil breakdown")
	}
	if bd.HasRecent {
		t.Error("expected HasRecent=false with no recent data")
	}
	if bd.BaseWt != 1.0 {
		t.Errorf("expected BaseWt=1.0, got %.4f", bd.BaseWt)
	}
	if bd.BlendedPts != bd.BasePts {
		t.Errorf("expected BlendedPts=BasePts (%.4f), got %.4f", bd.BasePts, bd.BlendedPts)
	}
	if !bd.IsSP {
		t.Error("expected IsSP=true for SP-eligible pitcher")
	}
}

func TestGetPitcherBreakdown_WithRecentData(t *testing.T) {
	proj := &PitcherProjection{G: 30, GS: 30, K: 200, W: 15, IP: 180}
	inner := &stubPitSrc{data: map[string]*PitcherProjection{"ace pitcher": proj}}
	scoring := testPitScoring()

	recent := map[string]fantrax.RecentStat{
		"p1": {FPtsPerGame: 25.0, GamesPlayed: 15},
	}
	nameToID := map[string]string{"ace pitcher": "p1"}
	playerPos := map[string][]string{"p1": {auth_client.PosSP}}

	src := NewPitcherBlendedSource(inner, recent, scoring, nameToID, playerPos, 2)
	bd := src.GetPitcherBreakdown("Ace Pitcher", "NYY", scoring)

	if bd == nil {
		t.Fatal("expected non-nil breakdown")
	}
	if !bd.HasRecent {
		t.Error("expected HasRecent=true")
	}
	if bd.GamesPlayed != 15 {
		t.Errorf("expected GamesPlayed=15, got %d", bd.GamesPlayed)
	}
	// SP at 15 GP should be 50/50.
	if math.Abs(bd.BaseWt-0.50) > 0.01 {
		t.Errorf("expected BaseWt ~0.50, got %.4f", bd.BaseWt)
	}
	if math.Abs(bd.RecentWt-0.50) > 0.01 {
		t.Errorf("expected RecentWt ~0.50, got %.4f", bd.RecentWt)
	}
	expectedBlend := bd.BaseWt*bd.BasePts + bd.RecentWt*25.0
	if math.Abs(bd.BlendedPts-expectedBlend) > 1e-9 {
		t.Errorf("expected BlendedPts=%.4f, got %.4f", expectedBlend, bd.BlendedPts)
	}
}

func TestGetPitcherBreakdown_InsufficientGP(t *testing.T) {
	proj := &PitcherProjection{G: 30, K: 100, IP: 150}
	inner := &stubPitSrc{data: map[string]*PitcherProjection{"reliever": proj}}
	scoring := testPitScoring()

	recent := map[string]fantrax.RecentStat{
		"p1": {FPtsPerGame: 20.0, GamesPlayed: 1}, // below minGP=2
	}
	nameToID := map[string]string{"reliever": "p1"}
	playerPos := map[string][]string{"p1": {"016"}} // RP only

	src := NewPitcherBlendedSource(inner, recent, scoring, nameToID, playerPos, 2)
	bd := src.GetPitcherBreakdown("Reliever", "NYY", scoring)

	if bd == nil {
		t.Fatal("expected non-nil breakdown")
	}
	if bd.HasRecent {
		t.Error("expected HasRecent=false with insufficient GP")
	}
	if bd.IsSP {
		t.Error("expected IsSP=false for RP-only pitcher")
	}
}

func TestGetPitcherBreakdown_NoProjection(t *testing.T) {
	inner := &stubPitSrc{data: map[string]*PitcherProjection{}}
	scoring := testPitScoring()

	src := NewPitcherBlendedSource(inner, nil, scoring, nil, nil, 2)
	bd := src.GetPitcherBreakdown("Unknown Pitcher", "NYY", scoring)

	if bd != nil {
		t.Error("expected nil breakdown for unknown pitcher")
	}
}

package projections

import (
	"math"
	"testing"
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

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
	// Base: (20/100)*4 + (60/100)*1 = 0.8 + 0.6 = 1.4
	// Recent: 10/5 = 2.0
	// 5 GP: approxPA = 19, seasonW = 19/269 ≈ 0.0706
	// baseW = 0.9294, recentW = 0.0706
	// Blended ≈ 0.9294*1.4 + 0.0706*2.0 ≈ 1.4424

	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{
		"player1": {FPtsPerGame: 2.0, GamesPlayed: 5},
	}, scoring, map[string]string{"test player": "player1"}, 2, 0)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if pts < 1.43 || pts > 1.46 {
		t.Errorf("expected ~1.44, got %.4f", pts)
	}
}

func TestBlendedSource_NoRecentStats_FallsBackToBase(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{
		"test player": {G: 100, HR: 20},
	}}
	scoring := fantrax.ScoringWeights{"HR": 4.0}
	// Base only: (20/100)*4 = 0.8

	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{}, scoring,
		map[string]string{"test player": "player1"}, 2, 0)

	pts, ok := src.GetPtsPerGame("Test Player", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	if pts < 0.79 || pts > 0.81 {
		t.Errorf("expected ~0.8, got %.4f", pts)
	}
}

func TestBlendedSource_NoBaseProjection_ReturnsFalse(t *testing.T) {
	inner := &stubSource{proj: map[string]*Projection{}}
	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{}, nil, map[string]string{}, 2, 0)
	_, ok := src.GetPtsPerGame("Unknown Player", "NYY", fantrax.ScoringWeights{"HR": 4.0})
	if ok {
		t.Error("expected false for unknown player")
	}
}

func TestBlendedSource_NoBaseProjection_SmallSample_ShrinksTowardBaseline(t *testing.T) {
	// A call-up with no FanGraphs projection who went 2-for-2 with 2 HR in
	// their first 2 games. Before rosterbot-4h7, this raw 2-game rate would
	// be returned unshrunk (~13+ FP/G). It should instead land close to the
	// league-average baseline, since 2 GP carries almost no signal.
	inner := &stubSource{proj: map[string]*Projection{}}
	scoring := fantrax.ScoringWeights{"HR": 4.0}
	const baseline = 1.2 // a plausible league-average hitter FP/G

	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{
		"player1": {FPtsPerGame: 13.0, GamesPlayed: 2},
	}, scoring, map[string]string{"call-up": "player1"}, 2, baseline)

	pts, ok := src.GetPtsPerGame("Call-Up", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	// At 2 GP the shrinkage curve puts recent weight well under 10%
	// (matches TestHitterBlendWeights_Progression: 4 GP ~= 6% recent).
	if pts > baseline+1.5 {
		t.Errorf("expected small-sample outlier pulled near baseline %.2f, got %.4f (raw recent was 13.0)", baseline, pts)
	}
	if pts <= baseline {
		t.Errorf("expected some upward pull from the hot recent sample, got %.4f <= baseline %.2f", pts, baseline)
	}
}

func TestBlendedSource_NoBaseProjection_LargeSample_LargelyUnaffected(t *testing.T) {
	// A player with a full season of recent data but no base projection
	// (e.g. no upstream coverage at all) should stay close to their own
	// established rate, not get dragged toward the baseline.
	inner := &stubSource{proj: map[string]*Projection{}}
	scoring := fantrax.ScoringWeights{"HR": 4.0}
	const baseline = 1.2
	const establishedRate = 5.0

	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{
		"player1": {FPtsPerGame: establishedRate, GamesPlayed: 150},
	}, scoring, map[string]string{"veteran": "player1"}, 2, baseline)

	pts, ok := src.GetPtsPerGame("Veteran", "NYY", scoring)
	if !ok {
		t.Fatal("expected true")
	}
	// At 150 GP, recent weight is ~70% (30% base floor), so the result
	// should sit meaningfully closer to the established rate than to the
	// baseline, though not identical to raw (base floor still applies).
	if pts < establishedRate*0.6 {
		t.Errorf("expected largely unaffected result close to %.2f, got %.4f", establishedRate, pts)
	}
}

func TestBlendedSource_GetProjection_Delegates(t *testing.T) {
	proj := &Projection{G: 100, HR: 20}
	inner := &stubSource{proj: map[string]*Projection{"test player": proj}}
	src := NewBlendedSource(inner, map[string]fantrax.RecentStat{}, nil, nil, 2, 0)
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
		gp         int
		expectBase float64 // approximate expected base projection weight
		tolerance  float64
	}{
		{4, 0.94, 0.02},   // early season: ~94% base
		{66, 0.50, 0.02},  // mid-June: ~50/50
		{150, 0.30, 0.01}, // September: hits 30% floor
		{162, 0.30, 0.01}, // full season: stays at 30% floor
	}

	for _, tt := range tests {
		sw, rw := HitterBlendWeightsForDisplay(tt.gp)
		if math.Abs(sw-tt.expectBase) > tt.tolerance {
			t.Errorf("GP=%d: expected base ~%.2f, got %.4f", tt.gp, tt.expectBase, sw)
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

func TestHitterBlendWeights_BaseFloor(t *testing.T) {
	for gp := 0; gp <= 200; gp++ {
		sw, _ := HitterBlendWeightsForDisplay(gp)
		if sw < hitterBaseFloor-1e-9 {
			t.Errorf("GP=%d: base weight %.4f below floor %.2f", gp, sw, hitterBaseFloor)
		}
	}
}

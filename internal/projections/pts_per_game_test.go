package projections

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

type fakePPSSource struct {
	proj *Projection
	pts  float64
	hit  bool
}

func (f fakePPSSource) GetProjection(_, _ string) (*Projection, bool) {
	return f.proj, f.proj != nil
}
func (f fakePPSSource) GetPtsPerGame(_, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	return f.pts, f.hit
}

type fakePlainSource struct{ proj *Projection }

func (f fakePlainSource) GetProjection(_, _ string) (*Projection, bool) {
	return f.proj, f.proj != nil
}

type fakePitcherPPSSource struct {
	proj *PitcherProjection
	pts  float64
	hit  bool
}

func (f fakePitcherPPSSource) GetPitcherProjection(_, _ string) (*PitcherProjection, bool) {
	return f.proj, f.proj != nil
}
func (f fakePitcherPPSSource) GetPitcherPtsPerGame(_, _ string, _ fantrax.ScoringWeights) (float64, bool) {
	return f.pts, f.hit
}

type fakePlainPitcherSource struct{ proj *PitcherProjection }

func (f fakePlainPitcherSource) GetPitcherProjection(_, _ string) (*PitcherProjection, bool) {
	return f.proj, f.proj != nil
}

// TestPointsPerGame pins the one assert-and-fallback that three call sites
// used to hand-roll: the blended per-game path wins when the source provides
// it, a per-game miss falls THROUGH to the season projection, and a
// projection with no games (or none at all) is an honest false, never a zero
// passed off as a value.
func TestPointsPerGame(t *testing.T) {
	scoring := fantrax.ScoringWeights{"HR": 4}
	proj := &Projection{G: 100, HR: 30} // 1.2 pts/G at HR:4

	t.Run("BlendedPathWinsWhenPresent", func(t *testing.T) {
		pts, ok := PointsPerGame(fakePPSSource{proj: proj, pts: 9.9, hit: true}, "A", "NYY", scoring)
		if !ok || pts != 9.9 {
			t.Fatalf("= (%v, %v), want the blended 9.9", pts, ok)
		}
	})

	t.Run("BlendedMissFallsThroughToProjection", func(t *testing.T) {
		pts, ok := PointsPerGame(fakePPSSource{proj: proj, hit: false}, "A", "NYY", scoring)
		if !ok || pts != 1.2 {
			t.Fatalf("= (%v, %v), want the derived 1.2", pts, ok)
		}
	})

	t.Run("PlainSourceDerivesFromProjection", func(t *testing.T) {
		pts, ok := PointsPerGame(fakePlainSource{proj: proj}, "A", "NYY", scoring)
		if !ok || pts != 1.2 {
			t.Fatalf("= (%v, %v), want the derived 1.2", pts, ok)
		}
	})

	t.Run("NoProjectionIsFalse", func(t *testing.T) {
		if pts, ok := PointsPerGame(fakePlainSource{}, "A", "NYY", scoring); ok || pts != 0 {
			t.Fatalf("= (%v, %v), want (0, false)", pts, ok)
		}
	})

	t.Run("ZeroGamesIsFalse", func(t *testing.T) {
		if _, ok := PointsPerGame(fakePlainSource{proj: &Projection{G: 0, HR: 30}}, "A", "NYY", scoring); ok {
			t.Fatal("a zero-G projection must not divide into a value")
		}
	})
}

// TestPitcherPointsPerGame is the pitcher twin, over the pitcher interfaces.
func TestPitcherPointsPerGame(t *testing.T) {
	scoring := fantrax.ScoringWeights{"SO": 1}
	proj := &PitcherProjection{G: 50, K: 100}

	t.Run("BlendedPathWinsWhenPresent", func(t *testing.T) {
		pts, ok := PitcherPointsPerGame(fakePitcherPPSSource{proj: proj, pts: 7.7, hit: true}, "A", "NYY", scoring)
		if !ok || pts != 7.7 {
			t.Fatalf("= (%v, %v), want the blended 7.7", pts, ok)
		}
	})

	t.Run("BlendedMissFallsThroughToProjection", func(t *testing.T) {
		want := PitcherExpectedPtsFromProj(proj, scoring)
		pts, ok := PitcherPointsPerGame(fakePitcherPPSSource{proj: proj, hit: false}, "A", "NYY", scoring)
		if !ok || pts != want {
			t.Fatalf("= (%v, %v), want the derived %v", pts, ok, want)
		}
	})

	t.Run("PlainSourceDerivesFromProjection", func(t *testing.T) {
		want := PitcherExpectedPtsFromProj(proj, scoring)
		pts, ok := PitcherPointsPerGame(fakePlainPitcherSource{proj: proj}, "A", "NYY", scoring)
		if !ok || pts != want {
			t.Fatalf("= (%v, %v), want the derived %v", pts, ok, want)
		}
	})

	t.Run("NoProjectionIsFalse", func(t *testing.T) {
		if _, ok := PitcherPointsPerGame(fakePlainPitcherSource{}, "A", "NYY", scoring); ok {
			t.Fatal("want false with no projection")
		}
	})

	t.Run("ZeroGamesIsFalse", func(t *testing.T) {
		if _, ok := PitcherPointsPerGame(fakePlainPitcherSource{proj: &PitcherProjection{G: 0}}, "A", "NYY", scoring); ok {
			t.Fatal("a zero-G projection must not divide into a value")
		}
	})
}

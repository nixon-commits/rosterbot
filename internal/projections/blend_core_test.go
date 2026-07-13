package projections

import "testing"

func TestBlendResult_NoProjectionNoRecent_ReturnsFalse(t *testing.T) {
	pts, ok := blendResult(false, 0, false, 0, 5.0, func() (float64, float64) { return 0.9, 0.1 })
	if ok {
		t.Errorf("expected false, got pts=%.4f", pts)
	}
}

func TestBlendResult_NoProjectionWithRecent_RegressesTowardBaseline(t *testing.T) {
	// Small-sample weight (90% base / 10% recent) should pull a hot raw rate
	// mostly down toward the baseline, not return it unshrunk.
	pts, ok := blendResult(false, 0, true, 20.0, 2.0, func() (float64, float64) { return 0.9, 0.1 })
	if !ok {
		t.Fatal("expected true")
	}
	want := 0.9*2.0 + 0.1*20.0 // 3.8
	if pts != want {
		t.Errorf("expected %.4f, got %.4f", want, pts)
	}
}

func TestBlendResult_HasProjectionNoRecent_ReturnsBase(t *testing.T) {
	pts, ok := blendResult(true, 4.5, false, 0, 2.0, func() (float64, float64) { return 0.5, 0.5 })
	if !ok {
		t.Fatal("expected true")
	}
	if pts != 4.5 {
		t.Errorf("expected 4.5 (pure base, weightFn unused), got %.4f", pts)
	}
}

func TestBlendResult_HasProjectionAndRecent_Blends(t *testing.T) {
	pts, ok := blendResult(true, 4.0, true, 6.0, 0, func() (float64, float64) { return 0.7, 0.3 })
	if !ok {
		t.Fatal("expected true")
	}
	want := 0.7*4.0 + 0.3*6.0 // 4.6
	if pts != want {
		t.Errorf("expected %.4f, got %.4f", want, pts)
	}
}

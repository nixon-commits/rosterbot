package backtest_test

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineuprun"
)

// TestStandardRotationSize_MatchesLineuprun pins backtest's duplicated rotation
// constant to the one the forecast actually divides by.
//
// The duplication is forced: internal/lineuprun imports internal/backtest (it
// writes the snapshots), so backtest cannot import lineuprun back. This test
// lives in the external test package, which can import both, and is the only
// thing standing between the two constants and a silent divergence — a
// rotation-supply figure computed against a different rotation depth than the
// GS forecast uses would disagree with the gate for no visible reason.
func TestStandardRotationSize_MatchesLineuprun(t *testing.T) {
	// A 5-man rotation over a 7-day week: 5 SPs supply 7 starts.
	got := backtest.RotationSupply{SPRosterDays: 5, DaysWithSnapshot: 1, GSCap: 7}
	supply, ok := got.SupplyPerWeek()
	if !ok {
		t.Fatal("SupplyPerWeek should be defined")
	}
	want := 5.0 * 7.0 / lineuprun.RotationSize
	if supply != want {
		t.Fatalf("SupplyPerWeek = %v, want %v — backtest's standardRotationSize has drifted "+
			"from lineuprun.RotationSize (%v)", supply, want, lineuprun.RotationSize)
	}
}

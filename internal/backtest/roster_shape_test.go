package backtest

import (
	"testing"
)

// TestPitcherIsDeployable_ExcludesRestDayStarters is the guard that keeps this
// metric from degenerating into a measurement of rotation cadence.
//
// SnapshotPlayer.ProjPtsPerGame for a pitcher is ScoredPitcher.ExpectedPts,
// which is UNDISCOUNTED — NonStarterSPDiscount (0.05x) is applied downstream,
// only to the local `generic` slice inside OptimizePitcherLineup, and never
// reaches the snapshot. HasGame means only that the pitcher's MLB team plays.
// So a HasGame-based denominator credits an ace his full ~16pt projection on
// every rest day, which against a five-man rotation is four days in five.
func TestPitcherIsDeployable_ExcludesRestDayStarters(t *testing.T) {
	restDayAce := SnapshotPlayer{
		Status: "Active", Role: "SP", IsPitcher: true,
		ProjPtsPerGame: 16.0, HasGame: true, IsStarter: false, GSSuppressed: false,
	}
	if pitcherIsDeployable(restDayAce) {
		t.Error("an SP whose team plays but who is not a probable starter must not count as deployable value")
	}
}

// TestPitcherIsDeployable_IncludesGateSuppressedStart pins that the gate's own
// suppressions land in the pitcher denominator. applyGSGate flips IsStarter to
// false on its way out, so IsStarter||GSSuppressed is what reconstructs the
// pre-gate probable-starter set. This is what makes the roster-shape block
// cohere with the GS-gate block printed directly above it.
func TestPitcherIsDeployable_IncludesGateSuppressedStart(t *testing.T) {
	suppressed := SnapshotPlayer{
		Status: "Active", Role: "SP", IsPitcher: true,
		ProjPtsPerGame: 11.0, HasGame: true, IsStarter: false, GSSuppressed: true,
	}
	if !pitcherIsDeployable(suppressed) {
		t.Error("a start the GS gate declined is owned value the roster could not field")
	}
}

func TestDeployability_TableCases(t *testing.T) {
	tests := []struct {
		name   string
		player SnapshotPlayer
		hitter bool // run through hitterIsDeployable rather than pitcherIsDeployable
		want   bool
	}{
		{
			name:   "hitter active with a game",
			player: SnapshotPlayer{Status: "Active", HasGame: true},
			hitter: true, want: true,
		},
		{
			name:   "hitter on the bench with a game is still owned value",
			player: SnapshotPlayer{Status: "Reserve", HasGame: true},
			hitter: true, want: true,
		},
		{
			name:   "hitter whose team is idle",
			player: SnapshotPlayer{Status: "Active", HasGame: false},
			hitter: true, want: false,
		},
		{
			name:   "injured hitter is not a roster shape",
			player: SnapshotPlayer{Status: "Injured Reserve", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "minor-league hitter cannot be fielded",
			player: SnapshotPlayer{Status: "Minors", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "pre-schema hitter has no status to judge",
			player: SnapshotPlayer{Status: "", HasGame: true},
			hitter: true, want: false,
		},
		{
			name:   "probable starter",
			player: SnapshotPlayer{Status: "Active", Role: "SP", IsStarter: true, HasGame: true},
			want:   true,
		},
		{
			name:   "reliever whose team plays",
			player: SnapshotPlayer{Status: "Reserve", Role: "RP", HasGame: true},
			want:   true,
		},
		{
			name:   "reliever whose team is idle",
			player: SnapshotPlayer{Status: "Active", Role: "RP", HasGame: false},
			want:   false,
		},
		{
			name:   "injured probable starter",
			player: SnapshotPlayer{Status: "Injured Reserve", Role: "SP", IsStarter: true, HasGame: true},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if tt.hitter {
				got = hitterIsDeployable(tt.player)
			} else {
				got = pitcherIsDeployable(tt.player)
			}
			if got != tt.want {
				t.Errorf("deployable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSideShape_StrandedAndRate(t *testing.T) {
	s := SideShape{OwnedPts: 200, FieldedPts: 150}
	if got := s.StrandedPts(); got != 50 {
		t.Errorf("StrandedPts = %v, want 50", got)
	}
	rate, ok := s.FieldedRate()
	if !ok {
		t.Fatal("FieldedRate should be defined when owned value is positive")
	}
	if rate != 0.75 {
		t.Errorf("FieldedRate = %v, want 0.75", rate)
	}
}

// TestSideShape_ZeroOwnedHasNoRate pins that an empty side reports "undefined"
// rather than 0%. A window in which nothing was deployable is not a window in
// which everything was stranded, and rendering it as 0% would read as a
// catastrophic roster failure.
func TestSideShape_ZeroOwnedHasNoRate(t *testing.T) {
	if _, ok := (SideShape{}).FieldedRate(); ok {
		t.Error("FieldedRate must be undefined when no deployable value exists")
	}
}

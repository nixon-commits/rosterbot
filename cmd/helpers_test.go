package cmd

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func TestColorDelta(t *testing.T) {
	tests := []struct {
		name    string
		delta   float64
		wantSub string // substring to find in output
	}{
		{"positive", 1.50, "\033[32m"},
		{"negative", -0.75, "\033[31m"},
		{"near-zero positive", 0.001, "\033[90m"},
		{"near-zero negative", -0.003, "\033[90m"},
		{"exact zero", 0.0, "\033[90m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorDelta(tt.delta)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("colorDelta(%.3f) = %q, want substring %q", tt.delta, got, tt.wantSub)
			}
		})
	}

	// All branches must produce the same byte length for column alignment.
	posLen := len(colorDelta(1.50))
	negLen := len(colorDelta(-0.75))
	zeroLen := len(colorDelta(0.0))
	if posLen != negLen || negLen != zeroLen {
		t.Errorf("byte lengths must match for alignment: pos=%d neg=%d zero=%d", posLen, negLen, zeroLen)
	}
}

func TestCombinedMovesDelta(t *testing.T) {
	ptsMap := map[string]float64{
		"emerson":  3.7,
		"campbell": 2.8,
		"benched1": 3.7,
		"benched2": 2.8,
		"hot":      6.0,
		"cold":     1.5,
	}

	tests := []struct {
		name     string
		activate []fantrax.PlayerSlot
		bench    []string
		want     float64
	}{
		{
			name: "zero net swap (Apr 26 case)",
			activate: []fantrax.PlayerSlot{
				{PlayerID: "emerson", PosID: "014"},
				{PlayerID: "campbell", PosID: "014"},
			},
			bench: []string{"benched1", "benched2"},
			want:  0.0,
		},
		{
			name: "positive net gain",
			activate: []fantrax.PlayerSlot{
				{PlayerID: "hot", PosID: "014"},
			},
			bench: []string{"cold"},
			want:  4.5,
		},
		{
			name:     "no moves",
			activate: nil,
			bench:    nil,
			want:     0.0,
		},
		{
			name: "missing pts default to zero",
			activate: []fantrax.PlayerSlot{
				{PlayerID: "unknown_id", PosID: "014"},
			},
			bench: nil,
			want:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := combinedMovesDelta(tt.activate, tt.bench, ptsMap)
			if got != tt.want {
				t.Errorf("combinedMovesDelta = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsZeroGainDelta(t *testing.T) {
	tests := []struct {
		name  string
		delta float64
		want  bool
	}{
		{"exact zero", 0.0, true},
		{"sub-eps positive", 1e-10, true},
		{"sub-eps negative", -1e-10, true},
		{"just above eps", 1e-8, false},
		{"clear gain", 0.5, false},
		{"clear loss", -0.5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isZeroGainDelta(tt.delta); got != tt.want {
				t.Errorf("isZeroGainDelta(%v) = %v, want %v", tt.delta, got, tt.want)
			}
		})
	}
}

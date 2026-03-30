package cmd

import (
	"strings"
	"testing"
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

package recap

import (
	"math"
	"testing"
	"time"
)

func TestLeagueDailySigma(t *testing.T) {
	// Three points: 10, 20, 30 → mean=20, sample var=100, σ=10
	days := []TeamDay{
		{Pts: 10}, {Pts: 20}, {Pts: 30},
	}
	got := LeagueDailySigma(days)
	if math.Abs(got-10.0) > 1e-9 {
		t.Errorf("LeagueDailySigma: want 10, got %.6f", got)
	}
}

func TestLeagueDailySigmaTooFew(t *testing.T) {
	if got := LeagueDailySigma(nil); got != 0 {
		t.Errorf("nil → want 0, got %.6f", got)
	}
	if got := LeagueDailySigma([]TeamDay{{Pts: 50}}); got != 0 {
		t.Errorf("len=1 → want 0, got %.6f", got)
	}
}

// silence unused import when later tests are added
var _ = time.Now

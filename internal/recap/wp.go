package recap

import (
	"hash/fnv"
	"math"
	"math/rand"
	"strconv"
)

// wpNumSims is the Monte Carlo iteration count per WP point. 5000 gives a
// standard error of ~0.007 at p=0.5 — invisible at chart resolution.
const wpNumSims = 5000

// LeagueDailySigma returns the sample standard deviation of daily team
// scores. Returns 0 for fewer than 2 points (caller should treat as
// "WP simulation unavailable" and skip the curve).
func LeagueDailySigma(days []TeamDay) float64 {
	n := len(days)
	if n < 2 {
		return 0
	}
	var sum float64
	for _, d := range days {
		sum += d.Pts
	}
	mean := sum / float64(n)
	var ss float64
	for _, d := range days {
		dev := d.Pts - mean
		ss += dev * dev
	}
	return math.Sqrt(ss / float64(n-1))
}

// wpRNG returns a deterministic *rand.Rand seeded from the matchup identity
// + week number, so every run produces identical curves.
func wpRNG(homeID, awayID string, week int) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(homeID))
	h.Write([]byte("|"))
	h.Write([]byte(awayID))
	h.Write([]byte("|"))
	h.Write([]byte(strconv.Itoa(week)))
	return rand.New(rand.NewSource(int64(h.Sum64())))
}

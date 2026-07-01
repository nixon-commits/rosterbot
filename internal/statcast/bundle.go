// Package statcast is the leaf owner of Baseball Savant data and the derived
// buy-low / hot Signals. It holds the Statcast Bundle (expected-stats and
// quality-of-contact slices joined by MLBAM ID) plus the signal-classification
// rules on top. waivers, claims, and recap depend on this package for the data
// and signals — not on each other's command package. Imports only internal/cache
// and internal/archive, so it stays a leaf.
package statcast

// HitterRow holds parsed data from the hitter expected-statistics CSV.
type HitterRow struct {
	MLBAMID int
	PA      int
	WOBA    float64
	XwOBA   float64
}

// HitterStatcastRow holds quality-of-contact metrics from the Statcast CSV.
type HitterStatcastRow struct {
	MLBAMID   int
	Barrel    float64 // percent (e.g. 12.4)
	HardHit   float64 // percent
	SweetSpot float64 // percent
}

// PitcherRow holds parsed data from the pitcher expected-statistics CSV.
type PitcherRow struct {
	MLBAMID int
	PA      int // TBF — total batters faced (the pitcher endpoint uses "pa")
	ERA     float64
	XERA    float64
	WOBA    float64
	XwOBA   float64
}

// Bundle aggregates every Savant data slice keyed by MLBAM ID — the Statcast
// Bundle. Any slice may be nil if its fetch failed; signal tagging handles
// missing data.
type Bundle struct {
	HitterExp     map[int]HitterRow
	HitterSC      map[int]HitterStatcastRow
	HitterExp14d  map[int]HitterRow
	PitcherExp    map[int]PitcherRow
	PitcherExp30d map[int]PitcherRow
}

package projections

import "strings"

// SourceEntry pairs one player's identity with a batting projection, for
// building a FanGraphsSource entirely in memory (fixture-backed tests, most
// prominently lineuprun's TestRun). The value it builds is the same concrete
// *FanGraphsSource the API and CSV loaders produce, keyed identically —
// projections by projKey(name, team), MLBAMIDs by NormalizeName — so lookup
// behavior cannot diverge from production.
type SourceEntry struct {
	Name    string
	Team    string // MLB club abbreviation, any case; normalized like the API builder
	MLBAMID int    // 0 = unknown, not recorded
	Proj    Projection
}

// PitcherSourceEntry is SourceEntry's pitcher-side twin.
type PitcherSourceEntry struct {
	Name    string
	Team    string
	MLBAMID int
	Proj    PitcherProjection
}

// NewFanGraphsSourceFromEntries builds a FanGraphsSource from in-memory
// entries. Entries with an empty name are skipped, matching the API and CSV
// builders.
func NewFanGraphsSourceFromEntries(entries []SourceEntry) *FanGraphsSource {
	src := &FanGraphsSource{
		projections: make(map[string]*Projection, len(entries)),
		mlbamIDs:    make(map[string]int, len(entries)),
	}
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		team := strings.ToUpper(strings.TrimSpace(e.Team))
		if name == "" {
			continue
		}
		p := e.Proj // copy, so the source never aliases the caller's struct
		src.projections[projKey(name, team)] = &p
		if e.MLBAMID > 0 {
			src.mlbamIDs[NormalizeName(name)] = e.MLBAMID
		}
	}
	return src
}

// NewFanGraphsPitcherSourceFromEntries builds a FanGraphsPitcherSource from
// in-memory entries. Entries with an empty name are skipped, matching the API
// and CSV builders.
func NewFanGraphsPitcherSourceFromEntries(entries []PitcherSourceEntry) *FanGraphsPitcherSource {
	src := &FanGraphsPitcherSource{
		projections: make(map[string]*PitcherProjection, len(entries)),
		mlbamIDs:    make(map[string]int, len(entries)),
	}
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		team := strings.ToUpper(strings.TrimSpace(e.Team))
		if name == "" {
			continue
		}
		p := e.Proj
		src.projections[projKey(name, team)] = &p
		if e.MLBAMID > 0 {
			src.mlbamIDs[NormalizeName(name)] = e.MLBAMID
		}
	}
	return src
}

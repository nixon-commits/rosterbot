package projections

// RollingSource provides a fallback projection based on a player's
// rolling recent stats. Stat values are stored as per-game rates
// and converted to season-equivalent totals for scoring.
//
// Season games played is assumed to be 162 so that per-game rates
// are comparable to FanGraphs season projections when divided by PA.
type RollingSource struct {
	// key: normalizeName(playerName)
	stats map[string]*Projection
}

// NewRollingSource creates an empty source. Use AddPlayer to populate it.
func NewRollingSource() *RollingSource {
	return &RollingSource{stats: make(map[string]*Projection)}
}

// AddPlayer records a player's rolling per-game rates.
// gamesPlayed is the window size; the rates are annualized to 162 games
// so they're on the same scale as FanGraphs season projections.
func (s *RollingSource) AddPlayer(
	name string,
	gamesPlayed int,
	h, doubles, triples, hr, rbi, r, bb, sb, cs, hbp float64,
) {
	if gamesPlayed <= 0 {
		return
	}
	scale := 162.0 / float64(gamesPlayed)
	s.stats[normalizeName(name)] = &Projection{
		PA:      float64(gamesPlayed) * 4.0 * scale, // rough 4 PA/game estimate
		H:       h * scale,
		Doubles: doubles * scale,
		Triples: triples * scale,
		HR:      hr * scale,
		RBI:     rbi * scale,
		R:       r * scale,
		BB:      bb * scale,
		SB:      sb * scale,
		CS:      cs * scale,
		HBP:     hbp * scale,
	}
}

// GetProjection implements Source.
func (s *RollingSource) GetProjection(name, _ string) (*Projection, bool) {
	p, ok := s.stats[normalizeName(name)]
	return p, ok
}

// ChainedSource tries sources in order, returning the first match.
type ChainedSource struct {
	sources []Source
}

// NewChainedSource returns a source that tries each delegate in order.
func NewChainedSource(sources ...Source) *ChainedSource {
	return &ChainedSource{sources: sources}
}

func (c *ChainedSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	for _, s := range c.sources {
		if p, ok := s.GetProjection(name, mlbTeam); ok {
			return p, true
		}
	}
	return nil, false
}

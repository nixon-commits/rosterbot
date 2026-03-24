package projections

import (
	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// ParkFactors holds per-stat park factor multipliers for a venue.
// Values are centered at 1.00 (league average). E.g., Coors HR = ~1.06.
type ParkFactors struct {
	Team string  // team abbreviation (canonical)
	HR   float64 // home run factor
	H    float64 // hits factor
	R    float64 // runs factor
	BB   float64 // walk factor
	SO   float64 // strikeout factor
	H1B  float64 // singles factor
	H2B  float64 // doubles factor
	H3B  float64 // triples factor
}

// ParkAdjustedSource wraps a projection source and applies park factor
// adjustments based on the venue each player is playing in today.
type ParkAdjustedSource struct {
	inner       Source
	innerPPS    PtsPerGameSource      // may be nil if inner doesn't implement PtsPerGameSource
	parkFactors map[string]ParkFactors // home team abbr → factors
	venues      map[string]string      // team abbr → home team abbr (today's games)
}

// NewParkAdjustedSource creates a park-adjusted wrapper.
// venues maps each team playing today to the home team of their game.
// parkFactors maps home team abbreviation to their park's factors.
func NewParkAdjustedSource(
	inner Source,
	parkFactors map[string]ParkFactors,
	venues map[string]string,
) *ParkAdjustedSource {
	pps, _ := inner.(PtsPerGameSource)
	return &ParkAdjustedSource{
		inner:       inner,
		innerPPS:    pps,
		parkFactors: parkFactors,
		venues:      venues,
	}
}

// GetProjection delegates to the inner source (unadjusted).
func (s *ParkAdjustedSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	return s.inner.GetProjection(name, mlbTeam)
}

// GetPtsPerGame returns park-adjusted points per game.
// If the player's team isn't playing today or park factors are unavailable,
// falls back to the unadjusted value.
func (s *ParkAdjustedSource) GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	// Get base projection for per-stat adjustment.
	proj, projOK := s.inner.GetProjection(name, mlbTeam)

	// Get blended pts/game from inner source.
	var basePts float64
	var ok bool
	if s.innerPPS != nil {
		basePts, ok = s.innerPPS.GetPtsPerGame(name, mlbTeam, scoring)
	}
	if !ok {
		if !projOK || proj.G <= 0 {
			return 0, false
		}
		basePts = ExpectedPtsFromProj(proj, scoring)
	}

	// Look up venue for this team.
	homeTeam, venueOK := s.venues[mlbTeam]
	if !venueOK {
		return basePts, true
	}

	pf, pfOK := s.parkFactors[homeTeam]
	if !pfOK {
		return basePts, true
	}

	// Apply per-stat park factor adjustment.
	if !projOK || proj.G <= 0 {
		// No projection breakdown available — apply aggregate runs factor.
		return basePts * pf.R, true
	}

	adjustment := computeParkAdjustment(proj, pf, scoring)
	return basePts * adjustment, true
}

// ParkFactor returns the park factor multiplier for a player's game today.
// Returns 1.0 if venue or park data is unavailable.
func (s *ParkAdjustedSource) ParkFactor(mlbTeam string) float64 {
	homeTeam, ok := s.venues[mlbTeam]
	if !ok {
		return 1.0
	}
	pf, ok := s.parkFactors[homeTeam]
	if !ok {
		return 1.0
	}
	return pf.R
}

// computeParkAdjustment calculates a weighted park factor multiplier based on
// how much each stat contributes to the player's total fantasy points.
func computeParkAdjustment(proj *Projection, pf ParkFactors, scoring fantrax.ScoringWeights) float64 {
	singles := proj.Singles
	if singles == 0 && proj.H > 0 {
		singles = proj.H - proj.Doubles - proj.Triples - proj.HR
	}
	xbh := proj.Doubles + proj.Triples + proj.HR
	tb := singles + 2*proj.Doubles + 3*proj.Triples + 4*proj.HR

	// Map each scoring category to its park factor.
	// Categories not directly park-affected (CS, GIDP, HBP, SB) use 1.0.
	statFactor := map[string]float64{
		"1B": pf.H1B, "2B": pf.H2B, "3B": pf.H3B,
		"HR": pf.HR, "R": pf.R, "RBI": pf.R,
		"BB": pf.BB, "SO": pf.SO,
		"SB": 1.0, "CS": 1.0, "HBP": 1.0, "GIDP": 1.0,
		"XBH": (pf.H2B + pf.H3B + pf.HR) / 3.0,
		"TB":  1.0,
	}

	// TB factor is a weighted blend of component hit type factors.
	if tb > 0 {
		statFactor["TB"] = (singles*pf.H1B + 2*proj.Doubles*pf.H2B + 3*proj.Triples*pf.H3B + 4*proj.HR*pf.HR) / tb
	}

	statMap := map[string]float64{
		"1B": singles, "2B": proj.Doubles, "3B": proj.Triples,
		"HR": proj.HR, "RBI": proj.RBI, "R": proj.R,
		"BB": proj.BB, "SB": proj.SB, "CS": proj.CS,
		"HBP": proj.HBP, "SO": proj.SO, "GIDP": proj.GIDP,
		"XBH": xbh, "TB": tb,
	}

	var neutralTotal, adjustedTotal float64
	for stat, seasonVal := range statMap {
		weight, ok := scoring[stat]
		if !ok {
			continue
		}
		perGame := (seasonVal / proj.G) * weight
		neutralTotal += perGame
		factor := statFactor[stat]
		if factor == 0 {
			factor = 1.0
		}
		adjustedTotal += perGame * factor
	}

	if neutralTotal == 0 {
		return 1.0
	}
	return adjustedTotal / neutralTotal
}

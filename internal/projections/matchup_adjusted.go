package projections

import (
	"math"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

const (
	unfavorablePlatoonMult = 0.93
	qualityMultMin         = 0.85
	qualityMultMax         = 1.15
	combinedMultMin        = 0.80
	combinedMultMax        = 1.15
)

// OpposingPitcher holds information about the starting pitcher a hitter will face.
type OpposingPitcher struct {
	Name   string
	Team   string
	Throws string  // "R" or "L"
	FIP    float64 // from Steamer projection
}

// MatchupAdjustedSource wraps a projection source and applies platoon split
// and opposing pitcher quality multipliers to hitter pts/game values.
type MatchupAdjustedSource struct {
	inner            Source
	innerPPS         PtsPerGameSource
	opposingPitchers map[string]OpposingPitcher // batting team abbr → opposing SP
	hitterBats       map[string]string          // NormalizeName(name) → "R"/"L"/"S"
	leagueAvgFIP     float64
}

// NewMatchupAdjustedSource creates a matchup-adjusted wrapper.
// opposingPitchers maps each batting team to the SP they face today.
// hitterBats maps normalized player name to handedness ("R", "L", or "S").
// leagueAvgFIP is the league-average FIP used to scale pitcher quality.
func NewMatchupAdjustedSource(
	inner Source,
	opposingPitchers map[string]OpposingPitcher,
	hitterBats map[string]string,
	leagueAvgFIP float64,
) *MatchupAdjustedSource {
	pps, _ := inner.(PtsPerGameSource)
	return &MatchupAdjustedSource{
		inner:            inner,
		innerPPS:         pps,
		opposingPitchers: opposingPitchers,
		hitterBats:       hitterBats,
		leagueAvgFIP:     leagueAvgFIP,
	}
}

// GetProjection delegates to the inner source (unadjusted).
func (s *MatchupAdjustedSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	return s.inner.GetProjection(name, mlbTeam)
}

// GetPtsPerGame returns matchup-adjusted points per game.
// Applies an unfavorable platoon penalty (0.93×) when the hitter bats same-side
// as the pitcher throws, and a quality multiplier based on pitcher FIP vs league average.
// The combined multiplier is clamped to [0.80, 1.15].
// Falls back gracefully when opposing pitcher, handedness, or FIP data is missing.
func (s *MatchupAdjustedSource) GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	var basePts float64
	var ok bool
	if s.innerPPS != nil {
		basePts, ok = s.innerPPS.GetPtsPerGame(name, mlbTeam, scoring)
	}
	if !ok {
		proj, projOK := s.inner.GetProjection(name, mlbTeam)
		if !projOK || proj.G <= 0 {
			return 0, false
		}
		basePts = ExpectedPtsFromProj(proj, scoring)
	}

	opp, oppOK := s.opposingPitchers[mlbTeam]
	if !oppOK {
		return basePts, true
	}

	// Platoon penalty: applies when hitter bats same-side as pitcher throws.
	// Switch hitters ("S") and unknown handedness never take the penalty.
	platoonMult := 1.0
	if bats, batsOK := s.hitterBats[NormalizeName(name)]; batsOK && opp.Throws != "" {
		if bats != "S" && bats == opp.Throws {
			platoonMult = unfavorablePlatoonMult
		}
	}

	// Quality multiplier: pitcher FIP / league avg FIP, clamped to [0.85, 1.15].
	// A lower FIP (better pitcher) suppresses hitter value; higher FIP boosts it.
	qualityMult := 1.0
	if s.leagueAvgFIP > 0 && opp.FIP > 0 {
		qualityMult = math.Max(qualityMultMin, math.Min(qualityMultMax, opp.FIP/s.leagueAvgFIP))
	}

	combined := math.Max(combinedMultMin, math.Min(combinedMultMax, platoonMult*qualityMult))
	return basePts * combined, true
}

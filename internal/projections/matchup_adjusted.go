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
	FIP    float64 // from projection system
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

// MatchupDetail holds the individual matchup multiplier components.
type MatchupDetail struct {
	PlatoonMult     float64 // 1.0 or unfavorablePlatoonMult
	QualityMult     float64 // FIP/leagueAvgFIP, clamped
	CombinedMult    float64 // clamped product
	Favorable       *bool   // nil=unknown, true=favorable platoon, false=unfavorable
	OpposingPitcher string
	OpposingFIP     float64
	LeagueAvgFIP    float64
}

// GetMatchupDetail returns the matchup adjustment components for a hitter.
func (s *MatchupAdjustedSource) GetMatchupDetail(name, mlbTeam string) MatchupDetail {
	d := MatchupDetail{PlatoonMult: 1.0, QualityMult: 1.0, CombinedMult: 1.0, LeagueAvgFIP: s.leagueAvgFIP}

	opp, oppOK := s.opposingPitchers[mlbTeam]
	if !oppOK {
		return d
	}
	d.OpposingPitcher = opp.Name
	d.OpposingFIP = opp.FIP

	if bats, batsOK := s.hitterBats[NormalizeName(name)]; batsOK && opp.Throws != "" {
		favorable := bats == "S" || bats != opp.Throws
		d.Favorable = &favorable
		if !favorable {
			d.PlatoonMult = unfavorablePlatoonMult
		}
	}

	if s.leagueAvgFIP > 0 && opp.FIP > 0 {
		d.QualityMult = math.Max(qualityMultMin, math.Min(qualityMultMax, opp.FIP/s.leagueAvgFIP))
	}

	d.CombinedMult = math.Max(combinedMultMin, math.Min(combinedMultMax, d.PlatoonMult*d.QualityMult))
	return d
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

// MatchupMultiplier returns the combined platoon + quality multiplier for a player.
// Returns 1.0 when no opposing pitcher data or handedness is available.
func (s *MatchupAdjustedSource) MatchupMultiplier(name, mlbTeam string) float64 {
	opp, oppOK := s.opposingPitchers[mlbTeam]
	if !oppOK {
		return 1.0
	}

	platoonMult := 1.0
	if bats, batsOK := s.hitterBats[NormalizeName(name)]; batsOK && opp.Throws != "" {
		if bats != "S" && bats == opp.Throws {
			platoonMult = unfavorablePlatoonMult
		}
	}

	qualityMult := 1.0
	if s.leagueAvgFIP > 0 && opp.FIP > 0 {
		qualityMult = math.Max(qualityMultMin, math.Min(qualityMultMax, opp.FIP/s.leagueAvgFIP))
	}

	return math.Max(combinedMultMin, math.Min(combinedMultMax, platoonMult*qualityMult))
}

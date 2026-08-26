package projections

import "github.com/nixon-commits/rosterbot/internal/fantrax"

// PointsPerGame is the single owner of the optional-capability discovery that
// four call sites used to hand-roll (the hitter optimizer's scoreRoster,
// MatchupAdjustedSource, and — via the pitcher twin below — the pitcher
// optimizer and lineuprun's pitcherProjectedPts, the GS forecast's pricing;
// that fourth copy went unnoticed until the pre-commit review of this very
// change, which is the drift this consolidation exists to end): if src also
// implements PtsPerGameSource its blended per-game
// value wins; a per-game miss falls THROUGH to the season projection, derived
// via ExpectedPtsFromProj. ok=false means src has no usable answer at all —
// no projection, or one with zero games.
//
// The known composition gap lives here too, in exactly one place now:
// ChainedSource does not implement PtsPerGameSource, so a blended source
// wrapped INSIDE a chain would lose its per-game path (today nothing does —
// production wraps the chain inside BlendedSource, and the backtest
// experiment's base variant legitimately has no blend). If that ever needs
// fixing, teach ChainedSource to delegate and every caller inherits it from
// this function unchanged.
func PointsPerGame(src Source, name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	if pps, ok := src.(PtsPerGameSource); ok {
		if pts, ok := pps.GetPtsPerGame(name, mlbTeam, scoring); ok {
			return pts, true
		}
	}
	proj, ok := src.GetProjection(name, mlbTeam)
	if !ok || proj.G <= 0 {
		return 0, false
	}
	return ExpectedPtsFromProj(proj, scoring), true
}

// PitcherPointsPerGame is PointsPerGame's pitcher twin, over PitcherSource /
// PitcherPtsPerGameSource.
func PitcherPointsPerGame(src PitcherSource, name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	if pps, ok := src.(PitcherPtsPerGameSource); ok {
		if pts, ok := pps.GetPitcherPtsPerGame(name, mlbTeam, scoring); ok {
			return pts, true
		}
	}
	proj, ok := src.GetPitcherProjection(name, mlbTeam)
	if !ok || proj.G <= 0 {
		return 0, false
	}
	return PitcherExpectedPtsFromProj(proj, scoring), true
}

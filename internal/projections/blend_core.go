package projections

// blendResult is the shared decision core for BlendedSource.GetPtsPerGame and
// PitcherBlendedSource.GetPitcherPtsPerGame (rosterbot-4lc). Both previously
// re-implemented the identical lookup/fallback/shrink shape independently,
// which meant the same bug (rosterbot-4h7) had to be fixed twice.
//
// weightFn returns the current base/recent split for a role-specific
// stabilization curve (hitters: PA-based; pitchers: role-aware GP-based).
//
// When there's no base projection, the recent rate is regressed toward
// baselineFPG (a league-average FP/G) using that same weight split, instead
// of being trusted unshrunk — a small hot/cold sample (e.g. a call-up's
// first 2 games) shouldn't be read as a stable rate (rosterbot-4h7).
func blendResult(
	hasProj bool,
	basePts float64,
	hasRecent bool,
	recentFPG float64,
	baselineFPG float64,
	weightFn func() (base, recent float64),
) (float64, bool) {
	if !hasProj {
		if hasRecent {
			sw, rw := weightFn()
			return sw*baselineFPG + rw*recentFPG, true
		}
		return 0, false
	}
	if !hasRecent {
		return basePts, true
	}
	sw, rw := weightFn()
	return sw*basePts + rw*recentFPG, true
}

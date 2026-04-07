package projections

import (
	"math"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
)

const (
	spStabilizationGP = 15.0 // 50/50 at 15 starts
	rpStabilizationGP = 25.0 // 50/50 at 25 appearances
	pitcherBaseFloor  = 0.35 // pitchers are more volatile, higher floor
)

// PitcherPtsPerGameSource can provide a pre-computed pitcher points-per-game value.
type PitcherPtsPerGameSource interface {
	GetPitcherPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool)
}

// PitcherBlendedSource wraps a pitcher projection source and blends its per-game
// value with recent Fantrax pitching data. Uses role-aware dynamic weights based
// on games played, with SP stabilization at 15 GP and RP at 25 GP.
type PitcherBlendedSource struct {
	inner     PitcherSource
	recent    map[string]fantrax.RecentStat
	scoring   fantrax.ScoringWeights
	nameToID  map[string]string   // NormalizeName(name) → player ID
	playerPos map[string][]string // player ID → position IDs
	minGP     int
}

func NewPitcherBlendedSource(
	inner PitcherSource,
	recent map[string]fantrax.RecentStat,
	scoring fantrax.ScoringWeights,
	nameToID map[string]string,
	playerPos map[string][]string,
	minGP int,
) *PitcherBlendedSource {
	return &PitcherBlendedSource{
		inner: inner, recent: recent, scoring: scoring,
		nameToID: nameToID, playerPos: playerPos, minGP: minGP,
	}
}

// GetPitcherProjection delegates to the inner source.
func (b *PitcherBlendedSource) GetPitcherProjection(name, mlbTeam string) (*PitcherProjection, bool) {
	return b.inner.GetPitcherProjection(name, mlbTeam)
}

// GetPitcherPtsPerGame returns blended FP/G with role-aware dynamic weights.
// Falls back to 100% base projection if no recent data or insufficient games.
func (b *PitcherBlendedSource) GetPitcherPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	proj, ok := b.inner.GetPitcherProjection(name, mlbTeam)
	if !ok || proj.G <= 0 {
		return 0, false
	}

	basePts := PitcherExpectedPtsFromProj(proj, scoring)

	playerID, idOK := b.nameToID[NormalizeName(name)]
	if !idOK {
		return basePts, true
	}

	recent, statOK := b.recent[playerID]
	if !statOK || recent.GamesPlayed < b.minGP {
		return basePts, true
	}

	recentPtsPerGame := recent.FPtsPerGame

	// Determine role from position eligibility, then compute dynamic weights.
	isSP := isSPEligible(b.playerPos[playerID])
	sw, rw := pitcherBlendWeights(recent.GamesPlayed, isSP)

	return sw*basePts + rw*recentPtsPerGame, true
}

// pitcherBlendWeights computes dynamic base/recent weights based on games played and role.
func pitcherBlendWeights(gamesPlayed int, isSP bool) (base, season float64) {
	stabilization := rpStabilizationGP
	if isSP {
		stabilization = spStabilizationGP
	}
	gp := float64(gamesPlayed)
	seasonWeight := gp / (gp + stabilization)
	base = math.Max(1-seasonWeight, pitcherBaseFloor)
	season = 1 - base
	return
}

// PitcherBlendWeightsForDisplay returns the base/season weight percentages for display.
func PitcherBlendWeightsForDisplay(gamesPlayed int, isSP bool) (basePct, seasonPct float64) {
	return pitcherBlendWeights(gamesPlayed, isSP)
}

// isSPEligible returns true if the player has SP position eligibility.
func isSPEligible(positions []string) bool {
	for _, pos := range positions {
		if pos == auth_client.PosSP { // "015"
			return true
		}
	}
	return false
}

// PitcherBreakdown holds the blending components for a pitcher, used by the pipeline display.
type PitcherBreakdown struct {
	BasePts     float64
	RecentFPG   float64
	BaseWt      float64
	RecentWt    float64
	BlendedPts  float64
	GamesPlayed int
	HasRecent   bool
	IsSP        bool
}

// GetPitcherBreakdown returns the blending components for a pitcher.
// Returns nil if the pitcher has no projection from the active system.
func (b *PitcherBlendedSource) GetPitcherBreakdown(name, mlbTeam string, scoring fantrax.ScoringWeights) *PitcherBreakdown {
	proj, ok := b.inner.GetPitcherProjection(name, mlbTeam)
	if !ok || proj.G <= 0 {
		return nil
	}

	basePts := PitcherExpectedPtsFromProj(proj, scoring)
	playerID, idOK := b.nameToID[NormalizeName(name)]
	isSP := false
	if idOK {
		isSP = isSPEligible(b.playerPos[playerID])
	}

	bd := &PitcherBreakdown{
		BasePts:    basePts,
		BaseWt:     1.0,
		BlendedPts: basePts,
		IsSP:       isSP,
	}

	if !idOK {
		return bd
	}

	recent, statOK := b.recent[playerID]
	if !statOK || recent.GamesPlayed < b.minGP {
		return bd
	}

	bd.HasRecent = true
	bd.GamesPlayed = recent.GamesPlayed
	bd.RecentFPG = recent.FPtsPerGame
	bd.BaseWt, bd.RecentWt = pitcherBlendWeights(recent.GamesPlayed, isSP)
	bd.BlendedPts = bd.BaseWt*basePts + bd.RecentWt*bd.RecentFPG
	return bd
}

// PitcherExpectedPtsFromProj computes per-game fantasy points from a pitcher projection.
func PitcherExpectedPtsFromProj(proj *PitcherProjection, scoring fantrax.ScoringWeights) float64 {
	if proj.G <= 0 {
		return 0
	}

	statMap := map[string]float64{
		"K":   proj.K,
		"BB":  proj.BBA,
		"H":   proj.HA,
		"ER":  proj.ER,
		"HR":  proj.HRA,
		"W":   proj.W,
		"L":   proj.L,
		"QS":  proj.QS,
		"SV":  proj.SV,
		"HLD": proj.HLD,
		"BS":  proj.BS,
		"IP":  proj.IP,
		"HBP": proj.HBP,
		"WP":  proj.WP,
		"BK":  proj.BK,
		"CG":  proj.CG,
		"SHO": proj.SHO,
		"PKO": proj.PKO,
	}

	var total float64
	for stat, seasonVal := range statMap {
		if pts, ok := scoring[stat]; ok {
			perGame := seasonVal / proj.G
			total += perGame * pts
		}
	}
	return total
}

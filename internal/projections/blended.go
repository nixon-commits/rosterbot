package projections

import (
	"math"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

const (
	hitterStabilizationPA = 250.0 // 50/50 at ~66 GP (roughly mid-June)
	hitterPAPerGame       = 3.8   // approximate PA per game played
	hitterSteamerFloor    = 0.30  // Steamer never drops below 30%
)

// PtsPerGameSource can provide a pre-computed points-per-game value.
type PtsPerGameSource interface {
	GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool)
}

// BlendedSource wraps a projection source and blends its per-game value
// with recent Fantrax scoring data.
type BlendedSource struct {
	inner    Source
	recent   map[string]fantrax.RecentStat
	scoring  fantrax.ScoringWeights
	nameToID map[string]string // NormalizeName(name) → player ID
	minGP    int
}

func NewBlendedSource(
	inner Source,
	recent map[string]fantrax.RecentStat,
	scoring fantrax.ScoringWeights,
	nameToID map[string]string,
	minGP int,
) *BlendedSource {
	return &BlendedSource{inner: inner, recent: recent, scoring: scoring, nameToID: nameToID, minGP: minGP}
}

// GetProjection delegates to the inner source.
func (b *BlendedSource) GetProjection(name, mlbTeam string) (*Projection, bool) {
	return b.inner.GetProjection(name, mlbTeam)
}

// GetPtsPerGame returns blended FP/G using PA-based dynamic weights.
// Falls back to 100% Steamer if no recent data. Returns false if no Steamer projection.
func (b *BlendedSource) GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool) {
	proj, ok := b.inner.GetProjection(name, mlbTeam)
	if !ok || proj.G <= 0 {
		return 0, false
	}

	steamerPts := ExpectedPtsFromProj(proj, scoring)

	playerID, idOK := b.nameToID[NormalizeName(name)]
	if !idOK {
		return steamerPts, true
	}

	recent, statOK := b.recent[playerID]
	if !statOK || recent.GamesPlayed < b.minGP {
		return steamerPts, true
	}

	recentPtsPerGame := recent.FPtsPerGame
	sw, rw := hitterBlendWeights(recent.GamesPlayed)
	return sw*steamerPts + rw*recentPtsPerGame, true
}

// hitterBlendWeights computes dynamic Steamer/recent weights based on games played.
func hitterBlendWeights(gamesPlayed int) (steamer, season float64) {
	approxPA := float64(gamesPlayed) * hitterPAPerGame
	seasonWeight := approxPA / (approxPA + hitterStabilizationPA)
	steamer = math.Max(1-seasonWeight, hitterSteamerFloor)
	season = 1 - steamer
	return
}

// HitterBlendWeightsForDisplay returns the Steamer/season weight percentages for display.
func HitterBlendWeightsForDisplay(gamesPlayed int) (steamerPct, seasonPct float64) {
	return hitterBlendWeights(gamesPlayed)
}

// HitterBreakdown holds the individual components of a blended hitter score.
type HitterBreakdown struct {
	SteamerPts  float64
	RecentFPG   float64
	SteamerWt   float64
	RecentWt    float64
	BlendedPts  float64
	GamesPlayed int
	HasRecent   bool // true if recent data was used in blending
}

// GetHitterBreakdown returns the blending components for a player.
// Returns nil if the player has no Steamer projection.
func (b *BlendedSource) GetHitterBreakdown(name, mlbTeam string, scoring fantrax.ScoringWeights) *HitterBreakdown {
	proj, ok := b.inner.GetProjection(name, mlbTeam)
	if !ok || proj.G <= 0 {
		return nil
	}

	steamerPts := ExpectedPtsFromProj(proj, scoring)
	bd := &HitterBreakdown{
		SteamerPts: steamerPts,
		SteamerWt:  1.0,
		BlendedPts: steamerPts,
	}

	playerID, idOK := b.nameToID[NormalizeName(name)]
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
	bd.SteamerWt, bd.RecentWt = hitterBlendWeights(recent.GamesPlayed)
	bd.BlendedPts = bd.SteamerWt*steamerPts + bd.RecentWt*bd.RecentFPG
	return bd
}

// ExpectedPtsFromProj computes per-game fantasy points from a projection.
// This is the canonical implementation; optimizer.expectedPts delegates here.
func ExpectedPtsFromProj(proj *Projection, scoring fantrax.ScoringWeights) float64 {
	if proj.G <= 0 {
		return 0
	}
	singles := proj.Singles
	if singles == 0 && proj.H > 0 {
		singles = proj.H - proj.Doubles - proj.Triples - proj.HR
	}
	xbh := proj.Doubles + proj.Triples + proj.HR
	tb := singles + 2*proj.Doubles + 3*proj.Triples + 4*proj.HR

	statMap := map[string]float64{
		"1B": singles, "2B": proj.Doubles, "3B": proj.Triples,
		"HR": proj.HR, "RBI": proj.RBI, "R": proj.R,
		"BB": proj.BB, "SB": proj.SB, "CS": proj.CS,
		"HBP": proj.HBP, "SO": proj.SO, "GIDP": proj.GIDP,
		"XBH": xbh, "TB": tb,
	}

	var total float64
	for stat, seasonVal := range statMap {
		if pts, ok := scoring[stat]; ok {
			total += (seasonVal / proj.G) * pts
		}
	}
	return total
}

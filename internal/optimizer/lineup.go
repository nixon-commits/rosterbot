package optimizer

import (
	"math"
	"sort"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
)

// ScoredPlayer pairs a player with their expected fantasy points per game.
type ScoredPlayer struct {
	Player      fantrax.Player
	ExpectedPts float64
	HasGame     bool
}

// Result describes the lineup changes the optimizer wants to make.
type Result struct {
	ToActivate []fantrax.PlayerSlot
	ToBench    []string // player IDs to move to reserve
	Scored     []ScoredPlayer
}

// OptimizeLineup computes the optimal daily hitter lineup.
func OptimizeLineup(
	roster []fantrax.Player,
	playingToday map[string]bool,
	projSrc projections.Source,
	scoring fantrax.ScoringWeights,
	slots []fantrax.Slot,
) Result {
	scored := scoreRoster(roster, playingToday, projSrc, scoring)

	// Sort for display (hasGame first, then by pts desc).
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].HasGame != scored[j].HasGame {
			return scored[i].HasGame
		}
		return scored[i].ExpectedPts > scored[j].ExpectedPts
	})

	// Use backtracking to find the assignment that maximizes total points.
	toActivate := optimalAssignment(scored, slots)

	// Build set of players in the optimal lineup.
	assigned := make(map[string]bool)
	for _, ps := range toActivate {
		assigned[ps.PlayerID] = true
	}

	// Build current assignment map: playerID → posID.
	current := make(map[string]string)
	for _, p := range roster {
		if p.Status == "Active" && p.RosterPosition != "" {
			current[p.ID] = p.RosterPosition
		}
	}

	// Only emit changes: activations where player isn't already in that slot.
	var changedActivate []fantrax.PlayerSlot
	for _, ps := range toActivate {
		if current[ps.PlayerID] != ps.PosID {
			changedActivate = append(changedActivate, ps)
		}
	}

	// Bench players who are currently active but not in the optimal lineup.
	var toBench []string
	for _, p := range roster {
		if p.Status == "Active" && !assigned[p.ID] {
			toBench = append(toBench, p.ID)
		}
	}

	return Result{
		ToActivate: changedActivate,
		ToBench:    toBench,
		Scored:     scored,
	}
}

// effectivePts returns the points a player contributes to the lineup.
// Players without a game today contribute 0 regardless of projection.
func effectivePts(sp ScoredPlayer) float64 {
	if !sp.HasGame {
		return 0
	}
	return sp.ExpectedPts
}

// optimalAssignment uses backtracking to find the slot assignment
// that maximizes total effective points across all slots.
func optimalAssignment(scored []ScoredPlayer, slots []fantrax.Slot) []fantrax.PlayerSlot {
	bestTotal := math.Inf(-1)
	var bestAssign []fantrax.PlayerSlot

	current := make([]fantrax.PlayerSlot, len(slots))
	used := make(map[int]bool) // index into scored

	// upperBound computes the max additional pts possible from remaining slots,
	// assuming each gets the best available unused player (ignoring eligibility).
	upperBound := func(slotIdx int, total float64) float64 {
		bound := total
		remaining := 0
		for i := slotIdx; i < len(slots); i++ {
			remaining++
		}
		avail := make([]float64, 0, remaining)
		for i, sp := range scored {
			if !used[i] {
				avail = append(avail, effectivePts(sp))
			}
			if len(avail) >= remaining {
				break
			}
		}
		// scored is sorted by hasGame+pts desc, so avail is already in desc order.
		for _, v := range avail {
			bound += v
		}
		return bound
	}

	var search func(slotIdx int, total float64)
	search = func(slotIdx int, total float64) {
		if slotIdx == len(slots) {
			if total > bestTotal {
				bestTotal = total
				bestAssign = make([]fantrax.PlayerSlot, len(current))
				copy(bestAssign, current)
			}
			return
		}

		// Prune: even the best-case remaining can't beat current best.
		if upperBound(slotIdx, total) <= bestTotal {
			return
		}

		slot := slots[slotIdx]
		filled := false
		for i, sp := range scored {
			if used[i] {
				continue
			}
			if !fantrax.EligibleForSlot(sp.Player.Positions, slot) {
				continue
			}
			used[i] = true
			current[slotIdx] = fantrax.PlayerSlot{
				PlayerID: sp.Player.ID,
				PosID:    slot.PosID,
			}
			search(slotIdx+1, total+effectivePts(sp))
			used[i] = false
			filled = true
		}

		// Allow leaving a slot empty if no eligible player found.
		if !filled {
			current[slotIdx] = fantrax.PlayerSlot{}
			search(slotIdx+1, total)
		}
	}

	search(0, 0)
	// Filter out empty assignments.
	var result []fantrax.PlayerSlot
	for _, ps := range bestAssign {
		if ps.PlayerID != "" {
			result = append(result, ps)
		}
	}
	return result
}

func scoreRoster(
	roster []fantrax.Player,
	playingToday map[string]bool,
	projSrc projections.Source,
	scoring fantrax.ScoringWeights,
) []ScoredPlayer {
	pps, hasPPS := projSrc.(projections.PtsPerGameSource)

	scored := make([]ScoredPlayer, 0, len(roster))
	for _, p := range roster {
		hasGame := playingToday[p.MLBTeam] && p.Status != "Injured Reserve" && p.Status != "Minors"
		var pts float64
		found := false

		if hasPPS {
			if blended, ok := pps.GetPtsPerGame(p.Name, p.MLBTeam, scoring); ok {
				pts = blended
				found = true
			}
		}

		if !found {
			proj, ok := projSrc.GetProjection(p.Name, p.MLBTeam)
			if ok && proj.G > 0 {
				pts = expectedPts(proj, scoring)
			}
		}

		scored = append(scored, ScoredPlayer{
			Player:      p,
			ExpectedPts: pts,
			HasGame:     hasGame,
		})
	}
	return scored
}

// expectedPts converts a season projection to expected fantasy points per game.
// Handles derived stats: 1B (if not projected directly), XBH, TB.
func expectedPts(proj *projections.Projection, scoring fantrax.ScoringWeights) float64 {
	if proj.G <= 0 {
		return 0
	}

	// Derive stats that may not be directly in the projection.
	singles := proj.Singles
	if singles == 0 && proj.H > 0 {
		singles = proj.H - proj.Doubles - proj.Triples - proj.HR
	}
	xbh := proj.Doubles + proj.Triples + proj.HR
	tb := singles + 2*proj.Doubles + 3*proj.Triples + 4*proj.HR

	statMap := map[string]float64{
		"1B":   singles,
		"2B":   proj.Doubles,
		"3B":   proj.Triples,
		"HR":   proj.HR,
		"RBI":  proj.RBI,
		"R":    proj.R,
		"BB":   proj.BB,
		"SB":   proj.SB,
		"CS":   proj.CS,
		"HBP":  proj.HBP,
		"SO":   proj.SO,
		"GIDP": proj.GIDP,
		"XBH":  xbh,
		"TB":   tb,
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

package optimizer

import (
	"math"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
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
// benchedToday contains normalized player names confirmed out of their team's
// real-life starting lineup. These players are treated as having no game.
// Pass nil or an empty map to disable (all players on playing teams get HasGame).
func OptimizeLineup(
	roster []fantrax.Player,
	playingToday map[string]bool,
	projSrc projections.Source,
	scoring fantrax.ScoringWeights,
	slots []fantrax.Slot,
	benchedToday map[string]bool,
) Result {
	scored := scoreRoster(roster, playingToday, projSrc, scoring, benchedToday)

	// Sort for display and backtracking (hasGame first, then by pts desc, then by ID for stability).
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].HasGame != scored[j].HasGame {
			return scored[i].HasGame
		}
		if scored[i].ExpectedPts != scored[j].ExpectedPts {
			return scored[i].ExpectedPts > scored[j].ExpectedPts
		}
		return scored[i].Player.ID < scored[j].Player.ID
	})

	// Build current assignment map: playerID → posID.
	currentAssign := make(map[string]string)
	for _, p := range roster {
		if p.Status == "Active" && p.RosterPosition != "" {
			currentAssign[p.ID] = p.RosterPosition
		}
	}

	// Partition locked active players: they keep their current slot.
	// Remove their slots from available pool so the optimizer works around them.
	availableSlots, lockedAssign := partitionLockedSlots(scored, slots, currentAssign)

	// Exclude locked players from optimizer candidates.
	var unlocked []ScoredPlayer
	for _, sp := range scored {
		if !sp.Player.Locked {
			unlocked = append(unlocked, sp)
		}
	}

	// Use backtracking to find the assignment that maximizes total points.
	toActivate := optimalAssignment(unlocked, availableSlots, currentAssign, fantrax.EligibleForSlot)

	// Merge locked assignments back in.
	toActivate = append(toActivate, lockedAssign...)

	// Build set of players in the optimal lineup.
	assigned := make(map[string]bool)
	for _, ps := range toActivate {
		assigned[ps.PlayerID] = true
	}

	// Only emit changes: activations where player isn't already in that slot.
	// Never emit locked players.
	var changedActivate []fantrax.PlayerSlot
	for _, ps := range toActivate {
		if currentAssign[ps.PlayerID] != ps.PosID {
			changedActivate = append(changedActivate, ps)
		}
	}

	// Bench players who are currently active but not in the optimal lineup.
	// Never bench locked players.
	var toBench []string
	for _, p := range roster {
		if p.Status == "Active" && !assigned[p.ID] && !p.Locked {
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
// When two assignments have the same score, it prefers the one with
// fewer changes from currentAssign (playerID → posID) for stability.
func optimalAssignment(scored []ScoredPlayer, slots []fantrax.Slot, currentAssign map[string]string, eligFunc func([]string, fantrax.Slot) bool) []fantrax.PlayerSlot {
	bestTotal := math.Inf(-1)
	bestChanges := math.MaxInt
	var bestAssign []fantrax.PlayerSlot

	candidate := make([]fantrax.PlayerSlot, len(slots))
	used := make(map[int]bool) // index into scored

	// upperBound computes the max additional pts possible from remaining slots,
	// assuming each gets the best available unused player (ignoring eligibility).
	upperBound := func(slotIdx int, total float64) float64 {
		bound := total
		remaining := len(slots) - slotIdx
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

	const eps = 1e-9

	var search func(slotIdx int, total float64, changes int)
	search = func(slotIdx int, total float64, changes int) {
		if slotIdx == len(slots) {
			better := total > bestTotal+eps
			tied := math.Abs(total-bestTotal) <= eps
			if better || (tied && changes < bestChanges) {
				bestTotal = total
				bestChanges = changes
				bestAssign = make([]fantrax.PlayerSlot, len(candidate))
				copy(bestAssign, candidate)
			}
			return
		}

		// Prune: even the best-case remaining can't beat current best.
		if upperBound(slotIdx, total) < bestTotal-eps {
			return
		}

		// Prune: score is tied with best but we already have as many or more changes.
		ub := upperBound(slotIdx, total)
		if math.Abs(ub-bestTotal) <= eps && changes >= bestChanges {
			return
		}

		slot := slots[slotIdx]
		filled := false
		for i, sp := range scored {
			if used[i] {
				continue
			}
			if !eligFunc(sp.Player.Positions, slot) {
				continue
			}
			used[i] = true
			candidate[slotIdx] = fantrax.PlayerSlot{
				PlayerID: sp.Player.ID,
				PosID:    slot.PosID,
			}
			isChange := 0
			if currentAssign[sp.Player.ID] != slot.PosID {
				isChange = 1
			}
			search(slotIdx+1, total+effectivePts(sp), changes+isChange)
			used[i] = false
			filled = true
		}

		// Allow leaving a slot empty if no eligible player found.
		if !filled {
			candidate[slotIdx] = fantrax.PlayerSlot{}
			search(slotIdx+1, total, changes)
		}
	}

	search(0, 0, 0)
	// Filter out empty assignments.
	var result []fantrax.PlayerSlot
	for _, ps := range bestAssign {
		if ps.PlayerID != "" {
			result = append(result, ps)
		}
	}
	return result
}

// partitionLockedSlots separates locked active players from the optimization.
// Locked players keep their current slot; those slots are removed from the
// available pool so the optimizer only considers movable players and open slots.
func partitionLockedSlots(scored []ScoredPlayer, slots []fantrax.Slot, currentAssign map[string]string) (available []fantrax.Slot, locked []fantrax.PlayerSlot) {
	// Count how many slots each posID type is consumed by locked players.
	consumed := make(map[string]int)
	for _, sp := range scored {
		if !sp.Player.Locked {
			continue
		}
		posID, ok := currentAssign[sp.Player.ID]
		if !ok {
			continue // locked bench player — stays on bench, no slot consumed
		}
		locked = append(locked, fantrax.PlayerSlot{
			PlayerID: sp.Player.ID,
			PosID:    posID,
		})
		consumed[posID]++
	}

	// Remove consumed slots from the pool (slots can repeat, e.g. 4x OF).
	for _, s := range slots {
		if consumed[s.PosID] > 0 {
			consumed[s.PosID]--
			continue
		}
		available = append(available, s)
	}
	return available, locked
}

func scoreRoster(
	roster []fantrax.Player,
	playingToday map[string]bool,
	projSrc projections.Source,
	scoring fantrax.ScoringWeights,
	benchedToday map[string]bool,
) []ScoredPlayer {
	pps, hasPPS := projSrc.(projections.PtsPerGameSource)

	scored := make([]ScoredPlayer, 0, len(roster))
	for _, p := range roster {
		hasGame := playingToday[p.MLBTeam] && !p.InMinors && !p.IsInjured && !benchedToday[projections.NormalizeName(p.Name)]
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
func expectedPts(proj *projections.Projection, scoring fantrax.ScoringWeights) float64 {
	return projections.ExpectedPtsFromProj(proj, scoring)
}

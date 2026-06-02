package optimizer

import (
	"sort"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/pmurley/go-fantrax/auth_client"
)

// ScoredPitcher pairs a pitcher with their expected fantasy points per game.
type ScoredPitcher struct {
	Player      fantrax.Player
	ExpectedPts float64
	HasGame     bool
	IsStarter   bool // true if SP-eligible and listed as probable starter
}

// PitcherResult describes the pitcher lineup changes the optimizer wants to make.
type PitcherResult struct {
	ToActivate []fantrax.PlayerSlot
	ToBench    []string // player IDs to move to reserve
	Scored     []ScoredPitcher
}

// OptimizePitcherLineup computes the optimal daily pitcher lineup.
// probableStarters maps normalized pitcher name → team abbreviation.
// An empty probableStarters map means no data available (future date/TBD);
// in that case SPs default to "has game" if their team plays.
// gsBudget is optional (nil = no GS limit); when set, the gate suppresses
// low-value SP starts to conserve weekly game-start budget.
func OptimizePitcherLineup(
	roster []fantrax.Player,
	playingToday map[string]bool,
	probableStarters map[string]string,
	projSrc projections.PitcherSource,
	scoring fantrax.ScoringWeights,
	slots []fantrax.Slot,
	gsBudget *GSBudget,
) PitcherResult {
	scored := scorePitcherRoster(roster, playingToday, probableStarters, projSrc, scoring)
	scored = applyGSGate(scored, gsBudget)

	// Sort: hasGame first, then by pts desc, then by ID for stability.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].HasGame != scored[j].HasGame {
			return scored[i].HasGame
		}
		if scored[i].ExpectedPts != scored[j].ExpectedPts {
			return scored[i].ExpectedPts > scored[j].ExpectedPts
		}
		return scored[i].Player.ID < scored[j].Player.ID
	})

	// Convert to ScoredPlayer for slot assignment.
	// Non-starting SPs whose team plays are eligible but unlikely to
	// pitch a full game. Discount their value to 10% so RPs and probable
	// starters are preferred, while non-starters still fill empty slots.
	var generic []ScoredPlayer
	for _, sp := range scored {
		if !sp.HasGame {
			continue
		}
		pts := sp.ExpectedPts
		if !sp.IsStarter && (isSPEligible(sp.Player.Positions) || strings.Contains(sp.Player.PosShortNames, "SP")) {
			pts *= 0.10
		}
		generic = append(generic, ScoredPlayer{
			Player:      sp.Player,
			ExpectedPts: pts,
			HasGame:     sp.HasGame,
		})
	}

	// Sort by effective (discounted) pts desc. optimalAssignment's upper-bound
	// pruning assumes the input is in descending effective-pts order; without
	// this re-sort, discounted SPs appear before full-value RPs and the pruner
	// underestimates reachable scores, skipping the optimal branch.
	sort.Slice(generic, func(i, j int) bool {
		if generic[i].ExpectedPts != generic[j].ExpectedPts {
			return generic[i].ExpectedPts > generic[j].ExpectedPts
		}
		return generic[i].Player.ID < generic[j].Player.ID
	})

	// Build current assignment map.
	currentAssign := make(map[string]string)
	for _, p := range roster {
		if p.Status == "Active" && p.RosterPosition != "" {
			currentAssign[p.ID] = p.RosterPosition
		}
	}

	// Partition locked active players: they keep their current slot.
	availableSlots, lockedAssign := partitionLockedSlots(generic, slots, currentAssign)

	// Exclude locked players from optimizer candidates.
	var unlocked []ScoredPlayer
	for _, sp := range generic {
		if !sp.Player.Locked {
			unlocked = append(unlocked, sp)
		}
	}

	toActivate := optimalAssignment(unlocked, availableSlots, currentAssign, fantrax.EligibleForPitcherSlot)

	// Merge locked assignments back in.
	toActivate = append(toActivate, lockedAssign...)

	// Build set of players in the optimal lineup.
	assigned := make(map[string]bool)
	for _, ps := range toActivate {
		assigned[ps.PlayerID] = true
	}

	// Fill remaining empty slots with currently-active non-playing pitchers.
	// There's no benefit to benching a pitcher whose team doesn't play —
	// leaving them in an unused slot avoids unnecessary roster churn.
	filledSlots := make(map[string]int) // posID → count of assigned players
	for _, ps := range toActivate {
		filledSlots[ps.PosID]++
	}
	slotCap := make(map[string]int) // posID → total slot count
	for _, s := range slots {
		slotCap[s.PosID]++
	}
	for posID, cap := range slotCap {
		slotCap[posID] = cap - filledSlots[posID]
	}
	// Keep active non-playing pitchers in their current slot if there's room.
	for _, p := range roster {
		if p.Status != "Active" || assigned[p.ID] || p.Locked {
			continue
		}
		posID, ok := currentAssign[p.ID]
		if !ok {
			continue
		}
		if slotCap[posID] > 0 {
			assigned[p.ID] = true
			slotCap[posID]--
		}
	}

	// Only emit changes. Never emit locked players.
	var changedActivate []fantrax.PlayerSlot
	for _, ps := range toActivate {
		if currentAssign[ps.PlayerID] != ps.PosID {
			changedActivate = append(changedActivate, ps)
		}
	}

	// Bench pitchers who are currently active but not in the optimal lineup
	// and couldn't be retained in an empty slot. Never bench locked players.
	var toBench []string
	for _, p := range roster {
		if p.Status == "Active" && !assigned[p.ID] && !p.Locked {
			toBench = append(toBench, p.ID)
		}
	}

	return PitcherResult{
		ToActivate: changedActivate,
		ToBench:    toBench,
		Scored:     scored,
	}
}

func scorePitcherRoster(
	roster []fantrax.Player,
	playingToday map[string]bool,
	probableStarters map[string]string,
	projSrc projections.PitcherSource,
	scoring fantrax.ScoringWeights,
) []ScoredPitcher {
	pps, hasPPS := projSrc.(projections.PitcherPtsPerGameSource)
	hasProbableData := len(probableStarters) > 0

	scored := make([]ScoredPitcher, 0, len(roster))
	for _, p := range roster {
		if p.InMinors || p.IsInjured {
			scored = append(scored, ScoredPitcher{Player: p})
			continue
		}

		teamPlays := playingToday[p.MLBTeam]
		normalizedName := projections.NormalizeName(p.Name)

		// Determine SP eligibility from PosShortNames (e.g. "SP", "RP").
		// Position IDs may only contain generic "P" ("017") in leagues
		// that use a single P slot, so PosShortNames is the reliable source.
		spEligible := isSPEligible(p.Positions) || strings.Contains(p.PosShortNames, "SP")

		// Determine hasGame based on role.
		var hasGame, isStarter bool

		if spEligible {
			if hasProbableData {
				if team, ok := probableStarters[normalizedName]; ok && team == p.MLBTeam {
					// SP listed as probable starter: full value.
					hasGame = true
					isStarter = true
				} else {
					// SP not starting: still eligible if team plays (could
					// enter in relief) but lower priority than RPs/starters.
					hasGame = teamPlays
				}
			} else {
				// No probable data (future date/TBD): SP has a game if team plays
				// but is NOT marked as a confirmed starter. The 0.10x non-starter
				// discount applies, so RPs (full value) are preferred for limited
				// P slots. The daily run corrects this when probables are announced.
				hasGame = teamPlays
			}
		} else {
			// RP: always eligible if team plays (can enter any game).
			hasGame = teamPlays
		}

		// Get projection.
		var pts float64
		found := false

		if hasPPS {
			if blended, ok := pps.GetPitcherPtsPerGame(p.Name, p.MLBTeam, scoring); ok {
				pts = blended
				found = true
			}
		}

		if !found {
			proj, ok := projSrc.GetPitcherProjection(p.Name, p.MLBTeam)
			if ok && proj.G > 0 {
				pts = pitcherExpectedPts(proj, scoring)
			}
		}

		scored = append(scored, ScoredPitcher{
			Player:      p,
			ExpectedPts: pts,
			HasGame:     hasGame,
			IsStarter:   isStarter,
		})
	}
	return scored
}

// pitcherExpectedPts converts a season pitcher projection to expected fantasy points per game.
func pitcherExpectedPts(proj *projections.PitcherProjection, scoring fantrax.ScoringWeights) float64 {
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

func isSPEligible(positions []string) bool {
	for _, pos := range positions {
		if pos == auth_client.PosSP { // "015"
			return true
		}
	}
	return false
}

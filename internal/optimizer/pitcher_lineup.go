package optimizer

import (
	"sort"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
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
func OptimizePitcherLineup(
	roster []fantrax.Player,
	playingToday map[string]bool,
	probableStarters map[string]string,
	projSrc projections.PitcherSource,
	scoring fantrax.ScoringWeights,
	slots []fantrax.Slot,
) PitcherResult {
	scored := scorePitcherRoster(roster, playingToday, probableStarters, projSrc, scoring)

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

	// Convert to ScoredPlayer for reuse of optimalAssignment.
	// Only include pitchers with games as candidates — unlike hitters,
	// pitchers without games (SP on non-start days) should not fill slots.
	var generic []ScoredPlayer
	for _, sp := range scored {
		if sp.HasGame {
			generic = append(generic, ScoredPlayer{
				Player:      sp.Player,
				ExpectedPts: sp.ExpectedPts,
				HasGame:     sp.HasGame,
			})
		}
	}

	// Build current assignment map.
	currentAssign := make(map[string]string)
	for _, p := range roster {
		if p.Status == "Active" && p.RosterPosition != "" {
			currentAssign[p.ID] = p.RosterPosition
		}
	}

	toActivate := optimalAssignment(generic, slots, currentAssign, fantrax.EligibleForPitcherSlot)

	// Build set of players in the optimal lineup.
	assigned := make(map[string]bool)
	for _, ps := range toActivate {
		assigned[ps.PlayerID] = true
	}

	// Only emit changes.
	var changedActivate []fantrax.PlayerSlot
	for _, ps := range toActivate {
		if currentAssign[ps.PlayerID] != ps.PosID {
			changedActivate = append(changedActivate, ps)
		}
	}

	// Bench pitchers who are currently active but not in the optimal lineup.
	var toBench []string
	for _, p := range roster {
		if p.Status == "Active" && !assigned[p.ID] {
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

		spEligible := isSPEligible(p.Positions)
		teamPlays := playingToday[p.MLBTeam]

		// Determine hasGame based on role.
		var hasGame, isStarter bool
		normalizedName := projections.NormalizeName(p.Name)

		if spEligible {
			if hasProbableData {
				// SP with probable data: only start if listed as probable starter.
				if team, ok := probableStarters[normalizedName]; ok && team == p.MLBTeam {
					hasGame = true
					isStarter = true
				} else if !teamPlays {
					// Team doesn't play — no game regardless.
					hasGame = false
				} else {
					// Team plays but pitcher not listed as probable: bench.
					// However, dual SP/RP players can still pitch in relief.
					if isRPEligible(p.Positions) {
						// Dual eligible: treat as RP when not starting.
						hasGame = teamPlays
					} else {
						hasGame = false
					}
				}
			} else {
				// No probable data (future date/TBD): default to start if team plays.
				hasGame = teamPlays
				isStarter = teamPlays
			}
		} else {
			// RP-only: start if team plays.
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

func isRPEligible(positions []string) bool {
	for _, pos := range positions {
		if pos == auth_client.PosRP { // "016"
			return true
		}
	}
	return false
}

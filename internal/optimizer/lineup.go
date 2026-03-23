package optimizer

import (
	"sort"
	"strings"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
	"github.com/nixon-commits/fantrax-optimizer/internal/projections"
)

// ScoredPlayer pairs a player with their expected fantasy points today.
type ScoredPlayer struct {
	Player      fantrax.Player
	ExpectedPts float64
	HasGame     bool
}

// Result describes the lineup changes the optimizer wants to make.
type Result struct {
	ToActivate []fantrax.PlayerSlot // move to active
	ToBench    []string              // player IDs to move to reserve
	Scored     []ScoredPlayer        // full ranking for logging
}

// OptimizeLineup computes the optimal daily hitter lineup.
//
//   - roster: all hitters (active + reserve)
//   - playingToday: MLB team abbreviations with games today
//   - projSrc: projection data source
//   - scoring: stat short-name → fantasy points per unit
//   - slots: ordered active slots (positional first, UTIL last)
func OptimizeLineup(
	roster []fantrax.Player,
	playingToday map[string]bool,
	projSrc projections.Source,
	scoring fantrax.ScoringWeights,
	slots []fantrax.Slot,
) Result {
	// Score every hitter.
	scored := scoreRoster(roster, playingToday, projSrc, scoring)

	// Sort: players with games first, then by expected points descending.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].HasGame != scored[j].HasGame {
			return scored[i].HasGame
		}
		return scored[i].ExpectedPts > scored[j].ExpectedPts
	})

	// Greedily assign players to slots.
	assigned := make(map[string]bool) // playerIDs already assigned
	var toActivate []fantrax.PlayerSlot

	for _, slot := range slots {
		for _, sp := range scored {
			if assigned[sp.Player.ID] {
				continue
			}
			if !eligible(sp.Player, slot) {
				continue
			}
			toActivate = append(toActivate, fantrax.PlayerSlot{
				PlayerID: sp.Player.ID,
				PosID:    slot.PosID,
			})
			assigned[sp.Player.ID] = true
			break
		}
	}

	// Anyone not assigned who was previously active gets benched.
	var toBench []string
	for _, p := range roster {
		if p.Status == "Active" && !assigned[p.ID] {
			toBench = append(toBench, p.ID)
		}
	}

	return Result{
		ToActivate: toActivate,
		ToBench:    toBench,
		Scored:     scored,
	}
}

// scoreRoster returns scored players for the full roster.
func scoreRoster(
	roster []fantrax.Player,
	playingToday map[string]bool,
	projSrc projections.Source,
	scoring fantrax.ScoringWeights,
) []ScoredPlayer {
	scored := make([]ScoredPlayer, 0, len(roster))
	for _, p := range roster {
		hasGame := playingToday[strings.ToUpper(p.MLBTeam)]
		proj, ok := projSrc.GetProjection(p.Name, p.MLBTeam)
		var pts float64
		if ok && proj.PA > 0 {
			pts = expectedPts(proj, scoring)
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
	// Estimate games from PA (roughly 4 PA/game).
	games := proj.PA / 4.0
	if games <= 0 {
		return 0
	}

	statMap := map[string]float64{
		"H":   proj.H,
		"2B":  proj.Doubles,
		"3B":  proj.Triples,
		"HR":  proj.HR,
		"RBI": proj.RBI,
		"R":   proj.R,
		"BB":  proj.BB,
		"SB":  proj.SB,
		"CS":  proj.CS,
		"HBP": proj.HBP,
	}

	var total float64
	for stat, seasonVal := range statMap {
		if pts, ok := scoring[stat]; ok {
			perGame := seasonVal / games
			total += perGame * pts
		}
	}
	return total
}

// eligible returns true if a player can fill the given slot based on position eligibility.
func eligible(p fantrax.Player, slot fantrax.Slot) bool {
	// UTIL accepts any hitter.
	if slot.PosName == "UTIL" || slot.PosID == "014" {
		return true
	}
	for _, pos := range p.Positions {
		if strings.EqualFold(pos, slot.PosName) {
			return true
		}
		// Handle MI (2B or SS eligible).
		if slot.PosName == "MI" && (strings.EqualFold(pos, "2B") || strings.EqualFold(pos, "SS")) {
			return true
		}
	}
	return false
}

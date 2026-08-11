package dynasty

import (
	"sort"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// benchSlots are Sleeper roster_positions entries that are not starting
// lineup slots.
var benchSlots = map[string]bool{"BN": true, "TAXI": true, "IR": true}

// StarterSlotCount returns how many of the league's roster slots are
// starting slots (i.e. everything except bench/taxi/IR).
func StarterSlotCount(rosterPositions []string) int {
	n := 0
	for _, p := range rosterPositions {
		if !benchSlots[p] {
			n++
		}
	}
	return n
}

// SelectStarters returns the set of player IDs Aggregate treats as starters
// for one roster.
//
// Primary signal: roster.Starters, the manager's own lineup selection,
// filtered of Sleeper's empty-slot placeholder ("0"/""). Fallback (only when
// that filtered set is empty, i.e. Starters is missing or entirely unset):
// the wantCount rostered players with the lowest search_rank (Sleeper's own
// depth-chart ranking; lower is more relevant).
//
// Deliberately never StatsGuy value in either path: choosing starters by
// value would make an unvalued player unselectable and starters-coverage
// 100% by construction (the exact rosterbot-hx5 failure this design avoids —
// see bd memory dynasty-football-coverage-gate-2026-08-10).
func SelectStarters(roster sleeper.Roster, wantCount int, players map[string]sleeper.Player) map[string]bool {
	set := make(map[string]bool)
	for _, id := range roster.Starters {
		if id == "" || id == "0" {
			continue
		}
		set[id] = true
	}
	if len(set) > 0 {
		return set
	}

	type ranked struct {
		id   string
		rank int
	}
	candidates := make([]ranked, 0, len(roster.Players))
	for _, id := range roster.Players {
		rank := players[id].SearchRank
		candidates = append(candidates, ranked{id: id, rank: rank})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].id < candidates[j].id // deterministic tiebreak
	})
	fallback := make(map[string]bool, wantCount)
	for i := 0; i < wantCount && i < len(candidates); i++ {
		fallback[candidates[i].id] = true
	}
	return fallback
}

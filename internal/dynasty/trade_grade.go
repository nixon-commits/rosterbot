package dynasty

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

// TradeAsset is one thing changing hands in a Sleeper trade transaction: a
// player or a draft pick, priced against the StatsGuy bundle.
type TradeAsset struct {
	Name   string
	Value  int
	Priced bool
}

// TradeSide is what one roster received in a trade.
type TradeSide struct {
	TeamID   string
	TeamName string
	Assets   []TradeAsset
}

// Raw is the plain sum of priced asset values.
func (s TradeSide) Raw() int {
	total := 0
	for _, a := range s.Assets {
		if a.Priced {
			total += a.Value
		}
	}
	return total
}

// Coverage reports how many of the side's assets carry a usable value.
func (s TradeSide) Coverage() (priced, total int) {
	for _, a := range s.Assets {
		if a.Priced {
			priced++
		}
	}
	return priced, len(s.Assets)
}

// TradeStatus is the outcome of GradeTrade.
type TradeStatus string

const (
	// TradeFavors means every asset priced and one side's raw sum leads.
	TradeFavors TradeStatus = "favors"
	// TradeIncomplete means at least one asset could not be priced, so no
	// comparison is meaningful.
	TradeIncomplete TradeStatus = "incomplete"
)

// TradeVerdict is the graded outcome of one trade.
type TradeVerdict struct {
	Status          TradeStatus
	FavoredTeamID   string
	FavoredTeamName string
	// Pct is the symmetric percent difference between the leading and
	// runner-up sides -- |a-b| as a share of their mean, matching HKB's own
	// reported figure (see internal/tradevalue.pctDiff, the baseball analog).
	Pct            float64
	UnpricedAssets int
}

// GradeTrade prices every side by a plain local sum and decides whether a
// verdict can be stated.
//
// Deliberately two-branch, not tradevalue's three: baseball's Evaluate
// compares a raw sum against a PackageDecay-adjusted sum and calls it
// "too-close" when the two methods disagree. That decay constant is
// explicitly NOT ported here -- it is documented as "an unmeasured prior,
// not a measured value" fitted to one HKB observation (rosterbot-492 is open
// to measure it properly), and StatsGuy's own /trades/evaluate applies none
// (measured: 9361+1757=11118 exactly, a plain sum). With only one pricing
// method, there is no second method to disagree with, so there is no
// too-close state -- a trade either grades cleanly or it is incomplete.
//
// Any unpriced asset on any side makes the whole verdict Incomplete, the
// same verdict-suppression discipline as tradevalue.Evaluate: a side total
// that omits an unpriced pick or player is not a smaller number, it is a
// different question. Football's own picks are ~100% priceable (StatsGuy's
// mid-tier prices every round the league drafts), where baseball is
// pick-blocked on 36% of verdicts -- so this branch should rarely fire.
func GradeTrade(sides []TradeSide) TradeVerdict {
	if len(sides) < 2 {
		return TradeVerdict{Status: TradeIncomplete}
	}

	unpriced, totalAssets := 0, 0
	for _, s := range sides {
		priced, total := s.Coverage()
		unpriced += total - priced
		totalAssets += total
	}
	if unpriced > 0 {
		return TradeVerdict{Status: TradeIncomplete, UnpricedAssets: unpriced}
	}
	if totalAssets == 0 {
		// Every side is empty: nothing this system tracks changed hands (or
		// everything that did was an unmodeled kind). A 0-0 tie is not a
		// verdict -- declaring one here is exactly the rosterbot-hx5 failure
		// shape, a statistic reporting a confident answer to a question that
		// had no real input.
		return TradeVerdict{Status: TradeIncomplete}
	}

	type scored struct {
		id, name string
		val      int
	}
	all := make([]scored, 0, len(sides))
	for _, s := range sides {
		all = append(all, scored{s.TeamID, s.TeamName, s.Raw()})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].val != all[j].val {
			return all[i].val > all[j].val
		}
		return all[i].id < all[j].id // stable tiebreak, matches the optimizer's convention
	})

	return TradeVerdict{
		Status:          TradeFavors,
		FavoredTeamID:   all[0].id,
		FavoredTeamName: all[0].name,
		Pct:             pctDiff(float64(all[0].val), float64(all[1].val)),
	}
}

func pctDiff(a, b float64) float64 {
	mean := (a + b) / 2
	if mean == 0 {
		return 0
	}
	return math.Abs(a-b) / mean * 100
}

// BuildTradeSides groups a completed trade transaction's assets by receiving
// roster, pricing players by exact Sleeper id against the bundle and picks
// via MidVariantPrice -- both in the given StatsGuy format. FAAB budget
// transfers are included as unpriced assets: StatsGuy prices dynasty players
// and picks, not cash, so a FAAB-involving trade correctly falls to
// GradeTrade's Incomplete branch rather than the cash silently vanishing
// (the real live-league trade that surfaced this: an empty side, because
// its only asset was FAAB this function used to drop entirely).
func BuildTradeSides(txn sleeper.Transaction, players map[string]sleeper.Player, bundle *statsguy.Bundle, teamNamesByRosterID map[int]string, format string) []TradeSide {
	byRoster := make(map[int]*TradeSide, len(txn.RosterIDs))
	for _, id := range txn.RosterIDs {
		byRoster[id] = &TradeSide{TeamID: strconv.Itoa(id), TeamName: teamNamesByRosterID[id]}
	}

	for playerID, rosterID := range txn.Adds {
		s, ok := byRoster[rosterID]
		if !ok {
			continue
		}
		sv, matched := bundle.Players[playerID]
		s.Assets = append(s.Assets, TradeAsset{
			Name:   playerDisplayName(players, playerID),
			Value:  sv.Value.Get(format),
			Priced: matched,
		})
	}

	for _, dp := range txn.DraftPicks {
		s, ok := byRoster[dp.OwnerID]
		if !ok {
			continue
		}
		year, err := strconv.Atoi(dp.Season)
		if err != nil {
			continue
		}
		value, priced := MidVariantPrice(bundle, year, dp.Round)
		s.Assets = append(s.Assets, TradeAsset{
			Name:   fmt.Sprintf("%s Round %d Pick", dp.Season, dp.Round),
			Value:  value.Get(format),
			Priced: priced,
		})
	}

	for _, wb := range txn.WaiverBudget {
		s, ok := byRoster[wb.Receiver]
		if !ok {
			continue
		}
		s.Assets = append(s.Assets, TradeAsset{
			Name:   fmt.Sprintf("$%d FAAB", wb.Amount),
			Priced: false,
		})
	}

	sides := make([]TradeSide, 0, len(byRoster))
	for _, s := range byRoster {
		sides = append(sides, *s)
	}
	sort.Slice(sides, func(i, j int) bool { return sides[i].TeamID < sides[j].TeamID })
	return sides
}

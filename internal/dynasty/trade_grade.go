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

	// Sides are sorted descending and StatsGuy values are never negative, so a
	// zero leader means EVERY side totals zero: everything priced, and all of it
	// worth nothing in this format. That is the same non-verdict as the
	// totalAssets==0 branch above, reached a different way, and it is routine
	// rather than exotic -- a future draft pick has no redraft value at all, so
	// every pick-for-pick trade is 0-vs-0 in both redraft formats.
	//
	// Reachable only since the trade log began grading all four formats: the
	// alert has always used one dynasty format, where values are non-zero, so
	// nothing ever asked this question before. Left ungated it renders as
	// "favors <whoever sorted first> (+0%)" -- the rosterbot-hx5 shape again, a
	// statistic answering confidently on no input.
	//
	// UnpricedAssets stays 0, which is what distinguishes this from the
	// could-not-be-priced branch: the view says "no value in this format" rather
	// than blaming an asset it did price.
	if all[0].val == 0 {
		return TradeVerdict{Status: TradeIncomplete}
	}

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
// roster, priced in one StatsGuy format.
//
// It is a projection of BuildTradeSidesAll rather than its own construction
// pass. There is exactly one place that decides what a trade's assets ARE, so
// a single-format grade and the four-format trade log can never disagree
// about which assets changed hands -- the drift shape this repo has been bitten
// by before when a reader and a writer each built the same key.
func BuildTradeSides(txn sleeper.Transaction, players map[string]sleeper.Player, bundle *statsguy.Bundle, teamNamesByRosterID map[int]string, format string) []TradeSide {
	return ProjectTradeSides(BuildTradeSidesAll(txn, players, bundle, teamNamesByRosterID), format)
}

// AssetAll is one thing changing hands, priced in every StatsGuy format.
//
// Priced is deliberately format-independent: it records whether the asset
// JOINED the bundle at all (a player id match, or a pick tier StatsGuy lists),
// which is a property of the asset, not of the format. An asset that joins is
// priced in all four formats or none.
type AssetAll struct {
	Name   string
	Values statsguy.FormatValues
	Priced bool
}

// SideAll is what one roster received, priced in every StatsGuy format.
type SideAll struct {
	TeamID   string
	TeamName string
	Assets   []AssetAll
}

// Totals sums this side's priced assets in every format.
func (s SideAll) Totals() statsguy.FormatValues {
	var t statsguy.FormatValues
	for _, a := range s.Assets {
		if !a.Priced {
			continue
		}
		t.SFDynasty += a.Values.SFDynasty
		t.NonSFDynasty += a.Values.NonSFDynasty
		t.SFRedraft += a.Values.SFRedraft
		t.NonSFRedraft += a.Values.NonSFRedraft
	}
	return t
}

// ProjectTradeSides narrows every side to one StatsGuy format.
func ProjectTradeSides(all []SideAll, format string) []TradeSide {
	sides := make([]TradeSide, 0, len(all))
	for _, s := range all {
		assets := make([]TradeAsset, 0, len(s.Assets))
		for _, a := range s.Assets {
			assets = append(assets, TradeAsset{Name: a.Name, Value: a.Values.Get(format), Priced: a.Priced})
		}
		sides = append(sides, TradeSide{TeamID: s.TeamID, TeamName: s.TeamName, Assets: assets})
	}
	return sides
}

// BuildTradeSidesAll groups a completed trade transaction's assets by receiving
// roster, pricing players by exact Sleeper id against the bundle and picks via
// MidVariantPrice -- carrying all four StatsGuy formats rather than collapsing
// to one, so the durable trade log can answer the dashboard's format toggle
// without re-pricing against a later bundle.
//
// FAAB budget transfers are included as unpriced assets: StatsGuy prices
// dynasty players and picks, not cash, so a FAAB-involving trade correctly
// falls to GradeTrade's Incomplete branch rather than the cash silently
// vanishing (the real live-league trade that surfaced this: an empty side,
// because its only asset was FAAB this function used to drop entirely).
//
// Assets within a side are sorted, because txn.Adds is a MAP and Go randomizes
// map iteration: without the sort the same trade yields a different asset order
// on every call, which would make the alert body and the durable log row churn
// between runs for no reason. Sides are sorted by TeamID for the same reason.
func BuildTradeSidesAll(txn sleeper.Transaction, players map[string]sleeper.Player, bundle *statsguy.Bundle, teamNamesByRosterID map[int]string) []SideAll {
	byRoster := make(map[int]*SideAll, len(txn.RosterIDs))
	for _, id := range txn.RosterIDs {
		byRoster[id] = &SideAll{TeamID: strconv.Itoa(id), TeamName: teamNamesByRosterID[id]}
	}

	for playerID, rosterID := range txn.Adds {
		s, ok := byRoster[rosterID]
		if !ok {
			continue
		}
		sv, matched := bundle.Players[playerID]
		s.Assets = append(s.Assets, AssetAll{
			Name:   playerDisplayName(players, playerID),
			Values: sv.Value,
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
		s.Assets = append(s.Assets, AssetAll{
			Name:   fmt.Sprintf("%s Round %d Pick", dp.Season, dp.Round),
			Values: value,
			Priced: priced,
		})
	}

	for _, wb := range txn.WaiverBudget {
		s, ok := byRoster[wb.Receiver]
		if !ok {
			continue
		}
		s.Assets = append(s.Assets, AssetAll{
			Name:   fmt.Sprintf("$%d FAAB", wb.Amount),
			Priced: false,
		})
	}

	sides := make([]SideAll, 0, len(byRoster))
	for _, s := range byRoster {
		sort.Slice(s.Assets, func(i, j int) bool {
			if s.Assets[i].Name != s.Assets[j].Name {
				return s.Assets[i].Name < s.Assets[j].Name
			}
			return s.Assets[i].Values.SFDynasty > s.Assets[j].Values.SFDynasty
		})
		sides = append(sides, *s)
	}
	sort.Slice(sides, func(i, j int) bool { return sides[i].TeamID < sides[j].TeamID })
	return sides
}

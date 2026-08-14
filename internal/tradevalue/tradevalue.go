// Package tradevalue prices the sides of a trade against HKB dynasty values
// and decides whether the result is worth stating as a verdict.
//
// It is a pure leaf: it imports internal/hkb for the player record and
// internal/playername for the join key, and does no I/O. Both the dashboard
// producer (internal/tradeboard) and the Pushover trade monitor
// (internal/transactions) value trades through here, so the two can never
// disagree about the same trade.
//
// The package is n-sided throughout. Two-team trades are the only kind this
// league has ever seen, but internal/transactions modelled sides as a fixed
// [2]TradeSide and silently dropped the third team's assets, so the shape is
// generalized here rather than inherited.
package tradevalue

import (
	"fmt"
	"math"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/hkb"
)

// PackageDecay discounts each asset on a side after the most valuable one:
// the k-th best asset (0-indexed) contributes Value * PackageDecay^k. It
// models the fact that roster spots are finite, so three good players are
// worth less than the sum of three good players.
//
// THIS CONSTANT IS AN UNMEASURED PRIOR, NOT A MEASURED VALUE. HKB applies a
// "package adjustment" in client-side JS that is not present anywhere in the
// payload we scrape, so 0.70 was fitted to exactly ONE observation of their
// calculator: assets 589/540/366 summing to 1495 were reported at 1144, and
// 589 + 540(0.70) + 366(0.70^2) = 1146.3, a residual of ~0.2%. One data point
// cannot distinguish this decay from a dozen other curves that pass near it.
//
// That uncertainty is exactly why Evaluate never reports the adjusted figure
// as the answer. The adjustment is real and large enough to invert a verdict
// -- on live offer lpe0ltlcmsdt40zv a 2-for-1 reads "favors you by 333" raw
// and "favors them by 37" adjusted -- so ignoring it would be worse than
// approximating it. But the honest reading of a fitted-to-one-point constant
// is that when it disagrees with the raw sum, the trade is too close to call,
// which is what Evaluate reports. The disagreement is the signal; the adjusted
// number by itself is not.
//
// Fitting it properly against several observations is rosterbot-492.
const PackageDecay = 0.70

// Asset is one thing changing hands: a player, or a draft pick.
type Asset struct {
	Name     string `json:"name"`
	Position string `json:"position,omitempty"`
	Value    int    `json:"value"`

	// Priced reports whether Value means anything. It is false for an
	// unidentified draft pick and for a player who missed the HKB join.
	// An unpriced asset contributes nothing to Raw or Adjusted, which is
	// why those totals are floors rather than totals whenever Coverage is
	// short -- and why Evaluate refuses a verdict in that case.
	Priced bool `json:"priced"`

	// IsPick marks a draft pick. Before rosterbot-uc3, IsPick always implied
	// !Priced: Fantrax's simplified Transaction carried no year or round for
	// a pick row, only an empty player name. NewDraftPickAsset recovers that
	// identity from the row's draftPickDisplayParts and can produce a Priced
	// pick when HKB still lists the year+round as upcoming.
	IsPick bool `json:"is_pick"`

	// Estimated marks a Value that is not a direct HKB name match but an
	// average across HKB's Early/Mid/Late tiers for a pick's year+round --
	// see NewDraftPickAsset. Always false for a player asset.
	Estimated bool `json:"estimated,omitempty"`
}

// Side is what one team receives in a trade.
type Side struct {
	Team   string  `json:"team"`
	Assets []Asset `json:"assets"`
}

// Raw is the plain sum of priced asset values.
func (s Side) Raw() int {
	total := 0
	for _, a := range s.Assets {
		if a.Priced {
			total += a.Value
		}
	}
	return total
}

// Adjusted is the sum with PackageDecay applied to every asset after the
// best: the k-th most valuable contributes Value * PackageDecay^k.
func (s Side) Adjusted() float64 {
	vals := make([]int, 0, len(s.Assets))
	for _, a := range s.Assets {
		if a.Priced {
			vals = append(vals, a.Value)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(vals)))

	total := 0.0
	for k, v := range vals {
		total += float64(v) * math.Pow(PackageDecay, float64(k))
	}
	return total
}

// Coverage reports how many of the side's assets carry a usable value. It is
// the denominator behind Raw and Adjusted, and callers are expected to show
// it: a side reading "1898" means something different at 4/4 priced than at
// 4/6.
func (s Side) Coverage() (priced, total int) {
	for _, a := range s.Assets {
		if a.Priced {
			priced++
		}
	}
	return priced, len(s.Assets)
}

// FullyPriced reports whether every asset on the side has a usable value.
func (s Side) FullyPriced() bool {
	priced, total := s.Coverage()
	return priced == total
}

// Status is the three-way outcome of Evaluate.
type Status string

const (
	// StatusFavors means both pricing methods agree on which side wins.
	StatusFavors Status = "favors"
	// StatusTooClose means the raw and adjusted sums name different
	// winners, so the trade is inside the model's own uncertainty.
	StatusTooClose Status = "too-close"
	// StatusIncomplete means at least one asset could not be priced, so no
	// comparison is meaningful at all.
	StatusIncomplete Status = "incomplete"
)

// Verdict is the answer, plus enough of the working to argue with it.
type Verdict struct {
	Status Status `json:"status"`

	// FavoredTeam is set only when Status is StatusFavors.
	FavoredTeam string `json:"favored_team,omitempty"`

	// RawLeader and AdjLeader name each method's winner. They are equal
	// when Status is StatusFavors and differ when it is StatusTooClose.
	// Both are empty when Status is StatusIncomplete.
	RawLeader string `json:"raw_leader,omitempty"`
	AdjLeader string `json:"adj_leader,omitempty"`

	// RawPct and AdjPct are the gap between the leading and runner-up
	// sides as a percentage of their mean -- the symmetric percent
	// difference HKB itself reports ("a 91.6% value difference" for 3079
	// against 1144). Using the leader as the denominator instead would
	// print 62.8% for that same trade and would not match the tool these
	// numbers get compared against.
	RawPct float64 `json:"raw_pct"`
	AdjPct float64 `json:"adj_pct"`

	// UnpricedAssets counts the assets that blocked a StatusIncomplete
	// verdict, across every side.
	UnpricedAssets int `json:"unpriced_assets,omitempty"`
}

// Evaluate prices every side and decides whether a verdict can be stated.
//
// The rule, in order:
//
//  1. Any unpriced asset anywhere -> StatusIncomplete. A side total that
//     omits a 2027 first-rounder is not a smaller number, it is a different
//     question, and reporting a winner from it would be confidently wrong
//     rather than merely imprecise. Measured against this league's executed
//     trade history, this branch takes 7 of 16 trades.
//  2. Raw and adjusted disagree on the leader -> StatusTooClose. The gap is
//     inside the uncertainty of a constant fitted to one data point, so
//     neither number earns the call.
//  3. Otherwise -> StatusFavors.
//
// Fewer than two sides is StatusIncomplete: there is nothing to compare.
func Evaluate(sides []Side) Verdict {
	if len(sides) < 2 {
		return Verdict{Status: StatusIncomplete}
	}

	unpriced := 0
	for _, s := range sides {
		priced, total := s.Coverage()
		unpriced += total - priced
	}
	if unpriced > 0 {
		return Verdict{Status: StatusIncomplete, UnpricedAssets: unpriced}
	}

	rawLeader, rawTop, rawNext := leader(sides, func(s Side) float64 { return float64(s.Raw()) })
	adjLeader, adjTop, adjNext := leader(sides, Side.Adjusted)

	v := Verdict{
		RawLeader: rawLeader,
		AdjLeader: adjLeader,
		RawPct:    pctDiff(rawTop, rawNext),
		AdjPct:    pctDiff(adjTop, adjNext),
	}
	if rawLeader != adjLeader {
		v.Status = StatusTooClose
		return v
	}
	v.Status = StatusFavors
	v.FavoredTeam = rawLeader
	return v
}

// leader returns the highest-scoring side's team along with the top and
// runner-up scores. Ties break on team name so the result is stable across
// runs -- the same idempotency requirement the optimizer's player-ID
// tiebreaker exists for.
func leader(sides []Side, score func(Side) float64) (team string, top, next float64) {
	type scored struct {
		team string
		val  float64
	}
	all := make([]scored, 0, len(sides))
	for _, s := range sides {
		all = append(all, scored{s.Team, score(s)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].val != all[j].val {
			return all[i].val > all[j].val
		}
		return all[i].team < all[j].team
	})
	return all[0].team, all[0].val, all[1].val
}

// pctDiff is the symmetric percent difference: |a-b| as a share of the mean
// of a and b. Zero when both sides are worthless.
func pctDiff(a, b float64) float64 {
	mean := (a + b) / 2
	if mean == 0 {
		return 0
	}
	return math.Abs(a-b) / mean * 100
}

// NewAsset builds an Asset by joining a Fantrax trade row against HKB.
//
// An empty name is Fantrax's representation of a draft pick: the row's
// scorer object carries no name for the upstream parser to read. This
// constructor has no year or round to work with -- a caller that has
// already recovered a pick's identity (see NewDraftPickAsset) should call
// that instead, since the identity lives in a different part of the row
// than the player scorer this function reads.
func NewAsset(name, position string, lookup hkb.Lookup) Asset {
	if name == "" {
		return Asset{Name: "Draft pick (unidentified)", IsPick: true}
	}
	e := hkb.Enrich(name, position, lookup, false)
	return Asset{
		Name:     name,
		Position: position,
		Value:    e.Value,
		Priced:   e.Ranked,
	}
}

// NewDraftPickAsset builds an Asset for a traded draft pick using the
// identity recovered from Fantrax's draftPickDisplayParts (rosterbot-uc3):
// the draft year, the round, and either a resolved slot number or the team
// whose future draft slot it is.
//
// HKB prices picks generically by projected slot (Early/Mid/Late per
// round), not by team, and Fantrax's payload names the team but not that
// team's projected standing -- so there is no principled way to choose one
// of the three tiers. hkb.PickAverage's mean of whichever tiers HKB
// currently lists is used instead, and the result is flagged Estimated so
// callers can distinguish it from a direct name match.
//
// A pick with a resolved slot (pickNumber > 0) came from a draft whose
// order is already known, which also means HKB has stopped listing it as
// an upcoming PICK asset -- PickAverage reports zero tiers for it and the
// asset stays unpriced, exactly as an unidentified pick did before this.
func NewDraftPickAsset(year, round, pickNumber int, originalTeam string, players []hkb.Player) Asset {
	name := pickDisplayName(year, round, pickNumber, originalTeam)
	avg, tiers := hkb.PickAverage(players, year, round)
	if tiers == 0 {
		return Asset{Name: name, IsPick: true}
	}
	return Asset{Name: name, Value: avg, Priced: true, IsPick: true, Estimated: true}
}

func pickDisplayName(year, round, pickNumber int, originalTeam string) string {
	label := fmt.Sprintf("%d %s Round Pick", year, hkb.Ordinal(round))
	switch {
	case pickNumber > 0:
		return fmt.Sprintf("%s (Pick %d)", label, pickNumber)
	case originalTeam != "":
		return fmt.Sprintf("%s (%s)", label, originalTeam)
	default:
		return label
	}
}

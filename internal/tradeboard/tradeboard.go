// Package tradeboard builds the two artifacts behind the dashboard's Trades
// tab: the current pending-offer snapshot and the league values table.
//
// The two are produced on different schedules on purpose. Pending offers are
// the time-sensitive half and cost one small league-home call, so the hourly
// Lineup job writes them. The values table needs the 10.7 MB full player pool
// and cannot in any case be fresher than HKB's own 8h cache TTL, so the daily
// TeamValues job writes it -- that job already holds the pool, the HKB
// rankings and the team names.
//
// This package deliberately does NOT import internal/fantrax. Callers map
// their own trade rows onto OfferInput. That keeps chromedp out of the
// dependency graph, which is the same constraint that forces
// internal/opsalert to redeclare its ledger fields, and it leaves the door
// open for internal/lineupapi to type these payloads later if it ever needs
// to (today it passes the bytes through untouched).
package tradeboard

import (
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/nixon-commits/rosterbot/internal/tradevalue"
)

// OfferInput is one asset changing hands within a pending trade, as the
// producer observed it. An empty Player is how Fantrax represents a draft
// pick (see tradevalue.NewAsset).
type OfferInput struct {
	TradeID  string
	Player   string
	Position string
	From     string // fantasy team name giving the asset up
	To       string // fantasy team name receiving it
}

// Offer is one pending trade, valued and judged.
type Offer struct {
	TradeID string             `json:"trade_id"`
	Sides   []Side             `json:"sides"`
	Verdict tradevalue.Verdict `json:"verdict"`

	// Impact is what accepting would do to my roster's value composition.
	// Nil when the verdict is incomplete -- an offer we cannot price is one
	// whose consequence we cannot state either -- and when the teams could
	// not be resolved against the values table.
	Impact *Impact `json:"impact,omitempty"`
}

// Side is one team's half of an offer: what they receive, and the totals.
// The valuation lives in tradevalue; this adds the presentation fields the
// tab needs so the browser does no arithmetic.
type Side struct {
	tradevalue.Side
	Raw         int     `json:"raw"`
	Adjusted    float64 `json:"adjusted"`
	PricedCount int     `json:"priced_count"`
	AssetCount  int     `json:"asset_count"`
}

// Snapshot is the whole trades/current.json payload.
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	MyTeam      string    `json:"my_team"`
	Offers      []Offer   `json:"offers"`
}

// PlayerValue is one rostered player in the values table.
type PlayerValue struct {
	Name      string  `json:"name"`
	Position  string  `json:"position,omitempty"`
	OwnerID   string  `json:"owner_id"`
	OwnerName string  `json:"owner_name"`
	Value     int     `json:"value"`
	Rank      int     `json:"rank,omitempty"`
	Age       float64 `json:"age,omitempty"`
	Level     string  `json:"level,omitempty"`
	IsPitcher bool    `json:"is_pitcher"`
	IsMinors  bool    `json:"is_minors"`
	Prospect  bool    `json:"prospect,omitempty"`
	FYPD      bool    `json:"fypd,omitempty"`

	// Matched is false when the player found no HKB entry, in which case
	// Value is 0 and the row is shown as unvalued rather than worthless.
	Matched bool `json:"matched"`
}

// PickValue is one of HKB's draft-pick assets. Picks are a league-wide
// reference list, not owned rows: Fantrax never tells us who holds which
// pick, so they cannot be attributed to a team (rosterbot-uc3).
type PickValue struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
	Rank  int    `json:"rank,omitempty"`
}

// ValuesTable is the whole trades/values.json payload.
type ValuesTable struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Players     []PlayerValue   `json:"players"`
	Picks       []PickValue     `json:"picks"`
	Teams       []teamvalue.Row `json:"teams"`

	// Rostered and Matched are the league-wide HKB join coverage. Every
	// value on this page is a floor by exactly the unmatched players, and
	// showing the denominator is what keeps that visible.
	Rostered int `json:"rostered"`
	Matched  int `json:"matched"`
}

// PoolPlayer is the subset of a Fantrax pool entry the values table needs.
// Declared here rather than imported for the reason in the package doc.
type PoolPlayer struct {
	Name           string
	Positions      []string
	FantasyTeamID  string
	MinorsEligible bool
	IsPitcher      bool
}

// BuildValuesTable joins every rostered player onto HKB and assembles the
// league values table. teams carries the per-team aggregates already computed
// by teamvalue.Aggregate -- this does not recompute them.
func BuildValuesTable(now time.Time, pool []PoolPlayer, hkbPlayers []hkb.Player, teams []teamvalue.Row, teamNames map[string]string) ValuesTable {
	lookup := hkb.BuildLookup(hkbPlayers)

	t := ValuesTable{GeneratedAt: now, Teams: teams}
	for _, pp := range pool {
		if pp.FantasyTeamID == "" {
			continue // free agent
		}
		t.Rostered++

		pos := ""
		if len(pp.Positions) > 0 {
			pos = pp.Positions[0]
		}
		pv := PlayerValue{
			Name:      pp.Name,
			Position:  pos,
			OwnerID:   pp.FantasyTeamID,
			OwnerName: teamNames[pp.FantasyTeamID],
			IsPitcher: pp.IsPitcher,
			IsMinors:  pp.MinorsEligible,
		}
		if hp, ok := lookup[playername.Normalize(pp.Name)]; ok {
			t.Matched++
			pv.Matched = true
			pv.Value = hp.Value
			pv.Rank = hp.Rank
			pv.Age = hp.Age
			pv.Level = hp.Level
			pv.Prospect = hp.Prospect
			pv.FYPD = hp.FYPD
		}
		t.Players = append(t.Players, pv)
	}

	for _, hp := range hkbPlayers {
		if hp.AssetType == "PICK" {
			t.Picks = append(t.Picks, PickValue{Name: hp.Name, Value: hp.Value, Rank: hp.Rank})
		}
	}

	// Deterministic order: value descending, name ascending on ties. The
	// artifact is rewritten daily and diffed by eye, so a stable sort keeps
	// an unchanged roster producing an unchanged file.
	sort.Slice(t.Players, func(i, j int) bool {
		if t.Players[i].Value != t.Players[j].Value {
			return t.Players[i].Value > t.Players[j].Value
		}
		return t.Players[i].Name < t.Players[j].Name
	})
	sort.Slice(t.Picks, func(i, j int) bool {
		if t.Picks[i].Value != t.Picks[j].Value {
			return t.Picks[i].Value > t.Picks[j].Value
		}
		return t.Picks[i].Name < t.Picks[j].Name
	})
	return t
}

// BuildOffers groups input rows into trades, prices each side against HKB and
// evaluates a verdict. Offers are returned in TradeID order so repeated runs
// against unchanged input produce an identical snapshot.
func BuildOffers(inputs []OfferInput, hkbPlayers []hkb.Player) []Offer {
	lookup := hkb.BuildLookup(hkbPlayers)

	byTrade := map[string]map[string][]tradevalue.Asset{}
	order := []string{}
	for _, in := range inputs {
		sides, ok := byTrade[in.TradeID]
		if !ok {
			sides = map[string][]tradevalue.Asset{}
			byTrade[in.TradeID] = sides
			order = append(order, in.TradeID)
		}
		sides[in.To] = append(sides[in.To], tradevalue.NewAsset(in.Player, in.Position, lookup))
	}
	sort.Strings(order)

	offers := make([]Offer, 0, len(order))
	for _, id := range order {
		raw := make([]tradevalue.Side, 0, len(byTrade[id]))
		for team, assets := range byTrade[id] {
			raw = append(raw, tradevalue.Side{Team: team, Assets: assets})
		}
		sort.Slice(raw, func(i, j int) bool { return raw[i].Team < raw[j].Team })

		o := Offer{TradeID: id, Verdict: tradevalue.Evaluate(raw)}
		for _, s := range raw {
			priced, total := s.Coverage()
			o.Sides = append(o.Sides, Side{
				Side:        s,
				Raw:         s.Raw(),
				Adjusted:    s.Adjusted(),
				PricedCount: priced,
				AssetCount:  total,
			})
		}
		offers = append(offers, o)
	}
	return offers
}

// Mine returns only the offers where myTeam is one of the sides.
func Mine(offers []Offer, myTeam string) []Offer {
	out := make([]Offer, 0, len(offers))
	for _, o := range offers {
		for _, s := range o.Sides {
			if s.Team == myTeam {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

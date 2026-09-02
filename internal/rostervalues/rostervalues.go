// Package rostervalues builds the My Team screen's payload: every player on a
// Fantrax roster, enriched with HKB dynasty value and 30-day momentum, one
// table per team.
//
// It is availablepool's sibling — the same join, the same namesake guard, the
// same sign and zero-sentinel rules, over the OWNED half of the pool — and
// imports that package's helpers rather than restating them, so the rank sign
// and the sentinel have one definition. Like availablepool it never imports
// internal/fantrax (chromedp rides behind it) and declares its own subset of a
// pool row.
//
// The one rule it does NOT share: an unranked player is kept, not dropped. On
// the pickups list a player HKB does not value has no reason to appear; on
// your own roster his absence is a silent omission. So every rostered player
// lands in exactly one segment, and the exhaustiveness invariant here is
// rostered == mlb + prospects rather than the pool's matched == mlb + prospects.
package rostervalues

import (
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/availablepool"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

// PoolPlayer is the subset of a Fantrax pool entry this package needs.
type PoolPlayer struct {
	ID      string
	Name    string
	MLBTeam string
	// Positions is the pool's raw PosShortNames ("SS,<b>INF</b>,UT"), markup
	// and all. availablepool.ParsePositions strips it; nothing raw reaches the
	// artifact.
	Positions string
	// FantasyTeamID is empty for an unowned player, who is skipped here and
	// belongs to availablepool instead. The FULL pool must still be passed:
	// the namesake guard can only see a contested name if both rows are.
	FantasyTeamID  string
	MinorsEligible bool
}

// Team names a Fantrax team. Only the name is consumed — the header total and
// the league rank are computed from the rows, so they cannot disagree with
// the list beneath them.
type Team struct {
	ID   string
	Name string
}

// Player is one row of a roster table.
//
// Every HKB-derived field is a pointer or omitempty, and that is the contract:
// on an unranked row (UnrankedReason set) ALL of them are absent from the wire.
// A client reading hkb_value: 0 would render a real-looking "0" for a player
// nobody valued, which is worse than a dash.
type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MLBTeam string `json:"mlb_team"`

	// Pos is HKB's position list; FantraxPos is the league's own eligibility.
	// Different facts — see availablepool.Player. FantraxPos survives an
	// unranked row because it comes from the pool, not from HKB.
	Pos        []string `json:"pos,omitempty"`
	FantraxPos []string `json:"fantrax_pos,omitempty"`

	Level        string `json:"level,omitempty"`
	ActiveLevels string `json:"active_levels,omitempty"`

	// Prospect is HKB's flag on a ranked row. On an UNRANKED row it is
	// Fantrax's minors eligibility instead — the one segmenting fact left when
	// HKB has none — and a client must not read it as an HKB judgement there.
	Prospect bool `json:"prospect"`
	FYPD     bool `json:"fypd"`

	// Age is absent, not 0, when HKB has no birthdate. The pool sends a
	// literal 0 for the same fact and every client special-cases it; a
	// pointer states it once, here.
	Age *float64 `json:"age,omitempty"`

	HKBValue *int `json:"hkb_value,omitempty"`
	HKBRank  *int `json:"hkb_rank,omitempty"`

	// Positive means better on both: see availablepool.NormaliseChanges.
	RankChange30D  *int `json:"rank_change_30d,omitempty"`
	ValueChange30D *int `json:"value_change_30d,omitempty"`

	// RankHistory30D uses 0 as "not ranked yet"; plot from RankHistoryStartsAt.
	// See availablepool.FirstRanked.
	RankHistory30D      []int `json:"rank_history_30d,omitempty"`
	RankHistoryStartsAt *int  `json:"rank_history_starts_at,omitempty"`

	// UnrankedReason is set, and every HKB field above absent, when HKB did
	// not value this player: ReasonNoMatch or ReasonNamesake.
	UnrankedReason string `json:"unranked_reason,omitempty"`
}

// The two ways a rostered player ends up without a value. They are counted
// separately because they fail differently: NoMatch climbing means name
// normalisation broke, Namesake climbing means the pool grew namesakes.
const (
	ReasonNoMatch  = "no HKB match"
	ReasonNamesake = "name shared by another Fantrax player"
)

// Counts is the join's denominator. Rostered == MLB + Prospects is the
// exhaustiveness invariant; Matched + Unmatched + NamesakeDeclined == Rostered
// is the join denominator. Both can fail, which is why both are emitted.
type Counts struct {
	Rostered         int `json:"rostered"`
	Matched          int `json:"matched"`
	Unmatched        int `json:"unmatched"`
	NamesakeDeclined int `json:"namesake_declined"`
	MLB              int `json:"mlb"`
	Prospects        int `json:"prospects"`
}

// Table is one team's roster/<team_id>.json payload.
type Table struct {
	// GeneratedAt is RFC3339 UTC with NO fractional seconds, formatted to a
	// string at construction for the reason availablepool.Table gives: a raw
	// time.Time marshals a variable-length fraction the iOS client's strict
	// formatter decodes to nil, which silently disarms its staleness check.
	GeneratedAt string `json:"generated_at"`
	HKBAsOf     string `json:"hkb_as_of"`

	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name,omitempty"`

	// TeamValue is the sum of the ranked rows below. LeagueRank ranks every
	// team's table by that same sum, 1-based, and TeamCount is how many there
	// are. One sum feeds all three, so header, list and rank cannot disagree.
	// It differs from the Team Values dashboard by exactly the namesake
	// declines, which that aggregate counts and this one refuses.
	TeamValue  int `json:"team_value"`
	LeagueRank int `json:"league_rank"`
	TeamCount  int `json:"team_count"`

	// Partitioned on Prospect, exactly as the pool is.
	MLB       []Player `json:"mlb"`
	Prospects []Player `json:"prospects"`
	Counts    Counts   `json:"counts"`
}

// BuildAll returns one Table per team with at least one rostered player,
// keyed by Fantrax team id. Unowned pool rows are skipped, not tabled.
func BuildAll(now time.Time, hkbAsOf string, pool []PoolPlayer, hkbPlayers []hkb.Player, teams []Team) map[string]Table {
	lookup := hkb.BuildLookup(hkbPlayers)

	// League-wide namesake census, over the whole pool — owned and unowned
	// alike, because the contest that matters is between an owned row and
	// anyone else carrying the name.
	namesakes := make(map[string]int, len(pool))
	for _, pp := range pool {
		namesakes[playername.Normalize(pp.Name)]++
	}

	names := make(map[string]string, len(teams))
	for _, tm := range teams {
		names[tm.ID] = tm.Name
	}
	generated := now.UTC().Format(time.RFC3339)

	tables := map[string]*Table{}
	for _, pp := range pool {
		if pp.FantasyTeamID == "" {
			continue
		}
		t := tables[pp.FantasyTeamID]
		if t == nil {
			t = &Table{
				GeneratedAt: generated, HKBAsOf: hkbAsOf,
				TeamID: pp.FantasyTeamID, TeamName: names[pp.FantasyTeamID],
				MLB: []Player{}, Prospects: []Player{},
			}
			tables[pp.FantasyTeamID] = t
		}
		t.Counts.Rostered++
		p, prospect := row(pp, lookup, namesakes[playername.Normalize(pp.Name)] > 1, &t.Counts)
		if prospect {
			t.Prospects = append(t.Prospects, p)
		} else {
			t.MLB = append(t.MLB, p)
		}
	}

	ids := make([]string, 0, len(tables))
	for id, t := range tables {
		sortRows(t.MLB)
		sortRows(t.Prospects)
		t.Counts.MLB = len(t.MLB)
		t.Counts.Prospects = len(t.Prospects)
		t.TeamValue = sumValues(t.MLB) + sumValues(t.Prospects)
		ids = append(ids, id)
	}
	// Total descending, id ascending on ties, so equal teams get stable
	// consecutive ranks rather than reshuffling day to day.
	sort.Slice(ids, func(i, j int) bool {
		a, b := tables[ids[i]], tables[ids[j]]
		if a.TeamValue != b.TeamValue {
			return a.TeamValue > b.TeamValue
		}
		return ids[i] < ids[j]
	})

	out := make(map[string]Table, len(tables))
	for i, id := range ids {
		t := tables[id]
		t.LeagueRank = i + 1
		t.TeamCount = len(ids)
		out[id] = *t
	}
	return out
}

// row builds one player and says which segment he belongs in.
//
// Order of the two refusals matches availablepool.Build: a lookup miss is
// Unmatched even for a contested name, and the namesake guard only fires on a
// hit — because it exists to stop a CONFIDENT wrong match, and a miss is not
// one.
func row(pp PoolPlayer, lookup hkb.Lookup, contested bool, c *Counts) (Player, bool) {
	p := Player{
		ID: pp.ID, Name: pp.Name, MLBTeam: pp.MLBTeam,
		FantraxPos: availablepool.ParsePositions(pp.Positions),
	}

	hp, ok := lookup.FindFor(pp.Name, hkb.Hint{MLBTeam: pp.MLBTeam, MinorsEligible: pp.MinorsEligible})
	switch {
	case !ok:
		c.Unmatched++
		p.UnrankedReason = ReasonNoMatch
	case contested:
		c.NamesakeDeclined++
		p.UnrankedReason = ReasonNamesake
	}
	if p.UnrankedReason != "" {
		// HKB's prospect flag is exactly what is missing; Fantrax's minors
		// eligibility is the one segmenting fact left.
		p.Prospect = pp.MinorsEligible
		return p, pp.MinorsEligible
	}

	c.Matched++
	rankChg, valueChg := availablepool.NormaliseChanges(hp)
	p.Name = hp.Name
	p.Pos = append([]string(nil), hp.Positions...)
	p.Level = hp.Level
	p.ActiveLevels = hp.ActiveLevels
	p.Prospect = hp.Prospect
	p.FYPD = hp.FYPD
	if hp.Age != 0 {
		p.Age = ptr(hp.Age)
	}
	p.HKBValue = ptr(hp.Value)
	if hp.Rank != 0 {
		p.HKBRank = ptr(hp.Rank)
	}
	p.RankChange30D = ptr(rankChg)
	p.ValueChange30D = ptr(valueChg)
	if len(hp.RankHistory30Days) > 0 {
		p.RankHistory30D = append([]int(nil), hp.RankHistory30Days...)
		p.RankHistoryStartsAt = ptr(availablepool.FirstRanked(hp.RankHistory30Days))
	}
	return p, hp.Prospect
}

// sortRows orders ranked rows value-descending, then unranked rows, with name
// ascending inside each group — the producer's order is the client's default
// sort, and the artifact is diffed by eye, so ties must not reshuffle.
func sortRows(ps []Player) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		switch {
		case (a.HKBValue == nil) != (b.HKBValue == nil):
			return a.HKBValue != nil
		case a.HKBValue != nil && *a.HKBValue != *b.HKBValue:
			return *a.HKBValue > *b.HKBValue
		}
		return a.Name < b.Name
	})
}

func sumValues(ps []Player) int {
	total := 0
	for _, p := range ps {
		if p.HKBValue != nil {
			total += *p.HKBValue
		}
	}
	return total
}

func ptr[T any](v T) *T { return &v }

// Package availablepool builds the Pickups screen's payload: every unowned
// Fantrax player, enriched with HKB dynasty value and 30-day momentum.
//
// It is the exact complement of tradeboard.BuildValuesTable, which skips free
// agents. Like that package it deliberately does not import internal/fantrax
// (which would drag chromedp in behind it) and declares its own subset of a
// pool row instead.
package availablepool

import (
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

// PoolPlayer is the subset of a Fantrax pool entry this package needs.
//
// FantasyStatus is carried raw because the pool feed puts HTML in it
// ("W <small>(Tue)</small>"); ParseStatus is the only thing that reads it, and
// no raw value reaches the artifact.
type PoolPlayer struct {
	ID      string
	Name    string
	MLBTeam string
	// Positions is the pool's raw PosShortNames ("SS,INF,UT"). Raw for the
	// same reason as FantasyStatus: upstream documents it as HTML and this
	// package owns the guarantee that no markup reaches the artifact.
	Positions      string
	FantasyStatus  string
	MinorsEligible bool
}

// Player is one row of the artifact.
type Player struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MLBTeam string `json:"mlb_team"`
	// Pos is HKB's position list. FantraxPos is the league's own eligibility,
	// which is a DIFFERENT FACT and the one that answers "can I slot him" —
	// the question this screen exists for. HKB says what a player is; Fantrax
	// says where this league will let him play. Clients prefer FantraxPos and
	// fall back to Pos.
	Pos        []string `json:"pos"`
	FantraxPos []string `json:"fantrax_pos,omitempty"`
	Level      string   `json:"level"`

	// ActiveLevels is HKB's own "MLB/AAA" shuttling marker. Level alone cannot
	// express it: Ryan Waldschmidt and Cooper Pratt both read Level "MLB" while
	// splitting the season with AAA, and both sit at the top of the mlb
	// segment — the worst place for the screen to be silent about it.
	ActiveLevels string `json:"active_levels,omitempty"`

	Prospect bool    `json:"prospect"`
	FYPD     bool    `json:"fypd"`
	Age      float64 `json:"age"`

	HKBValue int `json:"hkb_value"`
	HKBRank  int `json:"hkb_rank"`

	// RankChange30D and ValueChange30D are NORMALISED so positive always means
	// "better": climbed the board, or gained value. See the doc on
	// normaliseChanges — HKB's two raw fields use opposite formulas and only
	// coincide on that meaning because rank is inverted.
	RankChange30D  int `json:"rank_change_30d"`
	ValueChange30D int `json:"value_change_30d"`

	// RankHistory30D uses 0 as a "not ranked yet" sentinel, NOT as a rank.
	// RankHistoryStartsAt is the index of the first genuinely ranked entry, so
	// a client can plot from there instead of drawing a new entrant as having
	// fallen from the best possible rank.
	RankHistory30D      []int `json:"rank_history_30d,omitempty"`
	RankHistoryStartsAt int   `json:"rank_history_starts_at"`

	// FantasyStatus is "FA" or "W". WaiverClearsOn is the weekday a waiver
	// claim settles ("Tue"), empty for a true free agent.
	FantasyStatus  string `json:"fantasy_status"`
	WaiverClearsOn string `json:"waiver_clears_on,omitempty"`
}

// Decline is one player the namesake guard refused to value. WouldBeValue is
// the HKB value the wrong match WOULD have claimed, which is what makes the
// list a blast-radius report rather than 72 names: the top entry is the most
// dangerous row the guard prevented.
type Decline struct {
	Name         string `json:"name"`
	MLBTeam      string `json:"mlb_team"`
	Reason       string `json:"reason"`
	WouldBeValue int    `json:"would_be_value"`
}

// Counts is the join's denominator. Every number here exists so that a silent
// regression has somewhere to show up.
type Counts struct {
	MLB       int `json:"mlb"`
	Prospects int `json:"prospects"`
	// Matched is MLB + Prospects. Redundant by construction, and that is the
	// point: it is the denominator a client asserts the two segments sum to,
	// which is the assertion that catches a non-exhaustive segment rule. The
	// rule that shipped before this one was disjoint but not exhaustive and
	// dropped 67 players in silence.
	Matched          int `json:"matched"`
	Unmatched        int `json:"unmatched"`
	NamesakeDeclined int `json:"namesake_declined"`
}

// Table is the whole pool/available.json payload.
type Table struct {
	GeneratedAt time.Time `json:"generated_at"`
	HKBAsOf     string    `json:"hkb_as_of"`

	MLB       []Player `json:"mlb"`
	Prospects []Player `json:"prospects"`

	Counts   Counts    `json:"counts"`
	Declined []Decline `json:"declined,omitempty"`
}

// Build joins every unowned pool player onto HKB.
//
// pool must be the FULL league pool, owned players included. The namesake guard
// below can only see a contested name if the owned twin is present.
func Build(now time.Time, hkbAsOf string, pool []PoolPlayer, hkbPlayers []hkb.Player) Table {
	lookup := hkb.BuildLookup(hkbPlayers)

	// League-wide namesake census, over the whole pool.
	namesakes := make(map[string]int, len(pool))
	for _, pp := range pool {
		namesakes[playername.Normalize(pp.Name)]++
	}

	t := Table{GeneratedAt: now, HKBAsOf: hkbAsOf}
	for _, pp := range pool {
		if !IsUnowned(pp.FantasyStatus) {
			continue
		}
		hp, ok := lookup.FindFor(pp.Name, hkb.Hint{
			MLBTeam:        pp.MLBTeam,
			MinorsEligible: pp.MinorsEligible,
		})
		if !ok {
			t.Counts.Unmatched++
			continue
		}
		// hkb.Lookup.FindFor guards keys contested INSIDE HKB (rosterbot-5z7,
		// the Luis García case). It structurally cannot see a contest that
		// exists only on the Fantrax side: where HKB carries one row for a
		// name that Fantrax splits across two players, the key is uncontested
		// and FindFor returns a confident match for the wrong man.
		//
		// Measured live 2026-08-24: Fantrax carries Mason Miller 05ucd (owned,
		// the SD closer) and 06hw9 (free agent, a minor leaguer). HKB has one
		// Mason Miller. The unowned minor leaguer inherited the closer's 3177
		// and sorted to the top of this very list — the most prominent row in
		// the feature, indistinguishable from the jackpot it exists to surface.
		//
		// Declining is the honest answer: a miss renders as absent, a wrong
		// match renders as a confident number AND counts toward coverage.
		if namesakes[playername.Normalize(pp.Name)] > 1 {
			t.Counts.NamesakeDeclined++
			t.Declined = append(t.Declined, Decline{
				Name:         pp.Name,
				MLBTeam:      pp.MLBTeam,
				Reason:       "name shared by another Fantrax player",
				WouldBeValue: hp.Value,
			})
			continue
		}

		status, clears := ParseStatus(pp.FantasyStatus)
		rankChg, valueChg := normaliseChanges(hp)
		p := Player{
			ID:                  pp.ID,
			Name:                hp.Name,
			MLBTeam:             pp.MLBTeam,
			Pos:                 append([]string(nil), hp.Positions...),
			FantraxPos:          parsePositions(pp.Positions),
			Level:               hp.Level,
			ActiveLevels:        hp.ActiveLevels,
			Prospect:            hp.Prospect,
			FYPD:                hp.FYPD,
			Age:                 hp.Age,
			HKBValue:            hp.Value,
			HKBRank:             hp.Rank,
			RankChange30D:       rankChg,
			ValueChange30D:      valueChg,
			RankHistory30D:      append([]int(nil), hp.RankHistory30Days...),
			RankHistoryStartsAt: firstRanked(hp.RankHistory30Days),
			FantasyStatus:       status,
			WaiverClearsOn:      clears,
		}

		// Partitioned on Prospect alone: disjoint because it is a boolean, and
		// exhaustive for the same reason.
		//
		// The tempting rule — mlb = Level=="MLB", prospects = Prospect &&
		// Level!="MLB" — is disjoint but NOT exhaustive, and the gap is not
		// hypothetical. Measured live 2026-08-24, 67 unowned matched players
		// were Prospect==false with Level!="MLB": established major leaguers
		// optioned or rehabbing, whose HKB Level reads AAA/AA because that is
		// where they are currently playing. Quinn Priester (307), Clarke
		// Schmidt (172) and Félix Bautista (73) all vanished from both arrays
		// while counts.mlb + counts.prospects still looked healthy.
		if hp.Prospect {
			t.Prospects = append(t.Prospects, p)
		} else {
			t.MLB = append(t.MLB, p)
		}
	}

	sortByValue(t.MLB)
	sortByValue(t.Prospects)
	// Blast radius first: the value the guard stopped from reaching the top of
	// the list. Name ascending on ties keeps a daily diff stable.
	sort.Slice(t.Declined, func(i, j int) bool {
		if t.Declined[i].WouldBeValue != t.Declined[j].WouldBeValue {
			return t.Declined[i].WouldBeValue > t.Declined[j].WouldBeValue
		}
		return t.Declined[i].Name < t.Declined[j].Name
	})

	t.Counts.MLB = len(t.MLB)
	t.Counts.Prospects = len(t.Prospects)
	t.Counts.Matched = t.Counts.MLB + t.Counts.Prospects
	return t
}

// normaliseChanges returns (rankChange, valueChange) with positive meaning
// "improved" for both.
//
// HKB's two raw fields are computed with OPPOSITE formulas, verified against
// the live feed on 2026-08-24 (n=1729 rank rows, n=874 value rows):
//
//	rankChange30Days  == rankHistory30Days[0]  - rank    (1711/1729 exact)
//	valueChange30Days == value - valueHistory30Days[0]   (868/874 exact)
//
// Both nonetheless already mean "positive is good", because rank is inverted
// (lower is better) and value is not. So this function is a pass-through TODAY.
// It exists as a named seam anyway: the natural future "cleanup" is to make the
// two formulas symmetrical, which would silently flip rank and turn every
// riser badge in the client into a confident lie. TestSignConvention pins it.
//
// The residual rows in both counts are players whose history begins with the 0
// sentinel; HKB computes their change against the first ranked entry instead.
func normaliseChanges(hp hkb.Player) (rank, value int) {
	return hp.RankChange30Days, hp.ValueChange30Days
}

// firstRanked returns the index of the first day this player was ranked at all.
// HKB uses 0 to mean "not ranked yet", not "rank zero" — 23 of 1754 players
// carried leading zeros on 2026-08-24, and plotting those literally draws a new
// entrant as having fallen from the best possible rank.
//
// A history with no ranked entry returns len(hist), so slicing from it yields
// an empty series. Returning 0 there would assert the opposite of the truth —
// "ranked since day one" for a player never ranked at all.
func firstRanked(hist []int) int {
	for i, v := range hist {
		if v != 0 {
			return i
		}
	}
	return len(hist)
}

// IsUnowned mirrors internal/waivers' definition: a true free agent, an empty
// status, or any W-prefixed waiver-period value.
func IsUnowned(status string) bool {
	if status == "FA" || status == "" {
		return true
	}
	return strings.HasPrefix(status, "W")
}

// ParseStatus splits a raw pool status into a clean status and, for a waiver
// row, the weekday the claim settles.
//
// The pool feed puts MARKUP in this field — the live value is literally
// "W <small>(Tue)</small>" (20 players on 2026-08-24, Jameson Taillon among
// them). Passing it through verbatim would ship HTML to a client that badges
// it, the same trap as PosShortNames in the pool feed.
func ParseStatus(raw string) (status, clearsOn string) {
	if raw == "FA" || raw == "" {
		return "FA", ""
	}
	if !strings.HasPrefix(raw, "W") {
		return raw, ""
	}
	text := stripTags(raw)
	// "W (Tue)" -> day between the parentheses.
	if open := strings.Index(text, "("); open >= 0 {
		if close := strings.Index(text[open:], ")"); close > 0 {
			clearsOn = strings.TrimSpace(text[open+1 : open+close])
		}
	}
	return "W", clearsOn
}

// parsePositions turns the pool's PosShortNames into a clean list.
//
// PosShortNames is the right source despite the markup, and both obvious
// alternatives are wrong: MultiPositions is populated only for players eligible
// at more than one NAMED position (Juan Soto and Paul Skenes come back empty),
// and deriving from position IDs cannot reproduce Fantrax's own rule for when
// the UT flex slot is shown. cmd/team-values.go's positionDisplay says the same
// thing at length for the trades table.
func parsePositions(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Split(stripTags(raw), ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sortByValue orders a segment value-descending, name-ascending on ties. The
// artifact is rewritten daily and diffed by eye, so ties must not reshuffle.
func sortByValue(ps []Player) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].HKBValue != ps[j].HKBValue {
			return ps[i].HKBValue > ps[j].HKBValue
		}
		return ps[i].Name < ps[j].Name
	})
}

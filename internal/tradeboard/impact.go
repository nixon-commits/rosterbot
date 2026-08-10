package tradeboard

import (
	"fmt"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/playername"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

// Impact is what accepting an offer would do to my roster's value
// composition, and to where that composition ranks in the league.
//
// It exists because the verdict alone is not the decision. A trade can be
// dead even on HKB value and still be wrong: dealing your third-best arm for
// an equally-valued bat moves you from second to sixth in pitcher value while
// the headline number says the sides are level. Value tells you whether you
// are being robbed; this tells you what you end up owning.
type Impact struct {
	Team    string         `json:"team"`
	Metrics []ImpactMetric `json:"metrics"`
}

// ImpactMetric is one value dimension before and after the trade. Rank is
// 1-based and descending (rank 1 holds the most value); Teams is the field
// size, so the UI can render "3rd of 12" without assuming a league size.
type ImpactMetric struct {
	Name       string `json:"name"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
	Delta      int    `json:"delta"`
	RankBefore int    `json:"rank_before"`
	RankAfter  int    `json:"rank_after"`
	Teams      int    `json:"teams"`
}

// metrics are the five dimensions reported, in display order. Each reads a
// teamvalue.Row through its existing derived accessors rather than
// re-deriving the arithmetic here.
var metrics = []struct {
	name string
	of   func(teamvalue.Row) int
}{
	{"Total", teamvalue.Row.TotalValue},
	{"Hitters", teamvalue.Row.HitterValue},
	{"Pitchers", teamvalue.Row.PitcherValue},
	{"MLB", teamvalue.Row.MLBValue},
	{"Minors", teamvalue.Row.MinorsValue},
}

// BuildImpact replays an offer against the league's team-value rows and
// reports the before/after for myTeam.
//
// It returns (nil, reason) rather than a partial answer whenever anything
// cannot be resolved -- an unidentified draft pick, an asset on nobody's
// roster, a team name the values table does not know, or myTeam not being a
// participant. A composition delta computed with a missing player would look
// authoritative while being silently short by whatever that player is worth.
//
// The reason is returned rather than swallowed because suppression has to be
// legible. An offer can be fully PRICED and still have no impact: pricing
// needs only an HKB entry, while placing a player in a value leaf needs their
// current Fantrax roster row. A player dropped to free agency since the values
// table was built satisfies the first and not the second -- observed on
// 2026-08-09 with Heriberto Hernandez, who sat in a recorded offer, priced
// cleanly, and belonged to no team. Without a reason the tab would quietly
// omit the section and look like it had simply decided not to bother.
func BuildImpact(myTeam string, inputs []OfferInput, teams []teamvalue.Row, players []PlayerValue) (*Impact, string) {
	if len(inputs) == 0 || len(teams) == 0 {
		return nil, "no league value data yet"
	}

	byName := make(map[string]PlayerValue, len(players))
	for _, p := range players {
		byName[playername.Normalize(p.Name)] = p
	}

	before := make(map[string]teamvalue.Row, len(teams))
	after := make(map[string]teamvalue.Row, len(teams))
	for _, r := range teams {
		before[r.TeamName] = r
		after[r.TeamName] = r
	}
	if _, ok := before[myTeam]; !ok {
		return nil, fmt.Sprintf("%s is not in the league value table", myTeam)
	}

	for _, in := range inputs {
		pv, ok := byName[playername.Normalize(in.Player)]
		switch {
		case in.Player == "":
			return nil, "the offer includes an unidentified draft pick"
		case !ok:
			return nil, fmt.Sprintf("%s is not on any roster", in.Player)
		case !pv.Matched:
			return nil, fmt.Sprintf("%s has no HKB value", in.Player)
		}
		from, okFrom := after[in.From]
		to, okTo := after[in.To]
		if !okFrom || !okTo {
			return nil, "the offer names a team not in the league value table"
		}
		after[in.From] = addToLeaf(from, pv, -1)
		after[in.To] = addToLeaf(to, pv, +1)
	}

	imp := &Impact{Team: myTeam}
	for _, m := range metrics {
		b, a := before[myTeam], after[myTeam]
		imp.Metrics = append(imp.Metrics, ImpactMetric{
			Name:       m.name,
			Before:     m.of(b),
			After:      m.of(a),
			Delta:      m.of(a) - m.of(b),
			RankBefore: rankOf(before, myTeam, m.of),
			RankAfter:  rankOf(after, myTeam, m.of),
			Teams:      len(teams),
		})
	}
	return imp, ""
}

// addToLeaf moves one player's value and count into (sign +1) or out of
// (sign -1) the hitter/pitcher x MLB/minors leaf they belong to. The bucket
// choice mirrors teamvalue.Aggregate exactly: pitcher eligibility wins over
// hitter, minors eligibility splits within that.
func addToLeaf(r teamvalue.Row, pv PlayerValue, sign int) teamvalue.Row {
	v, c := sign*pv.Value, sign
	switch {
	case pv.IsPitcher && pv.IsMinors:
		r.PitcherMinorsValue += v
		r.PitcherMinorsCount += c
	case pv.IsPitcher:
		r.PitcherMLBValue += v
		r.PitcherMLBCount += c
	case pv.IsMinors:
		r.HitterMinorsValue += v
		r.HitterMinorsCount += c
	default:
		r.HitterMLBValue += v
		r.HitterMLBCount += c
	}
	r.RosteredCount += c
	r.MatchedCount += c
	return r
}

// rankOf is team's 1-based position when every team is ordered by metric
// descending. Ties break on team name so a rank never flips between runs on
// equal values.
func rankOf(rows map[string]teamvalue.Row, team string, metric func(teamvalue.Row) int) int {
	names := make([]string, 0, len(rows))
	for n := range rows {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		vi, vj := metric(rows[names[i]]), metric(rows[names[j]])
		if vi != vj {
			return vi > vj
		}
		return names[i] < names[j]
	})
	for i, n := range names {
		if n == team {
			return i + 1
		}
	}
	return 0
}

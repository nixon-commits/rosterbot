package recap

import (
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/transactions"
)

// SeasonExtras are the data sections of the year-end page that are derived
// rather than fetched: full-season efficiency from the rendered weeks, the
// trades of the year from the transactions ledger, and the season leaders
// reused from the latest week's board (same builder, same thresholds).
type SeasonExtras struct {
	Efficiency  []EfficiencyLine
	Trades      []TradeLine
	WOBALeaders []LeaderLine
	FIPLeaders  []LeaderLine
}

// EfficiencyLine is one team's full-season actual-vs-hindsight-optimal rate,
// pooled over the weeks it played, with that week count beside it.
type EfficiencyLine struct {
	Rank       int
	TeamID     string
	TeamName   string
	Weeks      int
	Actual     float64
	Optimal    float64
	Efficiency float64
}

// SeasonEfficiency pools each team over the weeks it had a scoring matchup:
// every regular-season week, and only the playoff rounds it was paired in.
// A bye or an eliminated week contributes nothing, so a team that went out
// early is not diluted by weeks it did not manage. Rates are value-weighted
// (sum of actual over sum of optimal), never a mean of weekly rates, the
// same rule the operator review uses.
func SeasonEfficiency(recaps []*Recap) []EfficiencyLine {
	acc := map[string]*EfficiencyLine{}
	for _, r := range recaps {
		played := map[string]bool{}
		for _, m := range r.Matchups {
			played[m.HomeTeamID] = true
			played[m.AwayTeamID] = true
		}
		for _, t := range r.Teams {
			if !played[t.TeamID] {
				continue
			}
			l, ok := acc[t.TeamID]
			if !ok {
				l = &EfficiencyLine{TeamID: t.TeamID, TeamName: t.TeamName}
				acc[t.TeamID] = l
			}
			l.Weeks++
			l.Actual += t.ActualPts
			l.Optimal += t.OptimalPts
		}
	}
	out := make([]EfficiencyLine, 0, len(acc))
	for _, l := range acc {
		if l.Optimal > 0 {
			l.Efficiency = l.Actual / l.Optimal
		}
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Efficiency != out[j].Efficiency {
			return out[i].Efficiency > out[j].Efficiency
		}
		return out[i].TeamID < out[j].TeamID
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// TradeLine is one executed trade on the year-end page.
type TradeLine struct {
	Date        time.Time
	Sides       []TradeSideLine
	Swing       int
	FavoredTeam string
}

// TradeSideLine is what one team received.
type TradeSideLine struct {
	TeamName string
	Total    int
	Players  []TradePlayerLine
}

// TradePlayerLine is one asset in a trade.
type TradePlayerLine struct {
	Name   string
	Value  int
	Ranked bool
	IsPick bool
}

// TradesOfTheYear ranks executed trades by HKB value swing — the gap between
// the largest and smallest side totals — and returns the top n with every
// asset on every side, so the page shows what moved rather than only who
// came out ahead. Ties break on the earlier date.
func TradesOfTheYear(trades []transactions.Trade, n int) []TradeLine {
	lines := make([]TradeLine, 0, len(trades))
	for _, t := range trades {
		if len(t.Sides) == 0 {
			continue
		}
		lo, hi := t.Sides[0].Total, t.Sides[0].Total
		l := TradeLine{Date: t.ProcessedDate, FavoredTeam: t.Verdict.FavoredTeam}
		for _, s := range t.Sides {
			if s.Total < lo {
				lo = s.Total
			}
			if s.Total > hi {
				hi = s.Total
			}
			side := TradeSideLine{TeamName: s.TeamName, Total: s.Total}
			for _, p := range s.Players {
				side.Players = append(side.Players, TradePlayerLine{Name: p.Name, Value: p.Value, Ranked: p.Ranked, IsPick: p.IsPick})
			}
			l.Sides = append(l.Sides, side)
		}
		l.Swing = hi - lo
		lines = append(lines, l)
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].Swing != lines[j].Swing {
			return lines[i].Swing > lines[j].Swing
		}
		return lines[i].Date.Before(lines[j].Date)
	})
	if n > 0 && len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

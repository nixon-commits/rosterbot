package transactions

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// toWireResult flattens grouped trades into the iOS wire shape. Each player
// carries the team they came FROM (their side); valuation is the HKB value.
func toWireResult(trades []Trade) lineupapi.TransactionsResult {
	out := lineupapi.TransactionsResult{}
	for _, tr := range trades {
		to := lineupapi.TradeOut{
			Teams:       []string{tr.Sides[0].TeamName, tr.Sides[1].TeamName},
			ProcessedAt: tr.ProcessedDate.UTC().Format(time.RFC3339),
		}
		for _, side := range tr.Sides {
			for _, p := range side.Players {
				to.Players = append(to.Players, lineupapi.TradePlayerOut{
					Name:      p.Name,
					FromTeam:  side.TeamName,
					Pos:       p.Position,
					Valuation: p.Value,
				})
			}
		}
		out.Trades = append(out.Trades, to)
	}
	return out
}

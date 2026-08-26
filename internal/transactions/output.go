package transactions

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"
)

// toWireResult flattens grouped trades into the iOS wire shape. Each player
// carries the team they came FROM (their side); valuation is the HKB value.
func toWireResult(trades []Trade) jobwire.TransactionsResult {
	out := jobwire.TransactionsResult{}
	for _, tr := range trades {
		// Every side, not the first two: Trade.Sides became a slice precisely
		// because a third team used to be dropped here as well.
		teams := make([]string, 0, len(tr.Sides))
		for _, s := range tr.Sides {
			teams = append(teams, s.TeamName)
		}
		to := jobwire.TradeOut{
			Teams:       teams,
			ProcessedAt: tr.ProcessedDate.UTC().Format(time.RFC3339),
		}
		for _, side := range tr.Sides {
			for _, p := range side.Players {
				to.Players = append(to.Players, jobwire.TradePlayerOut{
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

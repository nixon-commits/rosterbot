package claims

import (
	"sort"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/pmurley/go-fantrax/models"
)

// BuildMoves groups CLAIM/DROP transactions by transaction-set ID (Transaction.ID),
// valuing each added/dropped player via HKB. TRADE rows are ignored. The result
// is sorted by net value descending, ties broken by team then first added name
// then TxID for a total comparator (idempotency convention).
func BuildMoves(txs []models.Transaction, hkbLookup map[string]hkb.Player) []Move {
	byID := map[string]*Move{}
	var order []string

	for _, tx := range txs {
		if tx.Type != "CLAIM" && tx.Type != "DROP" {
			continue
		}
		m, ok := byID[tx.ID]
		if !ok {
			m = &Move{TxID: tx.ID, TeamName: tx.TeamName, TeamID: tx.TeamID, ProcessedDate: tx.ProcessedDate}
			byID[tx.ID] = m
			order = append(order, tx.ID)
		}
		// Team/date may only appear on the group's first (rowspan) row.
		if m.TeamName == "" {
			m.TeamName, m.TeamID = tx.TeamName, tx.TeamID
		}
		if m.ProcessedDate.IsZero() {
			m.ProcessedDate = tx.ProcessedDate
		}
		sp := lookupHKB(tx.PlayerName, tx.PlayerPosition, hkbLookup)
		switch tx.Type {
		case "CLAIM":
			if tx.ClaimType != "" {
				m.ClaimType = tx.ClaimType
			}
			if tx.BidAmount != "" {
				m.BidAmount = tx.BidAmount
			}
			if tx.Priority != "" {
				m.Priority = tx.Priority
			}
			m.Added = append(m.Added, sp)
		case "DROP":
			m.Dropped = append(m.Dropped, sp)
		}
	}

	moves := make([]Move, 0, len(order))
	for _, id := range order {
		moves = append(moves, *byID[id])
	}
	sort.SliceStable(moves, func(i, j int) bool {
		if moves[i].NetValue() != moves[j].NetValue() {
			return moves[i].NetValue() > moves[j].NetValue()
		}
		if moves[i].TeamName != moves[j].TeamName {
			return moves[i].TeamName < moves[j].TeamName
		}
		if firstAddedName(moves[i]) != firstAddedName(moves[j]) {
			return firstAddedName(moves[i]) < firstAddedName(moves[j])
		}
		return moves[i].TxID < moves[j].TxID
	})
	return moves
}

func firstAddedName(m Move) string {
	if len(m.Added) > 0 {
		return m.Added[0].Name
	}
	return ""
}

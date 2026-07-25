package fantrax

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/models"
)

// GetRecentTransactions returns CLAIM/DROP transactions processed after `since`.
// It wraps the auth client's CLAIM_DROP transaction view (distinct from
// GetAllTrades, which is the TRADE view). The full transaction list is cached
// under fantrax-all-transactions-<leagueID> with todayTTL — past transactions
// are immutable but today's batch can still grow, so the cache key is shared
// across all `since` values and the filter is applied to the cached payload.
func (c *Client) GetRecentTransactions(since time.Time) ([]models.Transaction, error) {
	all, err := c.allTransactions()
	if err != nil {
		return nil, fmt.Errorf("fetch transactions: %w", err)
	}
	return filterTransactionsSince(all, since), nil
}

func filterTransactionsSince(all []models.Transaction, since time.Time) []models.Transaction {
	var recent []models.Transaction
	for _, tx := range all {
		if tx.ProcessedDate.After(since) {
			recent = append(recent, tx)
		}
	}
	return recent
}

func (c *Client) allTransactions() ([]models.Transaction, error) {
	return cached(c, cache.Key(keyAllTransactions, c.leagueID), tierToday, c.auth.GetAllTransactions)
}

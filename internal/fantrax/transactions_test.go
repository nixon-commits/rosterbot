package fantrax

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

func TestFilterTransactionsSince(t *testing.T) {
	base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	all := []models.Transaction{
		{ID: "a", Type: "CLAIM", ProcessedDate: base.AddDate(0, 0, -2)},
		{ID: "b", Type: "CLAIM", ProcessedDate: base.AddDate(0, 0, 1)},
		{ID: "c", Type: "DROP", ProcessedDate: base.AddDate(0, 0, 2)},
	}
	got := filterTransactionsSince(all, base)
	if len(got) != 2 {
		t.Fatalf("want 2 transactions after cutoff, got %d", len(got))
	}
	for _, tx := range got {
		if !tx.ProcessedDate.After(base) {
			t.Errorf("transaction %s not after cutoff", tx.ID)
		}
	}
}

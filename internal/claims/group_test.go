package claims

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/pmurley/go-fantrax/models"
)

func TestBuildMoves_PairsClaimAndDropByTxID(t *testing.T) {
	d := time.Date(2026, 6, 12, 18, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{ID: "set1", Type: "CLAIM", ClaimType: "WW", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Added Guy", PlayerPosition: "OF", BidAmount: "12", Priority: "3", ProcessedDate: d},
		{ID: "set1", Type: "DROP", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Dropped Guy", PlayerPosition: "SP", ProcessedDate: d},
		{ID: "set2", Type: "CLAIM", ClaimType: "FA", TeamName: "Bandits", TeamID: "t2",
			PlayerName: "Solo Add", PlayerPosition: "1B", ProcessedDate: d},
	}
	lookup := buildHKBLookup([]hkb.Player{
		{Name: "Added Guy", Value: 3000},
		{Name: "Dropped Guy", Value: 1000},
	})

	moves := BuildMoves(txs, lookup)
	if len(moves) != 2 {
		t.Fatalf("want 2 moves, got %d", len(moves))
	}

	// Moves are sorted by NetValue desc; set1 = 3000-1000 = 2000 leads.
	m := moves[0]
	if m.TeamName != "Aces" || m.ClaimType != "WW" || m.BidAmount != "12" {
		t.Errorf("unexpected move metadata: %+v", m)
	}
	if len(m.Added) != 1 || len(m.Dropped) != 1 {
		t.Fatalf("want 1 add + 1 drop, got %d/%d", len(m.Added), len(m.Dropped))
	}
	if m.NetValue() != 2000 {
		t.Errorf("want net 2000, got %d", m.NetValue())
	}

	// set2 is a bare add (no drop).
	if len(moves[1].Dropped) != 0 || moves[1].NetValue() != 0 {
		t.Errorf("bare add should have no drops and net 0: %+v", moves[1])
	}
}

func TestBuildMoves_IgnoresTradeRows(t *testing.T) {
	d := time.Date(2026, 6, 12, 18, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{ID: "trade1", Type: "TRADE", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Trade Guy", PlayerPosition: "SP", ProcessedDate: d},
		{ID: "claim1", Type: "CLAIM", ClaimType: "FA", TeamName: "Bandits", TeamID: "t2",
			PlayerName: "Waiver Guy", PlayerPosition: "OF", ProcessedDate: d},
	}
	lookup := buildHKBLookup([]hkb.Player{
		{Name: "Trade Guy", Value: 9999},
		{Name: "Waiver Guy", Value: 2000},
	})

	moves := BuildMoves(txs, lookup)
	if len(moves) != 1 {
		t.Fatalf("want 1 move (TRADE ignored), got %d", len(moves))
	}
	if moves[0].TxID != "claim1" {
		t.Errorf("expected claim1, got %s", moves[0].TxID)
	}
}

func TestBuildMoves_DeterministicOrder(t *testing.T) {
	d := time.Date(2026, 6, 12, 18, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{ID: "aaa", Type: "CLAIM", ClaimType: "FA", TeamName: "Alpha", TeamID: "t1",
			PlayerName: "Player A", PlayerPosition: "OF", ProcessedDate: d},
		{ID: "bbb", Type: "CLAIM", ClaimType: "WW", TeamName: "Beta", TeamID: "t2",
			PlayerName: "Player B", PlayerPosition: "SP", ProcessedDate: d},
		{ID: "ccc", Type: "CLAIM", ClaimType: "FA", TeamName: "Gamma", TeamID: "t3",
			PlayerName: "Player C", PlayerPosition: "1B", ProcessedDate: d},
	}
	lookup := buildHKBLookup([]hkb.Player{
		{Name: "Player A", Value: 5000},
		{Name: "Player B", Value: 5000},
		{Name: "Player C", Value: 3000},
	})

	moves1 := BuildMoves(txs, lookup)
	moves2 := BuildMoves(txs, lookup)

	if len(moves1) != len(moves2) {
		t.Fatalf("length mismatch: %d vs %d", len(moves1), len(moves2))
	}
	for i := range moves1 {
		if moves1[i].TxID != moves2[i].TxID {
			t.Errorf("position %d: got %s then %s — not deterministic", i, moves1[i].TxID, moves2[i].TxID)
		}
	}
}

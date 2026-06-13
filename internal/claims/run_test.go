package claims

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/pmurley/go-fantrax/models"
)

type fakeClient struct{ txs []models.Transaction }

func (f fakeClient) GetRecentTransactions(since time.Time) ([]models.Transaction, error) {
	return f.txs, nil
}

func TestRun_NoClaimsIsNoop(t *testing.T) {
	dir := t.TempDir()
	ledgerDir := filepath.Join(dir, "claims")
	today := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	opts := Options{
		CacheDir:   dir,
		DryRun:     true,
		NoSignals:  true,
		Since:      today.AddDate(0, 0, -1),
		LedgerDir:  ledgerDir,
		CursorPath: filepath.Join(dir, "last-claims.json"),
		HKBPlayers: []hkb.Player{}, // non-nil → skip network
	}
	if err := Run(fakeClient{txs: nil}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(ledgerDir); !os.IsNotExist(err) {
		t.Errorf("expected no ledger dir on no-op, stat err = %v", err)
	}
	if loadCursor(opts.CursorPath).IsZero() {
		t.Error("cursor should advance even on no-op")
	}
}

func TestRun_WritesLedgerWhenClaimsExist(t *testing.T) {
	dir := t.TempDir()
	ledgerDir := filepath.Join(dir, "claims")
	today := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	d := today.Add(-2 * time.Hour)

	txs := []models.Transaction{
		{ID: "s1", Type: "CLAIM", ClaimType: "FA", TeamName: "Aces", TeamID: "t1",
			PlayerName: "Some Guy", PlayerPosition: "OF", ProcessedDate: d},
	}
	opts := Options{
		CacheDir: dir, DryRun: false, NoSignals: true,
		Since:      today.AddDate(0, 0, -1),
		LedgerDir:  ledgerDir,
		CursorPath: filepath.Join(dir, "last-claims.json"),
		HKBPlayers: []hkb.Player{{Name: "Some Guy"}},
	}
	if err := Run(fakeClient{txs: txs}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerDir, "2026-06-12.json")); err != nil {
		t.Errorf("expected ledger file written: %v", err)
	}
	if loadCursor(opts.CursorPath).IsZero() {
		t.Error("cursor should advance after a successful run")
	}
}

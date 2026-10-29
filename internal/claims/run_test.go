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

func noopOptions(dir string, today time.Time, dryRun bool) Options {
	return Options{
		CacheDir:   dir,
		DryRun:     dryRun,
		NoSignals:  true,
		Since:      today.AddDate(0, 0, -1),
		LedgerDir:  filepath.Join(dir, "claims"),
		CursorPath: filepath.Join(dir, "last-claims.json"),
		HKBPlayers: []hkb.Player{}, // non-nil → skip network
	}
}

func TestRun_NoClaimsIsNoop(t *testing.T) {
	dir := t.TempDir()
	today := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	opts := noopOptions(dir, today, false)

	if err := Run(t.Context(), fakeClient{txs: nil}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(opts.LedgerDir); !os.IsNotExist(err) {
		t.Errorf("expected no ledger dir on no-op, stat err = %v", err)
	}
	if loadCursor(opts.CursorPath).IsZero() {
		t.Error("cursor should advance even on no-op")
	}
}

// A dry run is side-effect-free, and the cursor is a side effect: advancing
// it would make the next real run skip the claims this one only looked at.
func TestRun_DryRunNoopLeavesTheCursorAlone(t *testing.T) {
	dir := t.TempDir()
	today := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	opts := noopOptions(dir, today, true)

	if err := Run(t.Context(), fakeClient{txs: nil}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !loadCursor(opts.CursorPath).IsZero() {
		t.Error("a dry run must not advance the claims cursor")
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
	if err := Run(t.Context(), fakeClient{txs: txs}, today, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerDir, "2026-06-12.json")); err != nil {
		t.Errorf("expected ledger file written: %v", err)
	}
	if loadCursor(opts.CursorPath).IsZero() {
		t.Error("cursor should advance after a successful run")
	}
}

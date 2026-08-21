package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

// rosterbot-iqso moved projection snapshots off cmd/sync.go's bulk directory
// sync onto a typed store, which changed backtestSnapshotDir from a filesystem
// path (".backtest/snapshots") into a partition name relative to the store
// root ("snapshots"). The local bytes must land in exactly the same place they
// always did: developers have existing .backtest/snapshots/ trees, and
// `backtest` reads them. A silent path change would not fail anything — it
// would just quietly grade nothing and report every day as "missing".
func TestLocalSnapshotPathIsUnchangedByTheStoreMigration(t *testing.T) {
	t.Setenv("STATE_BUCKET", "") // local mode, the case this test is about

	if _, err := statestore.FromEnv().SnapshotStore(); err != nil {
		t.Fatalf("SnapshotStore: %v", err)
	}

	// Root the store inside the test's temp dir by running there, so the
	// assertion is about the RELATIVE layout rather than the repo's own
	// .backtest/ contents.
	dir := t.TempDir()
	t.Chdir(dir)

	// Re-resolve after the chdir: the file store holds a relative root.
	st, err := statestore.FromEnv().SnapshotStore()
	if err != nil {
		t.Fatalf("SnapshotStore: %v", err)
	}

	date := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	if err := backtest.WriteSnapshot(st, backtestSnapshotDir, backtest.Snapshot{Date: "2026-08-18"}); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	want := filepath.Join(dir, ".backtest", "snapshots", "2026-08-18.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("snapshot did not land at the historical path %s: %v", want, err)
	}

	if _, ok := backtest.LoadSnapshot(st, backtestSnapshotDir, date); !ok {
		t.Fatal("LoadSnapshot could not read back what WriteSnapshot just wrote")
	}
}

// The shadow capture's per-system partition must likewise keep its historical
// local layout, since grade reads it for every system it compares.
func TestLocalShadowSnapshotPathIsUnchanged(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")

	dir := t.TempDir()
	t.Chdir(dir)

	st, err := statestore.FromEnv().SnapshotStore()
	if err != nil {
		t.Fatalf("SnapshotStore: %v", err)
	}
	part := systemSnapshotDir(shadowSnapshotRoot, "atc-ros")
	if err := backtest.WriteSnapshot(st, part, backtest.Snapshot{Date: "2026-08-18"}); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	want := filepath.Join(dir, ".backtest", "snapshots-systems", "system=atc-ros", "2026-08-18.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("shadow snapshot did not land at the historical path %s: %v", want, err)
	}
}

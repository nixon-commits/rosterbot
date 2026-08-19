package statestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chdir roots the relative LocalDirs the artifact table declares (".backtest",
// ".analysis", ...) inside a temp dir, so these tests are about the resolution
// rule rather than the repo's own state trees.
func chdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	return dir
}

// TestLocalReadRefusesWhenTenantDataSitsBesideTheLegacyPath is rosterbot-4b9.
//
// The crq.11 backfill relocated every per-tenant artifact under user=<uid>/ and
// left the pre-migration copy in place. That copy is FROZEN, not merely
// duplicated, so resolving it is not a stale read that self-corrects — the gap
// widens by one day per day. Locally ROSTERBOT_USER_ID is never set, so the
// empty-tenant fallback silently routed every read to it: a backtest over
// 2026-08-17:08-18 reported both days "missing (no snapshot written)" while the
// tenant snapshots for those days sat one directory away.
//
// The deployed side is already covered — resolveCredentialMode refuses a
// tenant-less run whenever IDENTITY_TABLE is set, before any store is built.
// Local dev has no user directory, so nothing refused it there.
func TestLocalReadRefusesWhenTenantDataSitsBesideTheLegacyPath(t *testing.T) {
	root := chdir(t)
	if err := os.MkdirAll(filepath.Join(root, ".backtest", "user=u123", "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := New("").SnapshotStore()
	if err == nil {
		t.Fatal("SnapshotStore with no tenant succeeded while .backtest/user=u123/ exists: " +
			"the read resolves the frozen pre-migration path and grades days as missing")
	}
	for _, want := range []string{"ROSTERBOT_USER_ID", "u123", ".backtest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q — it has to name what to set and what it found", err, want)
		}
	}
}

// A machine that has never synced tenant data has no ambiguity to resolve, so
// the un-segmented path is the only data there is. Absence of evidence is not
// evidence: refusing here would break every existing local tree and every
// hermetic test for a mistake nobody made.
func TestLocalReadAllowsTheLegacyPathWhenNoTenantDataExists(t *testing.T) {
	root := chdir(t)
	if err := os.MkdirAll(filepath.Join(root, ".backtest", "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := New("").SnapshotStore(); err != nil {
		t.Fatalf("SnapshotStore on a tenant-free tree = %v, want success", err)
	}
	// A directory that does not exist at all is the fresh-machine case.
	chdir(t)
	if _, err := New("").SnapshotStore(); err != nil {
		t.Fatalf("SnapshotStore with no .backtest at all = %v, want success", err)
	}
}

// The guard is about attribution, not about the segment existing: a run that
// says who it is reads its own data and never consults the legacy path.
func TestDeclaringTheTenantResolvesTheLiveSegment(t *testing.T) {
	root := chdir(t)
	if err := os.MkdirAll(filepath.Join(root, ".backtest", "user=u123", "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ForTenant("", "u123").SnapshotStore(); err != nil {
		t.Fatalf("SnapshotStore for the declared tenant = %v, want success", err)
	}
}

// The fix belongs to the whole PerTenant class, not to projection snapshots.
// Only .backtest exhibits it today, because it is the only prefix anyone has
// synced down; every other per-tenant prefix arms the moment someone does.
func TestTheGuardCoversEveryPerTenantArtifact(t *testing.T) {
	for name, dir := range map[string]string{
		"analysis":  ".analysis",
		"lineupgap": ".lineupgap",
		"backtest":  ".backtest",
		"reports":   ".reports",
	} {
		t.Run(name, func(t *testing.T) {
			root := chdir(t)
			if err := os.MkdirAll(filepath.Join(root, dir, "user=u123"), 0o755); err != nil {
				t.Fatal(err)
			}
			s := New("")
			var err error
			switch name {
			case "analysis":
				_, err = s.AnalysisReader()
			case "lineupgap":
				_, err = s.LineupGapReader()
			case "backtest":
				_, err = s.SnapshotStore()
			case "reports":
				_, err = s.ReportsStore()
			}
			if err == nil {
				t.Errorf("%s resolved the frozen legacy path with tenant data beside it", name)
			}
		})
	}
}

// A shared artifact is deliberately common ground (the TTL Cache, the
// league-wide value stores). It never gains the segment even when a tenant is
// set, so a user= directory beside it is not evidence of anything.
func TestSharedArtifactsAreUnaffected(t *testing.T) {
	root := chdir(t)
	if err := os.MkdirAll(filepath.Join(root, ".teamvalue", "user=u123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New("").TeamValueReader(); err != nil {
		t.Fatalf("TeamValueReader on a shared artifact = %v, want success", err)
	}
}

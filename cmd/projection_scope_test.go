package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectionScope_TenantNeverCreatesThePublicDir is the trap in splitting
// this command, and it would destroy data rather than merely misbehave.
//
// entrypoint.sh runs `sync up` after EVERY command, and publishSite mirrors
// ./report into DASHBOARD_BUCKET with --delete. It no-ops when the directory is
// absent — but runProjectionSite called MkdirAll unconditionally. So a
// per-tenant run, which has nothing to publish publicly, would create an empty
// ./report and the sync would then delete value.json and football.json for the
// whole league.
//
// The scope therefore decides whether the directory is created at all, not just
// what goes in it.
func TestProjectionScope_TenantNeverCreatesThePublicDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report")

	if scopeWritesPublicDir(scopeTenant) {
		t.Fatal("the tenant scope claims the public dir; an empty ./report would be " +
			"mirrored with --delete and wipe value.json and football.json")
	}
	if err := ensurePublicDir(scopeTenant, dir); err != nil {
		t.Fatalf("ensurePublicDir: %v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the tenant scope created ./report")
	}
}

// TestProjectionScope_LeagueCreatesIt: the league-wide half owns value.json and
// football.json, so it must create the directory even when a renderer later
// soft-fails — publishSite would otherwise skip a run that legitimately had
// something to say.
func TestProjectionScope_LeagueCreatesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report")

	if !scopeWritesPublicDir(scopeLeague) {
		t.Fatal("the league scope does not claim the public dir")
	}
	if err := ensurePublicDir(scopeLeague, dir); err != nil {
		t.Fatalf("ensurePublicDir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the league scope did not create %s: %v", dir, err)
	}
}

// TestProjectionScope_Split covers what each half is responsible for. The two
// must partition the work: anything in neither is silently never produced,
// and anything in both is written twice — once per tenant, which for a
// league-wide artifact means N tasks racing on one key.
func TestProjectionScope_Split(t *testing.T) {
	for _, tc := range []struct {
		scope                   projectionScope
		wantPrivate, wantPublic bool
	}{
		{scopeAll, true, true},
		{scopeTenant, true, false},
		{scopeLeague, false, true},
	} {
		if got := scopeWritesPrivateReports(tc.scope); got != tc.wantPrivate {
			t.Errorf("%s: private = %v, want %v", tc.scope, got, tc.wantPrivate)
		}
		if got := scopeWritesPublicDir(tc.scope); got != tc.wantPublic {
			t.Errorf("%s: public = %v, want %v", tc.scope, got, tc.wantPublic)
		}
	}
}

// TestProjectionScope_RejectsAnUnknownScope. The value comes from an
// EventBridge rule's static argv, so a typo is a wiring mistake. Defaulting to
// "all" would make a mistyped per-tenant schedule quietly publish league-wide
// artifacts from N tenants at once.
func TestProjectionScope_RejectsAnUnknownScope(t *testing.T) {
	if _, err := parseProjectionScope("tenat"); err == nil {
		t.Error("a mistyped scope was accepted; N tenants would race on the public prefix")
	}
	for _, ok := range []string{"all", "tenant", "league"} {
		if _, err := parseProjectionScope(ok); err != nil {
			t.Errorf("parseProjectionScope(%q): %v", ok, err)
		}
	}
}

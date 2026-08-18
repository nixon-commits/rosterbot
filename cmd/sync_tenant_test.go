package cmd

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// TestStatePairsFor_ScopesPerTenantPrefixes is a security test.
//
// The bulk dir<->prefix sync in entrypoint.sh is the ONLY writer that did not
// go through statestore's typed constructors, so it was the only one that never
// composed the tenant segment. Its prefixes were string literals.
//
// For session/ that is cross-tenant credential sharing, not untidiness. Every
// task syncs its .fantrax-cache/ up at the end of a run and pulls it down at
// the start of the next. With one shared session/ prefix, tenant B's task boots
// holding tenant A's Fantrax FX_RM cookie and acts as them — while every job
// reports success. Measured on the live bucket 2026-08-14: both
// session/.fantrax_cookie_cache.json and session/user=<uid>/... carried the
// same write timestamp, i.e. the un-tenanted copy is actively maintained rather
// than left over from the crq.11 backfill.
//
// backtest/ is the same mechanism with lower stakes: projection snapshots
// mixing between tenants rather than a credential.
func TestStatePairsFor_ScopesPerTenantPrefixes(t *testing.T) {
	const tenant = "alice"
	pairs := statePairsFor(tenant)

	byDir := map[string]string{}
	for _, p := range pairs {
		byDir[p.dir] = p.prefix
	}

	for _, tc := range []struct {
		dir  string
		want string
	}{
		{".fantrax-cache/", "session/user=alice/"},
		{".backtest/", "backtest/user=alice/"},
	} {
		got, ok := byDir[tc.dir]
		if !ok {
			t.Fatalf("no sync pair for %s", tc.dir)
		}
		if got != tc.want {
			t.Errorf("%s -> %q, want %q", tc.dir, got, tc.want)
		}
	}
}

// TestStatePairsFor_LeavesSharedPrefixesAlone: claims/ and archive/ are NOT
// PerTenant. claims is a league-wide transaction ledger and archive holds
// upstream snapshots that are identical for everyone, so scoping them per
// tenant would duplicate storage and split a shared history for no reason.
//
// This is the other half of the property: the fix must scope exactly the
// artifacts the layout declares PerTenant, not everything it syncs.
func TestStatePairsFor_LeavesSharedPrefixesAlone(t *testing.T) {
	pairs := statePairsFor("alice")
	for _, p := range pairs {
		switch p.dir {
		case ".waivers/", ".archive/":
			if strings.Contains(p.prefix, "user=") {
				t.Errorf("%s -> %q: a non-PerTenant artifact was tenant-scoped", p.dir, p.prefix)
			}
		}
	}
}

// TestStatePairsFor_EmptyTenantIsUnchanged pins backward compatibility for
// single-tenant deployments and local dev, where there is no tenant at all.
// PrefixFor already returns the bare prefix for an empty tenant; this asserts
// the sync inherits that rather than emitting a "user=/" segment.
func TestStatePairsFor_EmptyTenantIsUnchanged(t *testing.T) {
	for _, p := range statePairsFor("") {
		if strings.Contains(p.prefix, "user=") {
			t.Errorf("%s -> %q: an empty tenant produced a tenant segment", p.dir, p.prefix)
		}
	}
}

// TestStatePairsFor_MatchesTheLayoutDeclaration is the guard that keeps this
// from drifting again.
//
// The original bug was a second, independent statement of the same fact: the
// layout said PerTenant, and cmd/sync.go said "session/" in a string literal.
// Nothing made them agree, so they didn't. Deriving the prefixes from the
// layout artifacts is the fix; this test fails if someone reintroduces a
// literal, by checking each pair against what the layout itself would produce.
func TestStatePairsFor_MatchesTheLayoutDeclaration(t *testing.T) {
	const tenant = "alice"
	want := map[string]string{
		".fantrax-cache/": layout.Session.PrefixFor(tenant),
		".waivers/":       layout.Claims.PrefixFor(tenant),
		".backtest/":      layout.Backtest.PrefixFor(tenant),
		// No ".archive/": rosterbot-s25n took the Daily Archive out of this bulk
		// sync and gave it a typed per-partition store, because every task was
		// downloading the whole 877 MB tree at startup for a directory only
		// cmd/archive.go ever touches. Its absence here is the assertion.
	}
	pairs := statePairsFor(tenant)
	if len(pairs) != len(want) {
		t.Fatalf("statePairsFor returned %d pairs, want %d", len(pairs), len(want))
	}
	for _, p := range pairs {
		w, ok := want[p.dir]
		if !ok {
			t.Errorf("unexpected sync dir %q", p.dir)
			continue
		}
		if p.prefix != w {
			t.Errorf("%s -> %q, but the layout says %q", p.dir, p.prefix, w)
		}
	}
}

// TestSyncPairs_ReadsTheTenantFromTheEnvironment closes the gap the tests above
// leave: they exercise statePairsFor, and a call site that passed "" instead of
// the real tenant kept every one of them green. The tenant is now read inside
// syncPairs, so there is no argument for a caller to get wrong, and this
// asserts that reading actually happens.
func TestSyncPairs_ReadsTheTenantFromTheEnvironment(t *testing.T) {
	t.Setenv("ROSTERBOT_USER_ID", "alice")

	var session string
	for _, p := range syncPairs() {
		if p.dir == ".fantrax-cache/" {
			session = p.prefix
		}
	}
	if session != "session/user=alice/" {
		t.Fatalf("session prefix = %q, want session/user=alice/ — syncPairs is not reading the tenant", session)
	}
}

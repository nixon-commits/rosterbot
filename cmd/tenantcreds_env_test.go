package cmd

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// operatorEnv reproduces what every Fargate task actually starts with.
//
// infra.go injects the DEPLOYMENT's FANTRAX_USERNAME and FANTRAX_PASSWORD from
// SSM into the task definition, so they are present in the environment of a
// tenant's job as much as the operator's own. That matters because
// auth_client reads them with os.Getenv directly and never consults
// config.Config: clearing cfg leaves the credentials it will actually use
// untouched.
func operatorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FANTRAX_USERNAME", "operator@example.test")
	t.Setenv("FANTRAX_PASSWORD", "operator-pass")
	t.Setenv("FANTRAX_COOKIES", "FX_RM=operator-session")
}

// TestApplyTenantCredentials_InstallsTheTenantsSessionInTheEnvironment is
// rosterbot-crq.17 acceptance criterion 4: the FX_RM captured by connect must be
// the one a subsequent job uses.
//
// It was not. The sealed cookie was written, length-checked, copied into
// RunGrant and dropped — a secret with no consumer. What actually carried a
// session between runs was the PLAINTEXT .fantrax_cookie_cache.json file synced
// to S3, which meant the KMS envelope the design rests on was decorative.
//
// auth_client.GetCookies() checks FANTRAX_COOKIES before its cache file and
// before the browser, so installing the decrypted cookie there makes the sealed
// copy the real channel without touching the fork.
func TestApplyTenantCredentials_InstallsTheTenantsSessionInTheEnvironment(t *testing.T) {
	operatorEnv(t)
	cfg := envConfig()
	conns := stubConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}

	if err := applyTenantCredentials(context.Background(), cfg, "alice", conns, stubOpener{}); err != nil {
		t.Fatalf("applyTenantCredentials: %v", err)
	}

	if got, want := os.Getenv("FANTRAX_COOKIES"), "FX_RM=tenant-fxrm"; got != want {
		t.Errorf("FANTRAX_COOKIES = %q, want %q — the sealed session is still unused", got, want)
	}
}

// TestApplyTenantCredentials_OverwritesTheOperatorsEnvironment closes the half
// of criterion 1 that the config-level fix could not reach.
//
// AuthorizeRun and the cfg clearing stop a tenant's job from running with the
// operator's credentials THROUGH CONFIG. They do nothing about auth_client's own
// os.Getenv reads: if GetCookies falls through to the browser — a tenant's first
// run, or one after their cached cookie ages out — it logs in with whatever
// FANTRAX_USERNAME is in the environment, which on Fargate is the operator's.
// The job would then act as the operator against the tenant's team, which is
// exactly the failure the run gate was built to prevent.
func TestApplyTenantCredentials_OverwritesTheOperatorsEnvironment(t *testing.T) {
	operatorEnv(t)
	cfg := envConfig()
	conns := stubConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}

	if err := applyTenantCredentials(context.Background(), cfg, "alice", conns, stubOpener{}); err != nil {
		t.Fatalf("applyTenantCredentials: %v", err)
	}

	if got := os.Getenv("FANTRAX_USERNAME"); got != "tenant@example.test" {
		t.Errorf("FANTRAX_USERNAME = %q; a browser fallback would authenticate as %q", got, got)
	}
	if got := os.Getenv("FANTRAX_PASSWORD"); got != "tenant-pass" {
		t.Errorf("FANTRAX_PASSWORD is not the tenant's; a browser fallback would use the wrong account")
	}
}

// TestApplyTenantCredentials_RefusalsLeaveNoUsableEnvironment is the fail-safe
// property, asserted one layer below its config-level sibling.
//
// After a refusal there must be no way for anything downstream to authenticate
// as ANYONE. Leaving the operator's variables in place would mean a caller that
// ignored the error still got a working Fantrax session — the wrong one — and
// leaving a stale tenant cookie would be no better.
func TestApplyTenantCredentials_RefusalsLeaveNoUsableEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		conns  stubConns
		opener stubOpener
	}{
		{"never connected", stubConns{}, stubOpener{}},
		{"still pending", stubConns{conn: connFor(t, "alice", lineupapi.ConnPending)}, stubOpener{}},
		{"needs reconnect", stubConns{conn: connFor(t, "alice", lineupapi.ConnNeedsReconnect)}, stubOpener{}},
		{"record for another tenant", stubConns{conn: connFor(t, "bob", lineupapi.ConnVerified)}, stubOpener{}},
		{"store unavailable", stubConns{err: errors.New("dynamo down")}, stubOpener{}},
		{"decrypt refused", stubConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}, stubOpener{err: errors.New("kms denied")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operatorEnv(t)
			cfg := envConfig()

			if err := applyTenantCredentials(context.Background(), cfg, "alice", tc.conns, tc.opener); err == nil {
				t.Fatal("expected a refusal")
			}

			for _, v := range []string{"FANTRAX_USERNAME", "FANTRAX_PASSWORD", "FANTRAX_COOKIES"} {
				if got := os.Getenv(v); got != "" {
					t.Errorf("%s = %q after a refusal; auth_client would still authenticate with it", v, got)
				}
			}
		})
	}
}

// TestFantraxCookieHeader pins the wire format against the fork's own
// convertCookiesToString, which is what produces this header on the path that
// does NOT go through us. The two must agree, or a session that works when
// minted by the browser breaks when replayed from the sealed copy.
func TestFantraxCookieHeader(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain value", "abc123", "FX_RM=abc123"},
		{"quoted value is unwrapped, as the fork does", `"abc123"`, "FX_RM=abc123"},
		{"empty stays empty rather than sending a headerless cookie", "", ""},
		{"a lone quote is not treated as a wrapper", `"`, `FX_RM="`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fantraxCookieHeader(tc.in); got != tc.want {
				t.Errorf("fantraxCookieHeader(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

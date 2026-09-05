package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testToken = "break-glass-token"

var testSecret = []byte("test-session-secret")

// authzFixture builds a Handler backed by a real FileUserStore, plus a helper
// to mint a session cookie for a given user.
func authzFixture(t *testing.T) (http.Handler, *FileUserStore) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	h := Handler(Config{
		Token:         testToken,
		SessionSecret: testSecret,
		Users:         users,
		Infra:         nil, // admin route reached but unconfigured -> 501, which is past the gate
	})
	return h, users
}

func seedUser(t *testing.T, users *FileUserStore, id UserID, role Role, status UserStatus) *User {
	t.Helper()
	u := &User{ID: id, Email: string(id) + "@example.test", Role: role, Status: status}
	if err := users.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func reqWithSession(t *testing.T, method, path string, sub UserID, ver int) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: signSession(testSecret, sub, ver, time.Now())})
	return r
}

func status(h http.Handler, r *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

// TestBearerTokenWorksWithoutAUserStore is THE safety property of this change.
//
// Once routes resolve a caller by loading the user record, three new things can
// lock the operator out: the migration not yet run in production, DynamoDB
// unreachable, or a bug in resolveCaller. The bearer token is the recovery from
// all three, and it can only be that if it never reads the store. A nil Users
// here stands in for every one of those failures at once.
func TestBearerTokenWorksWithoutAUserStore(t *testing.T) {
	h := Handler(Config{Token: testToken, SessionSecret: testSecret, Users: nil})

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/runs", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	if got := status(h, r); got == http.StatusUnauthorized {
		t.Fatal("the bearer token was refused with no user store configured. It is " +
			"the break-glass for exactly that situation; routing it through a user " +
			"lookup means the dashboard is needed to fix the dashboard")
	}
}

// TestSessionRefusedWithoutAUserStore is the other half. The token must survive
// a missing store; a SESSION must not, because a session names a user and there
// is nothing to resolve it against. Serving it as some default principal is the
// failure this guards.
func TestSessionRefusedWithoutAUserStore(t *testing.T) {
	h := Handler(Config{Token: testToken, SessionSecret: testSecret, Users: nil})
	if got := status(h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0)); got != http.StatusUnauthorized {
		t.Fatalf("session with no user store = %d, want 401", got)
	}
}

func TestSessionForUnknownUserIsRefused(t *testing.T) {
	h, _ := authzFixture(t)
	if got := status(h, reqWithSession(t, http.MethodGet, "/v1/runs", "nobody", 0)); got != http.StatusUnauthorized {
		t.Fatalf("session naming an unknown user = %d, want 401", got)
	}
}

// TestStaleTokenVersionIsRefused is revocation actually working. rosterbot-crq.8
// put Ver in the cookie; this is the first code that compares it to anything.
func TestStaleTokenVersionIsRefused(t *testing.T) {
	h, users := authzFixture(t)
	u := seedUser(t, users, "alice", RoleMember, UserActive)

	if got := status(h, reqWithSession(t, http.MethodGet, "/v1/runs", u.ID, 0)); got == http.StatusUnauthorized {
		t.Fatal("a current session was refused before revocation")
	}

	u.TokenVersion = 1
	if err := users.PutUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := status(h, reqWithSession(t, http.MethodGet, "/v1/runs", u.ID, 0)); got != http.StatusUnauthorized {
		t.Fatalf("session minted at ver 0 against a user now at ver 1 = %d, want 401. "+
			"Without this, incrementing TokenVersion revokes nothing and there is no "+
			"way to log a compromised device out", got)
	}
}

func TestParkedUserIsRefused(t *testing.T) {
	h, users := authzFixture(t)
	u := seedUser(t, users, "bob", RoleMember, UserParked)
	if got := status(h, reqWithSession(t, http.MethodGet, "/v1/runs", u.ID, 0)); got != http.StatusUnauthorized {
		t.Fatalf("parked user = %d, want 401", got)
	}
}

// TestAdminOnlyRoutes covers the member/admin split. /v1/infra reports the
// layout, object counts and health of every prefix in the state bucket — the
// shape of the whole deployment, not one tenant's data.
func TestAdminOnlyRoutes(t *testing.T) {
	h, users := authzFixture(t)
	member := seedUser(t, users, "alice", RoleMember, UserActive)
	admin := seedUser(t, users, "jon", RoleAdmin, UserActive)

	if got := status(h, reqWithSession(t, http.MethodGet, "/v1/infra", member.ID, 0)); got != http.StatusForbidden {
		t.Errorf("member GET /v1/infra = %d, want 403", got)
	}
	// The admin passes the gate; Infra is nil in the fixture so the handler
	// itself answers 501. Anything other than 401/403 means authorization let
	// them through, which is what is under test.
	got := status(h, reqWithSession(t, http.MethodGet, "/v1/infra", admin.ID, 0))
	if got == http.StatusForbidden || got == http.StatusUnauthorized {
		t.Errorf("admin GET /v1/infra = %d, want to pass the gate", got)
	}

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/infra", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	if got := status(h, r); got == http.StatusForbidden || got == http.StatusUnauthorized {
		t.Errorf("bearer token GET /v1/infra = %d, want to pass the gate", got)
	}
}

// TestMemberCannotLaunchLeagueWideJobs guards spending. Every run costs real
// money and acts on the league rather than on the caller's own team.
func TestMemberCannotLaunchLeagueWideJobs(t *testing.T) {
	h, users := authzFixture(t)
	member := seedUser(t, users, "alice", RoleMember, UserActive)

	for _, job := range []string{"archive", "recap-site", "team-values", "version-check"} {
		got := status(h, reqWithSession(t, http.MethodPost, "/v1/jobs/"+job, member.ID, 0))
		if got != http.StatusForbidden {
			t.Errorf("member POST /v1/jobs/%s = %d, want 403", job, got)
		}
	}
	// A tenant-scoped job must still be reachable, or members can do nothing at
	// all and the split is just a blanket ban wearing a costume.
	got := status(h, reqWithSession(t, http.MethodPost, "/v1/jobs/optimize", member.ID, 0))
	if got == http.StatusForbidden {
		t.Error("member POST /v1/jobs/optimize = 403; a member must be able to run " +
			"jobs scoped to their own team")
	}
}

// TestCallerFromZeroValueIsPowerless pins the failure mode of a handler reached
// without the gate — a test wiring the mux directly, or a future route
// registered outside Handler. The zero Caller must be nobody, not an admin.
func TestCallerFromZeroValueIsPowerless(t *testing.T) {
	c := CallerFrom(context.Background())
	if c.IsAdmin() {
		t.Fatal("the zero Caller reads as admin; a handler reached without " +
			"authorization would then have full privileges")
	}
	if c.UserID != "" {
		t.Fatalf("zero Caller has UserID %q, want empty", c.UserID)
	}
}

// TestBearerCallerHasNoUserID records the deliberate cost of keeping the token
// store-independent: it authenticates an operator, not a person, so it can
// never be attributed to a tenant.
func TestBearerCallerHasNoUserID(t *testing.T) {
	cfg := Config{Token: testToken, SessionSecret: testSecret}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/runs", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)

	c, ok := cfg.resolveCaller(context.Background(), r)
	if !ok || !c.IsAdmin() {
		t.Fatalf("bearer token resolved to %+v, ok=%v; want an admin", c, ok)
	}
	if c.UserID != "" {
		t.Errorf("bearer caller has UserID %q; it must not impersonate a tenant", c.UserID)
	}
	if !c.ViaToken {
		t.Error("ViaToken not set; handlers that must attribute an action to a person " +
			"cannot tell this caller apart from a real one")
	}
}

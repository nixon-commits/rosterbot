package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRevokePasskey_InvalidatesTheSessionsItMinted is the audit's session
// revocation finding.
//
// User.TokenVersion is compared against the session cookie on every
// authenticated route, in three independent readers, and the mechanism
// demonstrably works. NOTHING EVER WROTE A CHANGED VALUE — no route, no CLI. So
// revocation was checked everywhere and could never be triggered, and
// docs/user-registration.md told the operator to increment a field no code
// updates.
//
// Revoking a passkey is the one control a user actually has, and it deleted the
// credential while leaving every session that credential had minted alive. For
// the case it exists to answer — a lost device — that is the wrong half: the
// attacker holding the device already has a live session cookie, and taking
// away their ability to make NEW ones does not disturb it.
//
// Bumping the version logs the caller out too. That is correct rather than
// unfortunate: sessions cannot be attributed to the credential that minted
// them, so "revoke this passkey" can only honestly mean "and end the sessions
// it may have created".
func TestRevokePasskey_InvalidatesTheSessionsItMinted(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	u := &User{ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive}
	if err := users.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	before, _, _ := users.GetUser(context.Background(), "alice")

	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t)})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodDelete, "/v1/auth/passkeys/AAAA",
		"alice", before.TokenVersion))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204: %s", rec.Code, rec.Body)
	}

	after, ok, err := users.GetUser(context.Background(), "alice")
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if after.TokenVersion == before.TokenVersion {
		t.Fatalf("TokenVersion still %d; every session minted by the revoked passkey "+
			"remains valid, which is the half that matters after a lost device",
			after.TokenVersion)
	}
}

// TestRevokePasskey_OldSessionsStopWorking drives the consequence rather than
// the field: a cookie minted at the previous version must be refused
// afterwards. A bumped counter that no route honours would be the same
// half-mechanism one layer along.
func TestRevokePasskey_OldSessionsStopWorking(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	u := &User{ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive}
	if err := users.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	before, _, _ := users.GetUser(context.Background(), "alice")

	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Lineups: memBlob{TodayKey: []byte("{}")}})

	// A session from before the revocation.
	stale := signedReq(t, secret, http.MethodGet, "/v1/lineup/today", "alice", before.TokenVersion)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodDelete, "/v1/auth/passkeys/AAAA",
		"alice", before.TokenVersion))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, stale)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a session minted before the revocation still works (%d); revocation is "+
			"checked but not effective", rec.Code)
	}
}

// signedReq builds a request carrying a session cookie signed with the given
// secret, so a test can choose the version the session was minted at.
func signedReq(t *testing.T, secret []byte, method, path string, sub UserID, ver int) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, sub, ver, time.Now())
	r := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

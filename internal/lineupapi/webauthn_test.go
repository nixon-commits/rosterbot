package lineupapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// fakeIdentities is an in-memory IdentityStore for handler tests. It enforces
// the same optimistic-concurrency contract as the real stores — a double that
// accepted every write would let a handler regress to a blind overwrite while
// every test here stayed green.
type fakeIdentities struct {
	id      *Identity
	ok      bool
	err     error
	version int
}

func (f *fakeIdentities) currentVersion() IdentityVersion {
	return IdentityVersion(strconv.Itoa(f.version))
}

// GetIdentity hands back a copy, as a store that deserializes fresh bytes does.
// Sharing the live pointer would let a caller's in-place mutation take effect
// without a write, which is precisely the bug being guarded against.
func (f *fakeIdentities) GetIdentity(_ context.Context) (*Identity, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	if !f.ok || f.id == nil {
		return nil, false, nil
	}
	cp := *f.id
	cp.Credentials = append([]webauthn.Credential(nil), f.id.Credentials...)
	cp.Version = f.currentVersion()
	return &cp, true, nil
}

func (f *fakeIdentities) PutIdentity(_ context.Context, id *Identity) error {
	if f.err != nil {
		return f.err
	}
	switch {
	case id.Version == "" && f.ok:
		return ErrIdentityConflict
	case id.Version != "" && !f.ok:
		return ErrIdentityConflict
	case id.Version != "" && id.Version != f.currentVersion():
		return ErrIdentityConflict
	}
	f.version++
	cp := *id
	cp.Credentials = append([]webauthn.Credential(nil), id.Credentials...)
	cp.Version = f.currentVersion()
	f.id = &cp
	f.ok = true
	id.Version = cp.Version
	return nil
}

func testWebAuthn(t *testing.T) *webauthn.WebAuthn {
	t.Helper()
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          "localhost",
		RPOrigins:     []string{"http://localhost:8080"},
		RPDisplayName: "rosterbot (test)",
	})
	if err != nil {
		t.Fatalf("webauthn.New: %v", err)
	}
	return wa
}

func TestRegisterBegin_RejectsWithoutSessionOrToken(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestRegisterBegin_RejectsBootstrapToken inverts an earlier test that
// asserted the bearer token COULD start a registration.
//
// That was correct while there was one identity: the token proved you were the
// operator, and the operator was the only account. With several users it
// becomes a god token — its holder could add a passkey to anybody's account,
// because the ceremony had no notion of whose account it was for. Enrollment is
// now authorized by a user-scoped enrollment link or an existing session.
//
// The token remains an admin break-glass for the rest of the API. It is no
// longer a way to become someone.
func TestRegisterBegin_RejectsBootstrapToken(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — the bootstrap token must no longer enroll "+
			"a passkey against an arbitrary account, body=%s", rec.Code, rec.Body.String())
	}
}

// TestRegisterBegin_AcceptsEnrollmentToken is the replacement path: a
// single-use link, scoped to one user.
func TestRegisterBegin_AcceptsEnrollmentToken(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	ctx := context.Background()
	u := &User{ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive}
	if err := users.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	tok, hash, err := MintEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := users.CreateEnrollment(ctx, hash, Enrollment{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin?token="+string(tok), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	// The link must still be unspent: an abandoned ceremony (user closes the tab
	// at the Touch ID prompt) has to leave it usable, or a single-use recovery
	// link is destroyed by a ceremony that never completed.
	e, ok, err := users.GetEnrollment(ctx, hash)
	if err != nil || !ok {
		t.Fatalf("GetEnrollment: ok=%v err=%v", ok, err)
	}
	if e.Redeemed() {
		t.Fatal("register/begin redeemed the enrollment link; redemption must happen " +
			"at finish, so an abandoned ceremony can be retried")
	}
}

func TestRegisterBegin_AcceptsValidSession(t *testing.T) {
	secret := []byte("s")
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	// Mint a valid session cookie the same way login/finish would.
	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, "alice", 0, time.Now())

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRegisterFinish_RejectsWithoutCeremonyCookie(t *testing.T) {
	secret := []byte("s")
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	// An authorized caller with no in-progress ceremony. The authorization has
	// to come first — otherwise this would answer 400 to someone who was never
	// entitled to ask, which is the same ordering mistake handleJob had.
	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, "alice", 0, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no in-progress ceremony), body=%s", rec.Code, rec.Body.String())
	}
}

func TestRegisterFinish_RejectsWithoutSessionOrToken(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// TestLoginBegin_DoesNotRevealWhetherAnyoneIsRegistered replaces an earlier
// test that expected 404 when the store held no passkeys.
//
// That behaviour was a property of the OLD single-user login, which loaded the
// one identity up front and put its credential ids in allowCredentials — so the
// server necessarily knew, before the user had said anything, whether an
// account existed. Discoverable login inverts that: the authenticator picks a
// resident credential and only then tells us whose it is.
//
// The consequence is worth keeping deliberately rather than incidentally: an
// unauthenticated caller can no longer distinguish "nobody is registered" from
// "someone is", so login/begin is not a user-enumeration oracle.
func TestLoginBegin_DoesNotRevealWhetherAnyoneIsRegistered(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a challenge regardless of who exists), body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "challenge") {
		t.Fatalf("no challenge in response: %s", rec.Body.String())
	}
}

func TestLoginBegin_ReturnsOptionsWhenPasskeyExists(t *testing.T) {
	identities := &fakeIdentities{ok: true, id: &Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var gotCeremonyCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == ceremonyCookieName {
			gotCeremonyCookie = true
		}
	}
	if !gotCeremonyCookie {
		t.Fatal("want a ceremony cookie set on a successful login/begin")
	}
}

func TestLoginFinish_RejectsWithoutCeremonyCookie(t *testing.T) {
	identities := &fakeIdentities{ok: true, id: &Identity{WebAuthnUserID: []byte("h"), Credentials: []webauthn.Credential{{ID: []byte("c")}}}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/finish", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no in-progress ceremony)", rec.Code)
	}
}

func TestListPasskeys_RequiresSession(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no session", rec.Code)
	}

	// Bearer token alone (no session) must NOT satisfy this route — passkey
	// management is a logged-in-browser action, not a break-glass one.
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with token but no session", rec.Code)
	}
}

func TestListPasskeys_ReturnsRegisteredIDs(t *testing.T) {
	secret := []byte("s")
	users := seedUserWithCredential(t, "alice", "cred-1")
	h := Handler(Config{Token: "t", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, "alice", 0, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	wantID := base64.RawURLEncoding.EncodeToString([]byte("cred-1"))
	if !strings.Contains(rec.Body.String(), wantID) {
		t.Fatalf("body = %s, want it to contain credential id %s", rec.Body.String(), wantID)
	}
}

func TestRevokePasskey_RemovesMatchingCredential(t *testing.T) {
	secret := []byte("s")
	users := seedUserWithCredential(t, "alice", "cred-1")
	h := Handler(Config{Token: "t", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, "alice", 0, time.Now())
	id := base64.RawURLEncoding.EncodeToString([]byte("cred-1"))
	req := httptest.NewRequest(http.MethodDelete, "/v1/auth/passkeys/"+id, nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}

	creds, err := users.Credentials(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Fatalf("got %d credentials after revoke, want 0", len(creds))
	}
	// The reverse index must go too. A credential that still resolves to its
	// owner is still usable for the non-discoverable login path, so leaving it
	// behind makes "revoked" a label rather than a fact.
	if _, ok, _ := users.UserByCredential(context.Background(), []byte("cred-1")); ok {
		t.Fatal("revoked credential still resolves through the lookup index")
	}
}

// seedUserWithCredential builds a store holding one active member with one
// passkey, the shape the passkey-management routes operate on.
func seedUserWithCredential(t *testing.T, id UserID, credID string) *FileUserStore {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	ctx := context.Background()
	if err := users.CreateUser(ctx, &User{
		ID: id, Email: string(id) + "@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}
	if err := users.PutCredential(ctx, id, webauthn.Credential{ID: []byte(credID), PublicKey: []byte("pk")}); err != nil {
		t.Fatal(err)
	}
	return users
}

func TestLogout_ClearsSessionCookie(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("want the session cookie cleared (MaxAge < 0)")
	}
}

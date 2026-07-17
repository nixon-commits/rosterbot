package lineupapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// fakeIdentities is an in-memory IdentityStore for handler tests.
type fakeIdentities struct {
	id  *Identity
	ok  bool
	err error
}

func (f *fakeIdentities) GetIdentity(_ context.Context) (*Identity, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.id, f.ok, nil
}

func (f *fakeIdentities) PutIdentity(_ context.Context, id *Identity) error {
	if f.err != nil {
		return f.err
	}
	f.id = id
	f.ok = true
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

func TestRegisterBegin_AcceptsBootstrapToken(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
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
		t.Fatal("want a ceremony cookie set on a successful register/begin")
	}
}

func TestRegisterBegin_AcceptsValidSession(t *testing.T) {
	secret := []byte("s")
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: secret})

	// Mint a valid session cookie the same way login/finish would.
	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, time.Now())

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
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no in-progress ceremony)", rec.Code)
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

// TestLoadOrCreateIdentity_StableAcrossCalls guards against a regression
// where the very-first-passkey bootstrap flow always failed: begin and
// finish each call loadOrCreateIdentity independently, and if the freshly
// generated identity isn't persisted immediately, each call draws its own
// independent random WebAuthnUserID. go-webauthn bakes the identity's
// WebAuthnUserID from the begin call into the ceremony session, then
// requires it to match the identity's WebAuthnUserID on finish — two
// independent 64-byte crypto/rand draws are equal with probability ~0, so
// an unpersisted identity fails the ceremony deterministically.
func TestLoadOrCreateIdentity_StableAcrossCalls(t *testing.T) {
	cfg := Config{Identities: &fakeIdentities{}}

	first, err := cfg.loadOrCreateIdentity(context.Background())
	if err != nil {
		t.Fatalf("first loadOrCreateIdentity: %v", err)
	}
	second, err := cfg.loadOrCreateIdentity(context.Background())
	if err != nil {
		t.Fatalf("second loadOrCreateIdentity: %v", err)
	}

	if !bytes.Equal(first.WebAuthnUserID, second.WebAuthnUserID) {
		t.Fatalf("WebAuthnUserID changed across calls: first=%x second=%x", first.WebAuthnUserID, second.WebAuthnUserID)
	}
}

func TestLoginBegin_NotFoundWhenNoPasskeysRegistered(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
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
	identities := &fakeIdentities{ok: true, id: &Identity{
		WebAuthnUserID: []byte("h"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, time.Now())
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
	identities := &fakeIdentities{ok: true, id: &Identity{
		WebAuthnUserID: []byte("h"),
		Credentials: []webauthn.Credential{
			{ID: []byte("keep-me")},
			{ID: []byte("revoke-me")},
		},
	}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, time.Now())
	targetID := base64.RawURLEncoding.EncodeToString([]byte("revoke-me"))
	req := httptest.NewRequest(http.MethodDelete, "/v1/auth/passkeys/"+targetID, nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
	if len(identities.id.Credentials) != 1 || string(identities.id.Credentials[0].ID) != "keep-me" {
		t.Fatalf("credentials after revoke = %+v, want only keep-me left", identities.id.Credentials)
	}
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

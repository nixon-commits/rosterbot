package lineupapi

import (
	"context"
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

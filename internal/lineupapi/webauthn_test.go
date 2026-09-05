package lineupapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// testUserID mints a UserID whose handle actually decodes, the way every
// production id does (see NewUserID / cmd/invite.go's newUserID).
//
// A literal like UserID("alice") looks like a fine test fixture and is not:
// UserID IS the base64url of the raw WebAuthn handle, and "alice" (5 bytes,
// length mod 4 == 1) is not valid base64url, so WebAuthnHandle() returns nil
// and go-webauthn 0.18.0's BeginRegistration refuses it outright ("the user
// id must be between 1 and 64 bytes but it has a length of 0"). Wrapping the
// name through NewUserID produces a real, if short, handle that decodes fine
// — distinct names still yield distinct ids, which is all these tests need.
func testUserID(name string) UserID { return NewUserID([]byte(name)) }

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
	h := Handler(Config{Token: "secret-token", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
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
	alice := testUserID("alice")
	u := &User{ID: alice, Email: "a@example.test", Role: RoleMember, Status: UserActive}
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin?token="+string(tok), nil)
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
	alice := testUserID("alice")
	if err := users.CreateUser(context.Background(), &User{
		ID: alice, Email: "a@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	// Mint a valid session cookie the same way login/finish would.
	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, alice, 0, time.Now())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

// TestRegisterBegin_RefusesInvalidHandle pins the loud failure this bead adds
// (rosterbot-jlqt): a user record whose id does not decode to a 1..64-byte
// WebAuthn handle must be refused with a distinct, diagnosable error rather
// than reaching go-webauthn 0.18.0's own opaque "could not begin
// registration" 500 — the same failure that made every register/begin test
// in this package fail identically until each was traced back to a
// hand-typed "alice" fixture (see testUserID's doc comment).
//
// No production id can trigger this (every real one is minted by
// NewUserID(newWebAuthnUserID()), always exactly 64 bytes — see
// UserID.WebAuthnHandle's doc comment), so the fixture here is deliberately
// the same shape as the tests that used to break: a literal, non-base64url
// id, standing in for a hand-typed record or a corrupted one.
func TestRegisterBegin_RefusesInvalidHandle(t *testing.T) {
	secret := []byte("s")
	users := NewFileUserStore(t.TempDir())
	const badID UserID = "alice" // 5 bytes, length mod 4 == 1: not valid base64url
	if err := users.CreateUser(context.Background(), &User{
		ID: badID, Email: "a@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, badID, 0, time.Now())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "could not begin registration") {
		t.Fatalf("body = %s, want the specific invalid-handle refusal, not go-webauthn's "+
			"opaque error — the whole point is naming the actual cause", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid webauthn identity") {
		t.Fatalf("body = %s, want it to name an invalid webauthn identity", rec.Body.String())
	}
}

func TestRegisterFinish_RejectsWithoutCeremonyCookie(t *testing.T) {
	secret := []byte("s")
	users := NewFileUserStore(t.TempDir())
	alice := testUserID("alice")
	if err := users.CreateUser(context.Background(), &User{
		ID: alice, Email: "a@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	// An authorized caller with no in-progress ceremony. The authorization has
	// to come first — otherwise this would answer 400 to someone who was never
	// entitled to ask, which is the same ordering mistake handleJob had.
	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, alice, 0, time.Now())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
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
	h := Handler(Config{Token: "secret-token", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
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
	h := Handler(Config{Token: "t", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/login/begin", nil)
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

// TestLoginBegin_SetsCeremonyCookie was TestLoginBegin_ReturnsOptionsWhenPasskeyExists,
// seeded with a stored identity holding one credential.
//
// That setup was decorative and the rename records why: BeginDiscoverableLogin
// consults no store at all, so seeding one changed nothing about the response.
// Deleting the seed alongside the dead Config.Identities field (rosterbot-w200)
// left the assertions byte-identical, which is the evidence that the field had
// no reader — a store whose removal a test cannot feel was never being read.
func TestLoginBegin_SetsCeremonyCookie(t *testing.T) {
	h := Handler(Config{Token: "t", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/login/begin", nil)
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
	h := Handler(Config{Token: "t", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/login/finish", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no in-progress ceremony)", rec.Code)
	}
}

func TestListPasskeys_RequiresSession(t *testing.T) {
	h := Handler(Config{Token: "t", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/auth/passkeys", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no session", rec.Code)
	}

	// Bearer token alone (no session) must NOT satisfy this route — passkey
	// management is a logged-in-browser action, not a break-glass one.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/auth/passkeys", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with token but no session", rec.Code)
	}
}

func TestListPasskeys_ReturnsRegisteredIDs(t *testing.T) {
	secret := []byte("s")
	alice := testUserID("alice")
	users := seedUserWithCredential(t, alice, "cred-1")
	h := Handler(Config{Token: "t", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, alice, 0, time.Now())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/auth/passkeys", nil)
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
	alice := testUserID("alice")
	users := seedUserWithCredential(t, alice, "cred-1")
	h := Handler(Config{Token: "t", Users: users, Enrollments: users,
		WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, alice, 0, time.Now())
	id := base64.RawURLEncoding.EncodeToString([]byte("cred-1"))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/v1/auth/passkeys/"+id, nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}

	creds, err := users.Credentials(context.Background(), alice)
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
	h := Handler(Config{Token: "t", WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/logout", nil)
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

// ParseRPOrigins is the split for the one comma-separated SSM parameter that
// carries both surfaces' origins (see RpOriginParam in infra/infra.go).
//
// The cases that matter are the ones that must NOT silently pass, because a
// bad origin fails asymmetrically: the surface whose origin parsed keeps
// working perfectly, so the only symptom is that passkeys stop working on the
// OTHER surface with a generic ceremony error. Anything rejected here has to
// come back in `dropped` so the Lambda can name it in a log line.
func TestParseRPOrigins(t *testing.T) {
	for _, tc := range []struct {
		name        string
		raw         string
		wantOrigins []string
		wantDropped []string
	}{{
		name:        "the real production value: both surfaces, apex first",
		raw:         "https://rosterbot.dev,https://dash.rosterbot.dev",
		wantOrigins: []string{"https://rosterbot.dev", "https://dash.rosterbot.dev"},
	}, {
		name:        "surrounding whitespace is a formatting choice, not an error",
		raw:         "https://rosterbot.dev , https://dash.rosterbot.dev",
		wantOrigins: []string{"https://rosterbot.dev", "https://dash.rosterbot.dev"},
	}, {
		name:        "a single origin is still the whole list",
		raw:         "https://rosterbot.dev",
		wantOrigins: []string{"https://rosterbot.dev"},
	}, {
		name:        "localhost over http, which is how `rosterbot serve` runs",
		raw:         "http://localhost:8080",
		wantOrigins: []string{"http://localhost:8080"},
	}, {
		name:        "http to a real host is refused: a passkey origin is https",
		raw:         "http://rosterbot.dev",
		wantDropped: []string{"http://rosterbot.dev"},
	}, {
		name:        "a trailing slash is a URL, not an origin; clientDataJSON never carries one",
		raw:         "https://rosterbot.dev/",
		wantDropped: []string{"https://rosterbot.dev/"},
	}, {
		name:        "a path means whoever wrote the parameter wrote a link",
		raw:         "https://rosterbot.dev/invite",
		wantDropped: []string{"https://rosterbot.dev/invite"},
	}, {
		name:        "a bare hostname is the RP ID, pasted into the origin parameter",
		raw:         "rosterbot.dev",
		wantDropped: []string{"rosterbot.dev"},
	}, {
		name:        "one bad entry must not cost the good one",
		raw:         "https://rosterbot.dev,dash.rosterbot.dev",
		wantOrigins: []string{"https://rosterbot.dev"},
		wantDropped: []string{"dash.rosterbot.dev"},
	}, {
		name:        "a stray comma is not worth reporting",
		raw:         "https://rosterbot.dev,,",
		wantOrigins: []string{"https://rosterbot.dev"},
	}, {
		name: "an empty parameter yields nothing, and says nothing was malformed",
		raw:  "",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			origins, dropped := ParseRPOrigins(tc.raw)
			if !slices.Equal(origins, tc.wantOrigins) {
				t.Errorf("origins = %q, want %q", origins, tc.wantOrigins)
			}
			if !slices.Equal(dropped, tc.wantDropped) {
				t.Errorf("dropped = %q, want %q", dropped, tc.wantDropped)
			}
		})
	}
}

// The config the production values actually produce must be accepted by
// go-webauthn itself, not merely by our own parser.
func TestNewWebAuthn_AcceptsBothProductionOrigins(t *testing.T) {
	origins, dropped := ParseRPOrigins("https://rosterbot.dev,https://dash.rosterbot.dev")
	if len(dropped) != 0 {
		t.Fatalf("production origin parameter dropped %q", dropped)
	}
	wa, err := NewWebAuthn("rosterbot.dev", origins, "rosterbot")
	if err != nil {
		t.Fatalf("NewWebAuthn rejected the production RP config: %v", err)
	}
	if got := wa.Config.RPID; got != "rosterbot.dev" {
		t.Errorf("RPID = %q, want the apex", got)
	}
	if !slices.Equal(wa.Config.RPOrigins, origins) {
		t.Errorf("RPOrigins = %q, want %q", wa.Config.RPOrigins, origins)
	}
}

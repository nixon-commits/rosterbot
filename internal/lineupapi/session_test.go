package lineupapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifySession(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	value := signSession(secret, "alice", 0, now)
	if err := parseSessionErr(secret, value, now); err != nil {
		t.Fatalf("verify immediately after sign: %v", err)
	}
	if err := parseSessionErr(secret, value, now.Add(sessionTTL-time.Minute)); err != nil {
		t.Fatalf("verify just before expiry: %v", err)
	}
	if err := parseSessionErr(secret, value, now.Add(sessionTTL+time.Minute)); err == nil {
		t.Fatal("want error verifying after expiry, got nil")
	}
}

func TestVerifySessionRejectsTamperedValue(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()
	value := signSession(secret, "alice", 0, now)

	if err := parseSessionErr([]byte("wrong-secret"), value, now); err == nil {
		t.Fatal("want error verifying with the wrong secret, got nil")
	}
	if err := parseSessionErr(secret, value+"x", now); err == nil {
		t.Fatal("want error verifying a tampered value, got nil")
	}
	if err := parseSessionErr(secret, "not-even-close", now); err == nil {
		t.Fatal("want error verifying a malformed value, got nil")
	}
	if err := parseSessionErr(secret, "", now); err == nil {
		t.Fatal("want error verifying an empty value, got nil")
	}
}

// TestSessionAuth_EmptySecretFailsClosed replaces TestHasValidSession, deleted
// along with hasValidSession itself once rosterbot-crq.10 landed per-route
// authorization and left the helper with no non-test caller (rosterbot-w200).
//
// The assertion it carried is not dead, which is why this is a replacement and
// not a deletion: sessionFromRequest refuses outright when SessionSecret is
// empty, and every route reaches that through resolveCaller. An SSM read that
// came back empty would otherwise leave the door verifying HMACs under a
// zero-length key — a key the attacker has too — so anyone could mint their own
// cookie naming any user and walk in as them.
//
// THE FORGERY IS THE WHOLE TEST, and getting this wrong once is worth
// recording: the first draft presented a cookie signed with a REAL secret to a
// server holding none, got its 401, and pinned nothing at all. That 401 came
// from the signature mismatch, not from the guard — deleting the guard left the
// test green. The cookie has to be signed with the same empty key the server
// holds, so that a server without the guard would genuinely accept it.
//
// The control is the other half: the same forgery against a server that DOES
// hold that secret must reach the route (200 from /v1/me). Without it a 401
// proves only that something refused the request, which a broken session codec
// or an unwired user store would also produce.
//
// It asserts on the OUTCOME at the door, not on either guard that produces it.
// The refusal is currently implemented twice — sessionFromRequest and
// verifyPayload both reject an empty key — so deleting either one alone leaves
// this green, and that is correct: defence in depth means no single layer is
// the property. Pinning one of them by name would fail the day someone
// consolidated them, which is a refactor and not a regression.
func TestSessionAuth_EmptySecretFailsClosed(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive,
	}); err != nil {
		t.Fatal(err)
	}

	// What an attacker can build knowing only that the server's secret is unset.
	forged := &http.Cookie{Name: sessionCookieName, Value: signSession(nil, "alice", 0, time.Now())}

	get := func(cfg Config) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.AddCookie(forged)
		rec := httptest.NewRecorder()
		Handler(cfg).ServeHTTP(rec, req)
		return rec.Code
	}

	// Control: a server whose secret really IS empty would find this cookie's
	// HMAC perfectly valid, so the refusal below has to be the guard.
	if code := get(Config{Token: "t", Users: users, SessionSecret: []byte{}}); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — a server with no configured session secret must "+
			"refuse a cookie signed under the empty key, not verify it", code)
	}
	if code := get(Config{Token: "t", Users: users, SessionSecret: nil}); code != http.StatusUnauthorized {
		t.Fatalf("nil secret: status = %d, want 401 (nil and []byte{} must behave alike)", code)
	}

	// The control the other way round: the same construction under a real
	// secret does authenticate, so the 401s above are about the empty key
	// rather than about a session codec or user store that never works.
	secret := []byte("test-secret")
	live := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	live.AddCookie(&http.Cookie{Name: sessionCookieName, Value: signSession(secret, "alice", 0, time.Now())})
	rec := httptest.NewRecorder()
	Handler(Config{Token: "t", Users: users, SessionSecret: secret}).ServeHTTP(rec, live)
	if rec.Code != http.StatusOK {
		t.Fatalf("control: status = %d, want 200 — a properly signed session must reach "+
			"the route, or the assertions above prove nothing, body=%s", rec.Code, rec.Body.String())
	}
}

func TestClearSessionCookieExpiresImmediately(t *testing.T) {
	rec := httptest.NewRecorder()
	clearSessionCookie(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("want one cookie with MaxAge < 0, got %+v", cookies)
	}
}

// parseSessionErr adapts parseSession to the error-only shape these tests were
// written against, so they keep asserting the same signature/expiry behaviour
// after rosterbot-crq.8 added a subject to the payload.
func parseSessionErr(secret []byte, value string, now time.Time) error {
	_, err := parseSession(secret, value, now)
	return err
}

// TestSession_CarriesTheSubject is the point of rosterbot-crq.8: a session says
// WHO it belongs to. Before it, the payload held only an expiry, so every
// logged-in user was indistinguishable from every other.
func TestSession_CarriesTheSubject(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	got, err := parseSession(secret, signSession(secret, "alice", 7, now), now)
	if err != nil {
		t.Fatalf("parseSession: %v", err)
	}
	if got.Sub != "alice" {
		t.Errorf("Sub = %q, want alice", got.Sub)
	}
	if got.Ver != 7 {
		t.Errorf("Ver = %d, want 7 — without it, incrementing a user's "+
			"TokenVersion cannot revoke their existing sessions", got.Ver)
	}
}

// TestSession_LegacyCookieWithoutSubjectIsRejected covers the cookies already
// in browsers when this deploys. They are correctly signed and unexpired, so
// the ONLY thing stopping them is the empty-subject check.
//
// Accepting them would mean a valid session belonging to nobody, and every
// downstream scoping decision would have to invent an answer — most likely
// "the operator", which is the worst possible default. The cost of rejecting
// is one forced re-login for everyone, once.
func TestSession_LegacyCookieWithoutSubjectIsRejected(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()

	// Exactly what the pre-crq.8 signSession produced: a valid HMAC over a
	// payload with an expiry and nothing else.
	legacy, err := json.Marshal(struct {
		ExpiresAt int64 `json:"exp"`
	}{now.Add(sessionTTL).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	value := signPayload(secret, legacy)

	// The signature really is valid — otherwise this test would pass for the
	// wrong reason.
	if _, err := verifyPayload(secret, value); err != nil {
		t.Fatalf("fixture is not a validly signed cookie: %v", err)
	}
	if _, err := parseSession(secret, value, now); err == nil {
		t.Fatal("a correctly signed session with no subject was accepted; it would " +
			"authenticate as nobody, and every scoping decision downstream would " +
			"have to guess who")
	}
}

// TestSession_SubjectIsSigned guards the obvious attack on a stateless token:
// editing the subject to become another user. It is inside the HMAC, so any
// edit invalidates the signature — but that is a property worth asserting
// rather than assuming, since the payload is plainly visible base64.
func TestSession_SubjectIsSigned(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()
	value := signSession(secret, "alice", 0, now)

	dot := strings.IndexByte(value, '.')
	payload, err := base64.RawURLEncoding.DecodeString(value[:dot])
	if err != nil {
		t.Fatal(err)
	}
	forged := bytes.Replace(payload, []byte(`"alice"`), []byte(`"mallory"`), 1)
	if bytes.Equal(forged, payload) {
		t.Fatal("fixture did not actually change the subject")
	}
	tampered := base64.RawURLEncoding.EncodeToString(forged) + value[dot:]

	if _, err := parseSession(secret, tampered, now); err == nil {
		t.Fatal("a session with a swapped subject verified; the subject must be " +
			"covered by the HMAC or any user can become any other")
	}
}

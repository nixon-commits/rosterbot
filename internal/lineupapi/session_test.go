package lineupapi

import (
	"bytes"
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

func TestHasValidSession(t *testing.T) {
	secret := []byte("test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	if hasValidSession(req, secret) {
		t.Fatal("want false with no cookie set")
	}

	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, "alice", 0, time.Now())
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if !hasValidSession(req, secret) {
		t.Fatal("want true with a freshly set cookie")
	}

	if hasValidSession(req, nil) {
		t.Fatal("want false when the server has no configured secret (misconfiguration must fail closed)")
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

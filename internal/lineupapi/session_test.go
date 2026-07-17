package lineupapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignAndVerifySession(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	value := signSession(secret, now)
	if err := verifySession(secret, value, now); err != nil {
		t.Fatalf("verify immediately after sign: %v", err)
	}
	if err := verifySession(secret, value, now.Add(sessionTTL-time.Minute)); err != nil {
		t.Fatalf("verify just before expiry: %v", err)
	}
	if err := verifySession(secret, value, now.Add(sessionTTL+time.Minute)); err == nil {
		t.Fatal("want error verifying after expiry, got nil")
	}
}

func TestVerifySessionRejectsTamperedValue(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()
	value := signSession(secret, now)

	if err := verifySession([]byte("wrong-secret"), value, now); err == nil {
		t.Fatal("want error verifying with the wrong secret, got nil")
	}
	if err := verifySession(secret, value+"x", now); err == nil {
		t.Fatal("want error verifying a tampered value, got nil")
	}
	if err := verifySession(secret, "not-even-close", now); err == nil {
		t.Fatal("want error verifying a malformed value, got nil")
	}
	if err := verifySession(secret, "", now); err == nil {
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
	setSessionCookie(rec, secret, time.Now())
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

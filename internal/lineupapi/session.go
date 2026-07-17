package lineupapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "rosterbot_session"
	sessionTTL        = 30 * 24 * time.Hour
)

var errInvalidSession = errors.New("invalid or expired session")

type sessionPayload struct {
	ExpiresAt int64 `json:"exp"`
}

// signSession mints a signed, stateless session cookie value:
// base64url(payload) + "." + base64url(HMAC-SHA256(payload, secret)). Nothing
// is stored server-side — verifying later needs only the same secret.
func signSession(secret []byte, now time.Time) string {
	payload, _ := json.Marshal(sessionPayload{ExpiresAt: now.Add(sessionTTL).Unix()})
	return signPayload(secret, payload)
}

func signPayload(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// verifySession checks the signature and expiry of a session cookie value.
func verifySession(secret []byte, value string, now time.Time) error {
	payload, err := verifyPayload(secret, value)
	if err != nil {
		return err
	}
	var p sessionPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return errInvalidSession
	}
	if now.Unix() > p.ExpiresAt {
		return errInvalidSession
	}
	return nil
}

func verifyPayload(secret []byte, value string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errInvalidSession
	}
	dot := strings.IndexByte(value, '.')
	if dot < 0 {
		return nil, errInvalidSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(value[:dot])
	if err != nil {
		return nil, errInvalidSession
	}
	sig, err := base64.RawURLEncoding.DecodeString(value[dot+1:])
	if err != nil {
		return nil, errInvalidSession
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	want := mac.Sum(nil)
	if subtle.ConstantTimeCompare(sig, want) != 1 {
		return nil, errInvalidSession
	}
	return payload, nil
}

func setSessionCookie(w http.ResponseWriter, secret []byte, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    signSession(secret, now),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// hasValidSession reports whether the request carries a session cookie signed
// by secret and not yet expired. An empty secret (misconfiguration) fails
// closed, matching authorized()'s empty-token behavior.
func hasValidSession(r *http.Request, secret []byte) bool {
	if len(secret) == 0 {
		return false
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySession(secret, c.Value, time.Now()) == nil
}

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

// sessionPayload is the signed body of the session cookie.
//
// Sub and Ver are what make this a multi-tenant session rather than a boolean
// "someone logged in". Before rosterbot-crq.8 the payload carried only an
// expiry, which was sufficient for exactly one operator and becomes "every
// logged-in user is the same user" the moment there are two.
type sessionPayload struct {
	// Sub is the user this session belongs to. A session without one is
	// rejected outright (see parseSession) rather than defaulted to anybody.
	Sub UserID `json:"sub"`

	// Ver is the User.TokenVersion this session was minted at. Revocation is
	// incrementing that field: every cookie carrying the old value stops
	// verifying, for that user alone.
	//
	// It is checked by the caller, not here, because the check needs the user
	// record — and every route that cares has to load that record anyway to
	// scope its query. Keeping this file free of the store keeps the crypto
	// testable without one.
	Ver int `json:"ver"`

	ExpiresAt int64 `json:"exp"`
}

// signSession mints a signed, stateless session cookie value:
// base64url(payload) + "." + base64url(HMAC-SHA256(payload, secret)). Nothing
// is stored server-side — verifying later needs only the same secret, plus the
// user's current TokenVersion for revocation.
func signSession(secret []byte, sub UserID, ver int, now time.Time) string {
	payload, _ := json.Marshal(sessionPayload{
		Sub: sub, Ver: ver, ExpiresAt: now.Add(sessionTTL).Unix(),
	})
	return signPayload(secret, payload)
}

func signPayload(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// parseSession checks the signature and expiry of a session cookie and returns
// its payload. It does NOT check Ver — that needs the user record; see
// sessionPayload.Ver.
//
// A payload with no Sub is rejected. Cookies minted before rosterbot-crq.8
// carry only an expiry, and they are still correctly signed, so the only thing
// standing between them and being accepted is this check. Accepting them would
// mean a valid session belonging to nobody, which every downstream scoping
// decision would then have to invent an answer for. The visible consequence is
// that deploying this logs out every existing session exactly once.
func parseSession(secret []byte, value string, now time.Time) (sessionPayload, error) {
	var p sessionPayload
	payload, err := verifyPayload(secret, value)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, errInvalidSession
	}
	if p.Sub == "" {
		return p, errInvalidSession
	}
	if now.Unix() > p.ExpiresAt {
		return p, errInvalidSession
	}
	return p, nil
}

// verifyPayload checks the HMAC and returns the raw payload bytes.
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

func setSessionCookie(w http.ResponseWriter, secret []byte, sub UserID, ver int, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    signSession(secret, sub, ver, now),
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

// sessionFromRequest returns the verified payload on the request, if any.
func sessionFromRequest(r *http.Request, secret []byte) (sessionPayload, error) {
	if len(secret) == 0 {
		return sessionPayload{}, errInvalidSession
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sessionPayload{}, errInvalidSession
	}
	return parseSession(secret, c.Value, time.Now())
}

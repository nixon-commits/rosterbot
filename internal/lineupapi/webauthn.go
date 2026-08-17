// Package lineupapi (this file): WebAuthn passkey registration. See
// docs/superpowers/specs/2026-07-17-dashboard-passkey-auth-design.md.
package lineupapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	ceremonyCookieName = "rosterbot_ceremony"
	ceremonyTTL        = 5 * time.Minute
)

// NewWebAuthn builds the RP config used by every ceremony handler. rpID must
// be the bare hostname (no scheme/port); rpOrigin must be the full origin
// (scheme+host, no trailing slash) the browser reports in clientDataJSON.
func NewWebAuthn(rpID, rpOrigin, rpDisplayName string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPOrigins:     []string{rpOrigin},
		RPDisplayName: rpDisplayName,
	})
}

func setCeremonyCookie(w http.ResponseWriter, session *webauthn.SessionData) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(data),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ceremonyTTL.Seconds()),
	})
	return nil
}

func ceremonySessionFromRequest(r *http.Request) (*webauthn.SessionData, error) {
	c, err := r.Cookie(ceremonyCookieName)
	if err != nil {
		return nil, errors.New("no in-progress ceremony")
	}
	data, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, errors.New("corrupt ceremony cookie")
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, errors.New("corrupt ceremony cookie")
	}
	return &session, nil
}

func clearCeremonyCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ceremonyCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

// registrationSubject decides WHO a registration ceremony is for, and whether
// it is allowed at all.
//
// Two routes in, and they answer different questions:
//
//	an enrollment token  -> "this specific person may create their first passkey"
//	a live session       -> "this logged-in user may add another device"
//
// The global bootstrap token is deliberately NOT one of them any more. It
// authorized enrolment against an identity model with no notion of whose —
// harmless with one operator, and a god token the moment there are several,
// since its holder could add a passkey to anybody's account. It remains an
// admin break-glass for the rest of the API; it is no longer a way to become
// someone.
//
// The token is read but NOT redeemed here. Redemption happens at finish, so an
// abandoned ceremony — a user who closes the tab at the Touch ID prompt —
// leaves the link usable. Burning it at begin would spend a single-use
// recovery link on a ceremony that never completed, with no way to retry.
func (cfg Config) registrationSubject(r *http.Request, token string) (*User, bool) {
	ctx := r.Context()
	if cfg.Users == nil {
		return nil, false
	}
	if token != "" && cfg.Enrollments != nil {
		e, ok, err := cfg.Enrollments.GetEnrollment(ctx, HashEnrollmentToken(EnrollmentToken(token)))
		if err != nil || !ok || e.Redeemed() || e.Expired(time.Now()) {
			return nil, false
		}
		u, ok, err := cfg.Users.GetUser(ctx, e.UserID)
		if err != nil || !ok {
			return nil, false
		}
		return u, true
	}
	if p, err := sessionFromRequest(r, cfg.SessionSecret); err == nil {
		u, ok, err := cfg.Users.GetUser(ctx, p.Sub)
		if err != nil || !ok || p.Ver != u.TokenVersion {
			return nil, false
		}
		return u, true
	}
	return nil, false
}

// enrollmentToken reads the enrollment token from ?token= or the JSON body.
//
// IT PUTS THE BODY BACK, and that is not tidiness — it is the difference
// between registration working and not. handleAuthRegisterFinish calls this
// first and then hands the SAME request to FinishRegistration, which parses the
// WebAuthn attestation out of the body. Consuming it here left that parse
// looking at an empty reader, so the ceremony failed with a credential error
// whose real cause was two lines earlier.
//
// It drained the body even when there was NO token to find, so this was never
// limited to enrollment: adding a second passkey from an existing session was
// broken the same way. It survived unnoticed because the only account in
// existence already had a passkey from before this function was introduced, so
// nothing anyone did reached the finish path — and the two tests covering that
// route both post "{}" and assert failure, which an empty body satisfies just
// as well as a full one.
//
// The whole body is read rather than a bounded prefix: a LimitReader would
// silently truncate a large attestation into a parse error, trading this bug
// for a subtler one. Request size is already capped upstream by the Function
// URL's payload limit.
func enrollmentToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if r.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))

	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.Token
}

func (cfg Config) handleAuthRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u, ok := cfg.registrationSubject(r, enrollmentToken(r))
	if !ok {
		writeErr(w, http.StatusForbidden, "not authorized to register a passkey")
		return
	}
	creds, err := cfg.Users.Credentials(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "user store unavailable")
		return
	}
	creation, session, err := cfg.WebAuthn.BeginRegistration(
		UserCredentials{User: u, Credentials: creds},
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not begin registration")
		return
	}
	if err := setCeremonyCookie(w, session); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start ceremony")
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

func (cfg Config) handleAuthRegisterFinish(w http.ResponseWriter, r *http.Request) {
	token := enrollmentToken(r)
	u, ok := cfg.registrationSubject(r, token)
	if !ok {
		writeErr(w, http.StatusForbidden, "not authorized to register a passkey")
		return
	}
	session, err := ceremonySessionFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "registration session expired, try again")
		return
	}
	creds, err := cfg.Users.Credentials(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "user store unavailable")
		return
	}
	cred, err := cfg.WebAuthn.FinishRegistration(
		UserCredentials{User: u, Credentials: creds}, *session, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "passkey registration failed")
		return
	}

	// Redeem only now that the ceremony has actually produced a credential, and
	// BEFORE writing it: if redemption loses a race the passkey must not exist,
	// or a link could yield two credentials.
	if token != "" && cfg.Enrollments != nil {
		if _, err := cfg.Enrollments.RedeemEnrollment(r.Context(),
			HashEnrollmentToken(EnrollmentToken(token)), time.Now()); err != nil {
			writeErr(w, http.StatusForbidden, "enrollment link is no longer valid")
			return
		}
	}

	// One credential, one key — no read-modify-write, so a concurrent login's
	// sign-counter update on another credential cannot lose this one.
	if err := cfg.Users.PutCredential(r.Context(), u.ID, *cred); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save passkey")
		return
	}

	clearCeremonyCookie(w)
	setSessionCookie(w, cfg.SessionSecret, u.ID, u.TokenVersion, time.Now())
	writeJSON(w, http.StatusOK, map[string]string{"status": "registered"})
}

// handleAuthLoginBegin starts a DISCOVERABLE (usernameless) assertion.
//
// The old flow loaded the one identity and passed it to BeginLogin, which sent
// its credential ids in allowCredentials — workable for a single operator and
// impossible for several, since the server would have to know who is logging in
// before they have said. Discoverable login inverts that: the authenticator
// picks a resident credential and tells us which user it belongs to.
//
// It also means this endpoint no longer reveals whether anyone is registered.
func (cfg Config) handleAuthLoginBegin(w http.ResponseWriter, r *http.Request) {
	assertion, session, err := cfg.WebAuthn.BeginDiscoverableLogin()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not begin login")
		return
	}
	if err := setCeremonyCookie(w, session); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start ceremony")
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

// discoverableUser resolves the handle an authenticator asserts into the user
// it names, plus their credentials.
//
// userHandle IS the UserID (see lineupapi.UserID) — the same 64 bytes, so this
// is a direct key-get with no index. rawID is unused for that reason; it is
// only needed by the non-discoverable fallback, which resolves through
// UserByCredential instead.
//
// Returning an error for an unknown handle is what makes a forged or stale
// handle fail the ceremony rather than authenticate as nobody.
func (cfg Config) discoverableUser(_ []byte, userHandle []byte) (webauthn.User, error) {
	if cfg.Users == nil {
		return nil, errors.New("no user store configured")
	}
	ctx := context.Background()
	uid := NewUserID(userHandle)
	u, ok, err := cfg.Users.GetUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("unknown user handle")
	}
	creds, err := cfg.Users.Credentials(ctx, uid)
	if err != nil {
		return nil, err
	}
	return UserCredentials{User: u, Credentials: creds}, nil
}

func (cfg Config) handleAuthLoginFinish(w http.ResponseWriter, r *http.Request) {
	session, err := ceremonySessionFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "login session expired, try again")
		return
	}

	// FinishPasskeyLogin rather than FinishDiscoverableLogin, because it RETURNS
	// the resolved user. The alternative — reading session.UserID — is a trap:
	// BeginDiscoverableLogin calls beginLogin(nil, nil, ...), so that field is
	// nil for the whole ceremony, and deriving a user id from it yields the
	// empty id and a 401 on every single login.
	authed, cred, err := cfg.WebAuthn.FinishPasskeyLogin(cfg.discoverableUser, *session, r)
	if err != nil {
		// One message for every failure — unknown handle, bad signature,
		// revoked credential. Distinguishing them would tell an unauthenticated
		// caller which user handles exist.
		writeErr(w, http.StatusUnauthorized, "login failed")
		return
	}
	// The value came back from cfg.discoverableUser, so this assertion holds by
	// construction; failing closed rather than panicking keeps a future change
	// to that handler from turning a type error into a crash on the login path.
	uc, ok := authed.(UserCredentials)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "login failed")
		return
	}
	uid, u := uc.User.ID, uc.User
	if u.Status != UserActive {
		writeErr(w, http.StatusForbidden, "account is not active")
		return
	}

	// Persist the sign counter to THIS CREDENTIAL'S OWN KEY. That is the
	// contention-free property the key layout exists for: a concurrent
	// registration writes a different sort key and cannot be lost by this
	// write, so no read-modify-write and no retry are involved.
	//
	// Best-effort, as before: a store failure must not fail a login that has
	// already verified cryptographically.
	_ = cfg.Users.PutCredential(r.Context(), uid, *cred)

	clearCeremonyCookie(w)
	setSessionCookie(w, cfg.SessionSecret, uid, u.TokenVersion, time.Now())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type passkeyOut struct {
	ID string `json:"id"`
}

// sessionUser resolves the logged-in user for the passkey-management routes.
// They are session-only: the bearer token authenticates an operator rather than
// a person, so it has no passkeys to list and no business revoking anyone's.
func (cfg Config) sessionUser(r *http.Request) (*User, bool) {
	if cfg.Users == nil {
		return nil, false
	}
	p, err := sessionFromRequest(r, cfg.SessionSecret)
	if err != nil {
		return nil, false
	}
	u, ok, err := cfg.Users.GetUser(r.Context(), p.Sub)
	if err != nil || !ok || p.Ver != u.TokenVersion {
		return nil, false
	}
	return u, true
}

func (cfg Config) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	u, ok := cfg.sessionUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	creds, err := cfg.Users.Credentials(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "user store unavailable")
		return
	}
	out := []passkeyOut{}
	for _, c := range creds {
		out = append(out, passkeyOut{ID: base64.RawURLEncoding.EncodeToString(c.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

func (cfg Config) handleRevokePasskey(w http.ResponseWriter, r *http.Request) {
	u, ok := cfg.sessionUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	targetID, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid passkey id")
		return
	}
	// Scoped to the caller's own credentials by construction: the key is
	// (their user id, this credential id), so revoking cannot reach across
	// tenants even if a caller supplies someone else's credential id.
	if err := cfg.Users.DeleteCredential(r.Context(), u.ID, targetID); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (cfg Config) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// ensureUserForIdentity makes the tenant record a session's subject resolves
// against, idempotently.
//
// It exists because authorization now requires one (rosterbot-crq.10): a
// session names a user, and resolveCaller refuses a subject with no record. The
// singleton Identity has no such record until either `migrate-identity` runs or
// this does — and without it a successful passkey login would mint a session
// that is then rejected by every route, which reads as "login worked and
// nothing else does".
//
// Creating the user as ADMIN is not a privilege grant so much as an
// observation: at this point the only way to hold a passkey at all is to have
// been the operator, since enrolment requires either the bootstrap token or an
// existing credential. It produces exactly what migrate-identity produces, so
// whichever runs first, the other is a no-op.
//
// Failures are returned rather than swallowed. A login that cannot produce a
// usable session should say so at the login, not hand back a cookie that fails
// on the next request.
func (cfg Config) ensureUserForIdentity(ctx context.Context, identity *Identity) (UserID, error) {
	uid := NewUserID(identity.WebAuthnUserID)
	if cfg.Users == nil {
		return uid, nil // no tenant directory configured; sessions are refused anyway
	}
	if _, ok, err := cfg.Users.GetUser(ctx, uid); err != nil {
		return uid, err
	} else if ok {
		return uid, nil
	}
	err := cfg.Users.CreateUser(ctx, &User{
		ID:          uid,
		DisplayName: "operator",
		Role:        RoleAdmin,
		Status:      UserActive,
		// AutoApply stays false even here: the switch that lets the bot write to
		// a roster is turned on by a person, never inherited from a bootstrap.
		AutoApply: false,
		CreatedAt: time.Now().UTC(),
	})
	// A concurrent request that created it first is success, not failure — the
	// postcondition (a user exists for this handle) holds either way.
	if errors.Is(err, ErrUserConflict) {
		return uid, nil
	}
	return uid, err
}

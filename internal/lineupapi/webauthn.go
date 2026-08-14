// Package lineupapi (this file): WebAuthn passkey registration. See
// docs/superpowers/specs/2026-07-17-dashboard-passkey-auth-design.md.
package lineupapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// canRegister allows enrolling a new passkey either from an already-logged-in
// session (adding a second device) or via the one-time bootstrap token (the
// very first passkey, or recovery if every passkey was ever lost/revoked).
func (cfg Config) canRegister(r *http.Request) bool {
	return hasValidSession(r, cfg.SessionSecret) || authorized(r, cfg.Token)
}

// identityMutateAttempts bounds the optimistic-concurrency retry loop. Losing
// a race means someone else's write landed between our read and our write; the
// contended resource is one small object touched by hand-driven browser
// ceremonies, so a handful of attempts is generous and an unbounded loop would
// only turn a stuck store into a hung request.
const identityMutateAttempts = 5

// errNoIdentity reports that a mutation was asked for against a record that
// does not exist. Every caller establishes the record first (register via
// loadOrCreateIdentity, login/revoke via a preceding GetIdentity), so this
// means the record was deleted mid-request, not that the caller skipped a step.
var errNoIdentity = errors.New("lineupapi: no identity record to update")

// loadOrCreateIdentity returns the existing Identity, or creates and
// immediately persists a brand new one if none exists yet. Persisting before
// returning is required: register/begin and register/finish each call this
// independently, and the WebAuthnUserID must be identical across both calls
// (it's baked into the ceremony's session data) or go-webauthn's
// CreateCredential rejects the finish with an "ID mismatch" error.
//
// The create is conditional on absence, and losing that race is resolved by
// adopting the winner's record rather than retrying our own. That is not
// merely politeness: each concurrent caller mints an independent random
// WebAuthnUserID, so overwriting the winner would invalidate the ceremony
// already running against it — the same "ID mismatch" failure, arrived at from
// the other direction.
func (cfg Config) loadOrCreateIdentity(ctx context.Context) (*Identity, error) {
	id, ok, err := cfg.Identities.GetIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		return id, nil
	}
	handle, err := newWebAuthnUserID()
	if err != nil {
		return nil, err
	}
	// Version is the zero value, which asserts "no record exists yet".
	id = &Identity{WebAuthnUserID: handle}
	err = cfg.Identities.PutIdentity(ctx, id)
	if errors.Is(err, ErrIdentityConflict) {
		id, ok, err = cfg.Identities.GetIdentity(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errNoIdentity
		}
		return id, nil
	}
	if err != nil {
		return nil, err
	}
	return id, nil
}

// mutateIdentity runs one read-modify-write against the Identity record under
// optimistic concurrency: read the current record, apply mutate to it, write
// conditional on the version it was read at, and on conflict start over
// against a freshly-read record.
//
// mutate is therefore called once per attempt and must be safe to re-apply to
// a record it has not seen before — it receives the *current* record, never a
// stale copy of its own earlier work. That is what makes a concurrent
// registration and login both survive: the loser re-reads the record including
// the winner's change and layers its own on top, instead of writing back a
// snapshot that never contained it (rosterbot-crq.2).
func (cfg Config) mutateIdentity(ctx context.Context, mutate func(*Identity) error) (*Identity, error) {
	var lastErr error
	for attempt := 0; attempt < identityMutateAttempts; attempt++ {
		current, ok, err := cfg.Identities.GetIdentity(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errNoIdentity
		}
		if err := mutate(current); err != nil {
			return nil, err
		}
		err = cfg.Identities.PutIdentity(ctx, current)
		if err == nil {
			return current, nil
		}
		if !errors.Is(err, ErrIdentityConflict) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (cfg Config) handleAuthRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !cfg.canRegister(r) {
		writeErr(w, http.StatusForbidden, "not authorized to register a passkey")
		return
	}
	identity, err := cfg.loadOrCreateIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	creation, session, err := cfg.WebAuthn.BeginRegistration(identityUser{id: identity},
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
	if !cfg.canRegister(r) {
		writeErr(w, http.StatusForbidden, "not authorized to register a passkey")
		return
	}
	session, err := ceremonySessionFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "registration session expired, try again")
		return
	}
	identity, err := cfg.loadOrCreateIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	cred, err := cfg.WebAuthn.FinishRegistration(identityUser{id: identity}, *session, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "passkey registration failed")
		return
	}
	// The credential is already minted; re-applying the append to whatever the
	// record looks like now is exactly right, and is what keeps a concurrent
	// login's sign-counter update from being clobbered (and vice versa).
	if _, err := cfg.mutateIdentity(r.Context(), func(cur *Identity) error {
		cur.Credentials = append(cur.Credentials, *cred)
		return nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save passkey")
		return
	}
	uid, err := cfg.ensureUserForIdentity(r.Context(), identity)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create user record")
		return
	}
	clearCeremonyCookie(w)
	// The session names WHO it belongs to (rosterbot-crq.8). Until the handlers
	// move onto the user store (rosterbot-crq.10) the subject is derived from
	// the singleton identity's handle — which is exactly the id
	// migrate-identity writes, so a session minted before the cutover names the
	// same user afterwards. TokenVersion is 0 because the singleton record has
	// no such field; it becomes user.TokenVersion when the store is wired.
	setSessionCookie(w, cfg.SessionSecret, uid, 0, time.Now())
	writeJSON(w, http.StatusOK, map[string]string{"status": "registered"})
}

func (cfg Config) handleAuthLoginBegin(w http.ResponseWriter, r *http.Request) {
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	if !ok || len(identity.Credentials) == 0 {
		writeErr(w, http.StatusNotFound, "no passkeys registered yet")
		return
	}
	assertion, session, err := cfg.WebAuthn.BeginLogin(identityUser{id: identity})
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

func (cfg Config) handleAuthLoginFinish(w http.ResponseWriter, r *http.Request) {
	session, err := ceremonySessionFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "login session expired, try again")
		return
	}
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "login failed")
		return
	}
	cred, err := cfg.WebAuthn.FinishLogin(identityUser{id: identity}, *session, r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login failed")
		return
	}
	// Persist the updated sign counter / clone-warning flag. Best-effort: a
	// store failure here shouldn't fail a login that already verified. The
	// mutation is applied to the record as it stands at write time, so a
	// passkey registered concurrently is not rolled back by this write; a
	// credential revoked concurrently simply isn't found and is left revoked.
	_, _ = cfg.mutateIdentity(r.Context(), func(cur *Identity) error {
		for i := range cur.Credentials {
			if bytes.Equal(cur.Credentials[i].ID, cred.ID) {
				cur.Credentials[i] = *cred
			}
		}
		return nil
	})

	uid, uerr := cfg.ensureUserForIdentity(r.Context(), identity)
	if uerr != nil {
		writeErr(w, http.StatusInternalServerError, "could not resolve user record")
		return
	}
	clearCeremonyCookie(w)
	setSessionCookie(w, cfg.SessionSecret, uid, 0, time.Now())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type passkeyOut struct {
	ID string `json:"id"`
}

func (cfg Config) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	if !hasValidSession(r, cfg.SessionSecret) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	out := []passkeyOut{}
	if ok {
		for _, c := range identity.Credentials {
			out = append(out, passkeyOut{ID: base64.RawURLEncoding.EncodeToString(c.ID)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

func (cfg Config) handleRevokePasskey(w http.ResponseWriter, r *http.Request) {
	if !hasValidSession(r, cfg.SessionSecret) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	targetID, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid passkey id")
		return
	}
	if _, err := cfg.mutateIdentity(r.Context(), func(cur *Identity) error {
		kept := cur.Credentials[:0]
		for _, c := range cur.Credentials {
			if !bytes.Equal(c.ID, targetID) {
				kept = append(kept, c)
			}
		}
		cur.Credentials = kept
		return nil
	}); err != nil {
		if errors.Is(err, errNoIdentity) {
			writeErr(w, http.StatusNotFound, "no passkeys registered")
			return
		}
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

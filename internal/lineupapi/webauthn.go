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

// loadOrCreateIdentity returns the existing Identity, or creates and
// immediately persists a brand new one if none exists yet. Persisting before
// returning is required: register/begin and register/finish each call this
// independently, and the WebAuthnUserID must be identical across both calls
// (it's baked into the ceremony's session data) or go-webauthn's
// CreateCredential rejects the finish with an "ID mismatch" error.
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
	id = &Identity{WebAuthnUserID: handle}
	if err := cfg.Identities.PutIdentity(ctx, id); err != nil {
		return nil, err
	}
	return id, nil
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
	identity.Credentials = append(identity.Credentials, *cred)
	if err := cfg.Identities.PutIdentity(r.Context(), identity); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save passkey")
		return
	}
	clearCeremonyCookie(w)
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
	// store failure here shouldn't fail a login that already verified.
	for i := range identity.Credentials {
		if bytes.Equal(identity.Credentials[i].ID, cred.ID) {
			identity.Credentials[i] = *cred
		}
	}
	_ = cfg.Identities.PutIdentity(r.Context(), identity)

	clearCeremonyCookie(w)
	setSessionCookie(w, cfg.SessionSecret, time.Now())
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
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "no passkeys registered")
		return
	}
	kept := identity.Credentials[:0]
	for _, c := range identity.Credentials {
		if !bytes.Equal(c.ID, targetID) {
			kept = append(kept, c)
		}
	}
	identity.Credentials = kept
	if err := cfg.Identities.PutIdentity(r.Context(), identity); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (cfg Config) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

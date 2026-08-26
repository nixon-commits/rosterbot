package lineupapi

import (
	"encoding/json"
	"net/http"

	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// MeResponse is what a signed-in user is told about themselves.
//
// It carries no ciphertext and no token_version. The ciphertexts are opaque,
// but an endpoint that emits them invites a client that stores them, and
// token_version is an internal revocation counter with no meaning to a browser.
type MeResponse struct {
	ID          UserID     `json:"id"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Role        Role       `json:"role"`
	Status      UserStatus `json:"status"`

	// AutoApply is the only field here a user may change, and the only one that
	// decides whether the bot writes to their roster.
	AutoApply bool `json:"auto_apply"`

	// TeamID is DISPLAYED, never submitted. It is proven at connect time
	// against Fantrax's own MyTeamIDs, so a value typed by a user would mean
	// nothing — it would simply make their next connect fail.
	TeamID string `json:"team_id,omitempty"`

	Fantrax *FantraxStatus `json:"fantrax,omitempty"`
}

// FantraxStatus is the connection state the settings page shows.
type FantraxStatus struct {
	Status       ConnStatus `json:"status"`
	LastError    string     `json:"last_error,omitempty"`
	FantraxEmail string     `json:"fantrax_email,omitempty"`

	// LastVerifiedAt is wiretime.Time, not time.Time: the stored value comes
	// from the connect task's time.Now(), so a raw field here emits a
	// variable-length RFC3339Nano fraction that the iOS client's strict
	// ISO8601 formatter decodes to nil — and a nil date here is a staleness
	// check that silently never fires (rosterbot-4e1j).
	LastVerifiedAt wiretime.Time `json:"last_verified_at,omitempty"`
}

// handleMe serves the caller their own profile.
//
// It exists for two reasons at once. The settings page needs the profile; and
// the SPA had no way to learn the caller's ROLE, so it could not render an
// admin-only tab at all — every user saw the same navigation, and every job in
// GET /v1/jobs, launchable or not.
//
// The role here is ADVISORY. Authorization stays server-side in adminOnlyRoutes
// and leagueWideJobs, so a member who edits this response in their browser
// gains a tab that answers 403.
func (cfg Config) handleMe(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		// The bearer token has no UserID by construction. "Me" has no answer
		// for it, and resolving it to the operator would make a break-glass
		// credential look like a person.
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	u, ok, err := cfg.Users.GetUser(r.Context(), caller.UserID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}

	resp := MeResponse{
		ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
		Role: u.Role, Status: u.Status, AutoApply: u.AutoApply, TeamID: u.TeamID,
	}
	// The connection is optional and its absence is ordinary — a user who has
	// not connected Fantrax yet is the normal state on day one.
	if cfg.Connections != nil {
		if conn, found, cerr := cfg.Connections.GetConnection(r.Context(), caller.UserID); cerr == nil && found {
			resp.Fantrax = &FantraxStatus{
				Status: conn.Status, LastError: conn.LastError,
				FantraxEmail:   conn.FantraxEmail,
				LastVerifiedAt: wiretime.New(conn.LastVerifiedAt),
			}
			if resp.TeamID == "" {
				resp.TeamID = conn.TeamID
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetPreferences updates the caller's own preferences.
//
// AUTO_APPLY IS THE ONLY WRITABLE FIELD, and that is deliberate rather than a
// starting point. Everything else on a profile is either attested by an admin
// (email), proven against Fantrax (team), or a security control (role, status,
// token_version) — a settings page that could set those would be a privilege
// escalation with a friendly label.
//
// THE SUBJECT COMES FROM THE SESSION, NEVER THE BODY. A user_id field in the
// request is ignored, because the one control that decides whether the bot
// writes to a roster must not be settable for somebody else's.
func (cfg Config) handleSetPreferences(w http.ResponseWriter, r *http.Request) {
	caller := CallerFrom(r.Context())
	if caller.UserID == "" {
		writeErr(w, http.StatusForbidden, "this endpoint requires a passkey session")
		return
	}
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}

	// A pointer so "field absent" is distinguishable from "set to false".
	// Without that, a request changing some future preference would silently
	// switch auto_apply off.
	var body struct {
		AutoApply *bool `json:"auto_apply"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if body.AutoApply == nil {
		writeErr(w, http.StatusBadRequest, "no preference supplied")
		return
	}

	// Read-modify-write: PutUser stores a whole record, so building a fresh one
	// here would erase the proven team binding, the attested email and the role.
	u, ok, err := cfg.Users.GetUser(r.Context(), caller.UserID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	u.AutoApply = *body.AutoApply
	if err := cfg.Users.PutUser(r.Context(), u); err != nil {
		writeErr(w, http.StatusBadGateway, "could not save preferences")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auto_apply": u.AutoApply})
}

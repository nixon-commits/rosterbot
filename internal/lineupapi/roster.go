package lineupapi

import (
	"context"
	"net/http"
)

// handleRosterValues serves the caller's own roster: every rostered player
// joined onto HKB value and 30-day momentum, one artifact per Fantrax team.
//
// Passthrough via serveBlob like /v1/pool/available, so the producer owns the
// schema and this package never parses the bytes. The key is the TEAM ID, so
// "whose roster" is decided here, once, by teamIDFor — and never by anything
// the request carries.
func (cfg Config) handleRosterValues(w http.ResponseWriter, r *http.Request) {
	view, ok := cfg.requireTenantView(w, r)
	if !ok {
		return
	}
	// Checked before resolution so a deployment that has not wired the store
	// reports that defect (501) rather than a misleading "no team" (404).
	if view.RosterValues == nil {
		writeErr(w, http.StatusNotImplemented, "roster values store not configured")
		return
	}
	teamID, err := cfg.teamIDFor(r.Context(), CallerFrom(r.Context()))
	if err != nil {
		// Never laundered into "no team": that would answer a transient
		// outage with the empty state and hide it.
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}
	if teamID == "" {
		// Ordinary for a member who has not connected Fantrax yet: there is
		// no team to have a roster. 404 is what the client maps to its empty
		// state, matching every other not-yet-produced artifact.
		writeErr(w, http.StatusNotFound, "no Fantrax team bound to this account")
		return
	}
	serveBlob(w, r, view.RosterValues, teamID, "roster values")
}

// teamIDFor is handleMe's resolution — the user record, then the connection —
// with the bearer caller mapped onto DefaultTenant first and DefaultTeamID as
// the final fallback. An empty result means "no team", not an error; only a
// failing user directory is an error, because "we could not ask" and "there
// is no answer" must reach the client as different statuses.
func (cfg Config) teamIDFor(ctx context.Context, caller Caller) (string, error) {
	uid := caller.UserID
	if uid == "" {
		uid = cfg.DefaultTenant
	}
	if uid != "" {
		if cfg.Users != nil {
			u, ok, err := cfg.Users.GetUser(ctx, uid)
			if err != nil {
				return "", err
			}
			if ok && u.TeamID != "" {
				return u.TeamID, nil
			}
		}
		if cfg.Connections != nil {
			if conn, ok, err := cfg.Connections.GetConnection(ctx, uid); err == nil && ok && conn.TeamID != "" {
				return conn.TeamID, nil
			}
		}
	}
	return cfg.DefaultTeamID, nil
}

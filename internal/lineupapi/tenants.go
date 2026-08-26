package lineupapi

import (
	"net/http"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

// TenantSummary is one tenant's row on the admin Tenants tab.
//
// It answers "what is failing, for whom" across N tenants without reading the
// ledger by hand, which is the question a pilot with other people's teams
// generates constantly.
type TenantSummary struct {
	ID          UserID     `json:"id"`
	DisplayName string     `json:"display_name,omitempty"`
	Email       string     `json:"email,omitempty"`
	Role        Role       `json:"role"`
	Status      UserStatus `json:"status"`

	// AutoApply is on the row because it is the difference between a bot that
	// proposes and one that writes to somebody's team. An operator scanning
	// this page is entitled to see, at a glance, whose rosters this deployment
	// is actually touching.
	AutoApply bool `json:"auto_apply"`

	TeamID string `json:"team_id,omitempty"`

	// Passkeys counts this tenant's registered credentials. Zero is the signal
	// the operator scans for — invited but never registered. nil means the
	// credential store could not answer, which must render as unknown rather
	// than as zero: a masked error that prints 0 would tell the operator to
	// re-invite someone whose account is fine.
	Passkeys *int `json:"passkeys,omitempty"`

	// ConnStatus is empty when the tenant has never connected Fantrax, which is
	// different from a failed connection and is shown differently.
	ConnStatus ConnStatus `json:"conn_status,omitempty"`
	ConnError  string     `json:"conn_error,omitempty"`

	// LastVerifiedAt is wiretime.Time for the reason spelled out on
	// FantraxStatus.LastVerifiedAt (me.go): the same stored value, fed by the
	// connect task's time.Now(), reaching the same client (rosterbot-4e1j).
	LastVerifiedAt wiretime.Time `json:"last_verified_at,omitempty"`
}

// TenantsResponse is the admin tab's payload.
type TenantsResponse struct {
	Tenants []TenantSummary `json:"tenants"`
}

// handleTenants lists every tenant for an operator.
//
// ADMIN-ONLY, and enforced by adminOnlyRoutes rather than by a check here. That
// list is an ALLOWLIST, so a route absent from it is reachable by every member
// — this endpoint exposes every tenant's email, team and connection state, so
// its entry there is the load-bearing part of this file, not this function.
//
// It deliberately reports no per-tenant run history. Doing that would mean N
// ledger listings inside one request, and the run ledger is already served
// per-caller by GET /v1/runs; an operator who needs a tenant's runs has a
// place to look. The rows here are the identity-store facts, which are one
// read.
func (cfg Config) handleTenants(w http.ResponseWriter, r *http.Request) {
	if cfg.Users == nil {
		writeErr(w, http.StatusNotImplemented, "user directory not configured")
		return
	}
	// ListUsers, not ListActive: this page is the only reactivate control, so
	// a parked tenant must stay on it. The fan-out's listing rightly differs.
	users, err := cfg.Users.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "user directory unavailable")
		return
	}

	out := make([]TenantSummary, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		s := TenantSummary{
			ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
			Role: u.Role, Status: u.Status, AutoApply: u.AutoApply, TeamID: u.TeamID,
		}
		// Soft, like the connection read below: a credentials-store hiccup
		// leaves the count nil ("unknown"), never zero — zero is a real value
		// meaning "invited but never registered", and conflating the two would
		// tell the operator to re-invite someone whose account is fine.
		if creds, cerr := cfg.Users.Credentials(r.Context(), u.ID); cerr == nil {
			n := len(creds)
			s.Passkeys = &n
		}
		// Soft: a connection-store hiccup must not blank the whole page. A row
		// with no connection state reads as "unknown", which is honest, where a
		// 502 for the entire list would hide the tenants that are fine.
		if cfg.Connections != nil {
			if conn, ok, cerr := cfg.Connections.GetConnection(r.Context(), u.ID); cerr == nil && ok {
				s.ConnStatus, s.ConnError = conn.Status, conn.LastError
				s.LastVerifiedAt = wiretime.New(conn.LastVerifiedAt)
				if s.TeamID == "" {
					s.TeamID = conn.TeamID
				}
			}
		}
		out = append(out, s)
	}
	// Stable order so the page does not reshuffle between polls.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	writeJSON(w, http.StatusOK, TenantsResponse{Tenants: out})
}

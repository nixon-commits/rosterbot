package lineupapi

import (
	"net/http"
	"sort"
	"time"
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

	// ConnStatus is empty when the tenant has never connected Fantrax, which is
	// different from a failed connection and is shown differently.
	ConnStatus ConnStatus `json:"conn_status,omitempty"`
	ConnError  string     `json:"conn_error,omitempty"`

	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`
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
	users, err := cfg.Users.ListActive(r.Context())
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
		// Soft: a connection-store hiccup must not blank the whole page. A row
		// with no connection state reads as "unknown", which is honest, where a
		// 502 for the entire list would hide the tenants that are fine.
		if cfg.Connections != nil {
			if conn, ok, cerr := cfg.Connections.GetConnection(r.Context(), u.ID); cerr == nil && ok {
				s.ConnStatus, s.ConnError = conn.Status, conn.LastError
				s.LastVerifiedAt = conn.LastVerifiedAt
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

package lineupapi

import "time"

// Platform names a fantasy provider.
//
// A named string rather than a bool-per-platform because the set grows: the
// question a caller asks is "which provider", and every alternative encoding
// answers it by elimination.
type Platform string

const (
	PlatformFantrax Platform = "fantrax"
	PlatformSleeper Platform = "sleeper"
)

// Membership is one league a tenant belongs to, on any platform.
type Membership struct {
	Platform Platform `json:"platform"`

	// LeagueID is empty for Fantrax, and that is not an omission. The Fantrax
	// league is a property of the DEPLOYMENT (FANTRAX_LEAGUE_ID, one env var in
	// internal/config), not of the tenant, so there is no per-tenant value to
	// put here. Threading config into this package to fill it in would couple
	// the identity layer to the optimizer's configuration for a display string.
	LeagueID string `json:"league_id,omitempty"`

	// TeamID is the Fantrax team id, or the Sleeper user id that owns a roster
	// in this league. On both platforms it answers "which team here is yours".
	TeamID      string `json:"team_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`

	// Writable is whether RosterBot can act on this league. Permanently false
	// for Sleeper, whose public API has no write endpoints.
	//
	// Stored rather than derived from Platform at each call site: a capability
	// check that is a field read cannot be got wrong by a third platform the
	// way a repeated string comparison can.
	Writable bool `json:"writable"`

	AddedAt time.Time `json:"added_at"`
}

// AllMemberships returns the tenant's leagues across platforms, Fantrax first.
//
// It PROJECTS TeamID rather than reading a stored Fantrax membership. That is
// the whole trick: callers get one uniform list, while the field the connect
// flow proves against Fantrax's own MyTeamIDs — and that the store enforces
// uniqueness on — stays singular and untouched.
//
// A tenant with no proven team yields no Fantrax entry. An empty TeamID is
// "not connected yet", and a membership claiming a league with no team in it
// would be a fact nobody established.
func (u *User) AllMemberships() []Membership {
	out := make([]Membership, 0, len(u.Memberships)+1)
	if u.TeamID != "" {
		out = append(out, Membership{
			Platform: PlatformFantrax,
			TeamID:   u.TeamID,
			Writable: true,
			AddedAt:  u.CreatedAt,
		})
	}
	// append onto a fresh slice, never returning u.Memberships itself — a
	// caller appending to the result would otherwise write into the record.
	out = append(out, u.Memberships...)
	return out
}

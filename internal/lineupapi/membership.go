package lineupapi

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/wiretime"
)

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
	//
	// Writable is forced false on the way past rather than trusted from the
	// store. Only Fantrax, projected above, is writable, and clients read this
	// field to decide whether to offer a write action — so "the one
	// constructor always sets it false" being a convention rather than a
	// guarantee is not good enough. A hand-edited record or a second
	// constructor added later would otherwise put a true in front of a user.
	// m is a copy, so the stored membership is untouched.
	for _, m := range u.Memberships {
		if m.Platform != PlatformFantrax {
			m.Writable = false
		}
		out = append(out, m)
	}
	return out
}

// MembershipOut is one membership as a CLIENT sees it, and it exists solely so
// AddedAt can be a wiretime.Time.
//
// Membership itself must keep its time.Time: it is stored inside the user
// record, which FileUserStore writes as JSON and ddbuser writes through
// attributevalue — which ignores json tags entirely and encodes a struct as a
// time only when the type is ConvertibleTo(time.Time). wiretime.Time wraps its
// instant in an UNEXPORTED field, so it is not convertible, and attributevalue
// falls through to the generic struct path and emits an empty M. Measured: a
// time.Time field marshals to an S attribute, the wrapper to an M with no
// members — so converting the stored struct would silently persist no timestamp
// at all, which is worse than the durable-format churn rosterbot-4e1j rules out.
// This is the other half of that rule: where a durable record is also served on
// the wire, convert at the response boundary.
//
// Field order mirrors Membership exactly, because encoding/json emits struct
// order and these keys are an existing contract.
type MembershipOut struct {
	Platform    Platform      `json:"platform"`
	LeagueID    string        `json:"league_id,omitempty"`
	TeamID      string        `json:"team_id,omitempty"`
	DisplayName string        `json:"display_name,omitempty"`
	Writable    bool          `json:"writable"`
	AddedAt     wiretime.Time `json:"added_at"`
}

// MembershipsOut projects the stored memberships onto the wire type. Every
// /v1/memberships response goes through it; handing AllMemberships' result
// straight to writeJSON is the mistake it exists to remove.
//
// Non-nil for an empty input, so the JSON is [] rather than null — the three
// handlers all report the caller's whole list, and a client distinguishing
// "no leagues" from "field missing" would be reading a difference that means
// nothing.
func MembershipsOut(ms []Membership) []MembershipOut {
	out := make([]MembershipOut, 0, len(ms))
	for _, m := range ms {
		out = append(out, MembershipOut{
			Platform:    m.Platform,
			LeagueID:    m.LeagueID,
			TeamID:      m.TeamID,
			DisplayName: m.DisplayName,
			Writable:    m.Writable,
			AddedAt:     wiretime.New(m.AddedAt),
		})
	}
	return out
}

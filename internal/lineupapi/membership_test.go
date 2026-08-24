package lineupapi

import (
	"testing"
	"time"
)

func TestAllMembershipsProjectsFantraxTeam(t *testing.T) {
	created := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	u := &User{TeamID: "7", CreatedAt: created}

	got := u.AllMemberships()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Platform != PlatformFantrax {
		t.Errorf("Platform = %q, want %q", got[0].Platform, PlatformFantrax)
	}
	if got[0].TeamID != "7" {
		t.Errorf("TeamID = %q, want 7", got[0].TeamID)
	}
	if !got[0].Writable {
		t.Error("Writable = false, want true — Fantrax is the platform we write to")
	}
	if !got[0].AddedAt.Equal(created) {
		t.Errorf("AddedAt = %v, want %v", got[0].AddedAt, created)
	}
}

// The every-existing-tenant case: no record in the store has a Memberships
// field, so it decodes to nil and must render as "Fantrax only".
func TestAllMembershipsWithNilSliceIsFantraxOnly(t *testing.T) {
	u := &User{TeamID: "7", Memberships: nil}
	if len(u.AllMemberships()) != 1 {
		t.Fatalf("len = %d, want 1", len(u.AllMemberships()))
	}
}

// A tenant invited but not yet connected has no proven team, so there is no
// Fantrax membership to project. Inventing one with an empty TeamID would
// claim a league binding that does not exist.
func TestAllMembershipsOmitsFantraxWhenNoTeamProven(t *testing.T) {
	u := &User{TeamID: ""}
	if len(u.AllMemberships()) != 0 {
		t.Fatalf("len = %d, want 0", len(u.AllMemberships()))
	}
}

func TestAllMembershipsAppendsSleeperAfterFantrax(t *testing.T) {
	u := &User{
		TeamID: "7",
		Memberships: []Membership{
			{Platform: PlatformSleeper, LeagueID: "123", DisplayName: "Dynasty"},
		},
	}
	got := u.AllMemberships()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Platform != PlatformFantrax {
		t.Errorf("first = %q, want fantrax first", got[0].Platform)
	}
	if got[1].LeagueID != "123" {
		t.Errorf("LeagueID = %q, want 123", got[1].LeagueID)
	}
	if got[1].Writable {
		t.Error("Writable = true for a Sleeper membership; Sleeper has no write API")
	}
}

// The projection must not alias the stored slice: a caller appending to the
// result would otherwise mutate the user record it came from.
func TestAllMembershipsDoesNotAliasStoredSlice(t *testing.T) {
	u := &User{Memberships: []Membership{{Platform: PlatformSleeper, LeagueID: "1"}}}
	got := u.AllMemberships()
	got[0].LeagueID = "mutated"
	if u.Memberships[0].LeagueID != "1" {
		t.Errorf("stored LeagueID = %q, want 1 — AllMemberships aliased the slice",
			u.Memberships[0].LeagueID)
	}
}

// Writable is what a client reads to decide whether to offer a write action,
// and the spec justified denormalising it on the grounds that exactly one
// constructor sets it. That is a convention, not a guarantee: a hand-edited
// record, a restored backup, or a second constructor added later all put a
// true on disk with nothing to stop them. The projection is the one place
// every reader passes through, so enforce it there and the invariant holds
// whatever the store says.
func TestAllMembershipsForcesSleeperUnwritable(t *testing.T) {
	u := &User{
		TeamID: "7",
		Memberships: []Membership{
			{Platform: PlatformSleeper, LeagueID: "123", Writable: true},
		},
	}
	got := u.AllMemberships()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[1].Writable {
		t.Error("a stored Writable:true Sleeper membership was projected verbatim; " +
			"Sleeper has no write API, so no route may report one as writable")
	}
	if !got[0].Writable {
		t.Error("the projected Fantrax membership lost Writable; it is the platform " +
			"this bot actually writes to")
	}
	// The stored record must not be edited on the way past — AllMemberships is
	// a read, and mutateUser writes back whatever the User holds.
	if !u.Memberships[0].Writable {
		t.Error("AllMemberships mutated the stored membership rather than its copy")
	}
}

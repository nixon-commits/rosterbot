package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func seedTeamUser(t *testing.T, s lineupapi.UserStore, id lineupapi.UserID, team string) {
	t.Helper()
	u := &lineupapi.User{
		ID: id, Email: string(id) + "@example.test", DisplayName: string(id),
		Role: lineupapi.RoleMember, Status: lineupapi.UserActive, TeamID: team,
	}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("seeding CreateUser(%s): %v", id, err)
	}
}

// TestSetTeam_AssignsToAUserWithNoTeam is the case this command exists for.
//
// rosterbot-crq.18 made connect refuse a user whose record names no team, which
// is correct — an empty team gave the MyTeamIDs check nothing to compare, so the
// connection reached verified having proven nothing. But it left no way to fix
// such a record: invite sets TeamID only at creation, and ClaimTeam's sole
// caller was connect itself, which can no longer get there.
func TestSetTeam_AssignsToAUserWithNoTeam(t *testing.T) {
	s := lineupapi.NewFileUserStore(t.TempDir())
	seedTeamUser(t, s, "alice", "")

	if err := setTeam(context.Background(), s, "alice", "team-7"); err != nil {
		t.Fatalf("setTeam: %v", err)
	}

	u, ok, err := s.GetUser(context.Background(), "alice")
	if err != nil || !ok {
		t.Fatalf("GetUser: %v ok=%v", err, ok)
	}
	if u.TeamID != "team-7" {
		t.Errorf("TeamID = %q, want team-7", u.TeamID)
	}
}

// TestSetTeam_IsIdempotent: re-running with the team the user already holds is
// success, not ErrTeamTaken. An admin who cannot tell whether the first run
// landed must be able to run it again.
func TestSetTeam_IsIdempotent(t *testing.T) {
	s := lineupapi.NewFileUserStore(t.TempDir())
	seedTeamUser(t, s, "alice", "team-7")

	if err := setTeam(context.Background(), s, "alice", "team-7"); err != nil {
		t.Fatalf("re-assigning the same team failed: %v", err)
	}
}

// TestSetTeam_RefusesATeamHeldByAnotherUser is the load-bearing refusal.
//
// usertest states the consequence: two users holding one Fantrax team means
// both tenants' hourly jobs optimize and apply to the same roster, each reading
// the other's changes as drift, fighting every hour, invisibly. It must be an
// error, never a warning.
func TestSetTeam_RefusesATeamHeldByAnotherUser(t *testing.T) {
	s := lineupapi.NewFileUserStore(t.TempDir())
	seedTeamUser(t, s, "alice", "team-7")
	seedTeamUser(t, s, "bob", "")

	err := setTeam(context.Background(), s, "bob", "team-7")
	if err == nil {
		t.Fatal("setTeam let a second user claim a team alice already holds")
	}
	if !errors.Is(err, lineupapi.ErrTeamTaken) {
		t.Errorf("error = %v, want it to wrap ErrTeamTaken so the caller can tell "+
			"this apart from an infrastructure failure", err)
	}

	u, _, _ := s.GetUser(context.Background(), "alice")
	if u.TeamID != "team-7" {
		t.Errorf("alice lost her team to a refused claim: %q", u.TeamID)
	}
}

// TestSetTeam_RefusesReassigningAUserWhoAlreadyHasADifferentTeam guards a leak
// in ClaimTeam rather than in this command.
//
// ClaimTeam writes the NEW team's claim file and updates the profile, but never
// releases the OLD team's claim. Reassigning alice from team-7 to team-9 would
// leave team-7 recorded as hers in the claim index while her profile says
// team-9 — so team-7 becomes permanently unassignable to anyone, with nothing
// pointing at why.
//
// Refusing here keeps the damage impossible without pushing a release path into
// ClaimTeam, whose other implementation (ddbuser) would need the same change
// and whose only other caller is connect. A genuine reassignment is rare enough
// to be worth doing deliberately.
func TestSetTeam_RefusesReassigningAUserWhoAlreadyHasADifferentTeam(t *testing.T) {
	s := lineupapi.NewFileUserStore(t.TempDir())
	seedTeamUser(t, s, "alice", "team-7")

	err := setTeam(context.Background(), s, "alice", "team-9")
	if err == nil {
		t.Fatal("setTeam silently reassigned a user who already held a different team; " +
			"ClaimTeam does not release the old claim, so team-7 would be orphaned")
	}

	u, _, _ := s.GetUser(context.Background(), "alice")
	if u.TeamID != "team-7" {
		t.Errorf("a refused reassignment still changed TeamID to %q", u.TeamID)
	}
}

// TestSetTeam_RefusesAnUnknownUser: assigning a team to a user id that does not
// exist must fail loudly. Silently succeeding would leave a claim file pointing
// at nobody, which is the same orphaned-claim shape as above.
func TestSetTeam_RefusesAnUnknownUser(t *testing.T) {
	s := lineupapi.NewFileUserStore(t.TempDir())

	if err := setTeam(context.Background(), s, "nobody", "team-7"); err == nil {
		t.Fatal("setTeam accepted a user id that does not exist")
	}
}

// TestSetTeam_RefusesAnEmptyTeam closes the loop with crq.18: an empty team is
// exactly the state connect now rejects, so writing one here would be a command
// whose only effect is to recreate the bug it exists to fix.
func TestSetTeam_RefusesAnEmptyTeam(t *testing.T) {
	s := lineupapi.NewFileUserStore(t.TempDir())
	seedTeamUser(t, s, "alice", "")

	if err := setTeam(context.Background(), s, "alice", ""); err == nil {
		t.Fatal("setTeam accepted an empty team; that is the exact state connect " +
			"refuses with no_team")
	}
}

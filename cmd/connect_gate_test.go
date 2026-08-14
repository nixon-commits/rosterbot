package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestProveTeam is the gate standing between a successful Fantrax login and
// ConnVerified.
//
// The bug it closes: both halves of the ownership step in runConnect were
// guarded on `conn.TeamID != ""`, and the ConnVerified assignment after them
// was not. An empty team therefore skipped the MyTeamIDs check AND the team
// claim and fell straight through to verified — a connection marked proven
// with nothing about it proven. Every user-creation path can produce that empty
// team: `invite` without --team, migrate-identity, and the bootstrap profile in
// ensureUserForIdentity, none of which set TeamID.
//
// Extracting the decision is what makes it assertable at all: runConnect around
// it needs DynamoDB, KMS and a headless browser, so the rule would otherwise be
// checkable only in production, against the one path whose whole job is to be
// the check.
func TestProveTeam(t *testing.T) {
	for _, tc := range []struct {
		name  string
		team  string
		owned []string
		want  string
	}{
		{
			name:  "team is among those the credentials control",
			team:  "team-7",
			owned: []string{"team-3", "team-7"},
			want:  "",
		},
		{
			name:  "credentials are valid but control a different team",
			team:  "team-7",
			owned: []string{"team-3"},
			want:  lineupapi.ConnErrTeamNotOwned,
		},
		{
			name:  "no team on the record, so there is nothing to prove",
			team:  "",
			owned: []string{"team-3"},
			want:  lineupapi.ConnErrNoTeam,
		},
		{
			// Pins the decision taken in rosterbot-crq.18 rather than merely the
			// current behaviour. Binding the sole owned team here would be a
			// defensible product choice, but it discards the admin's invite as
			// an independent cross-check on a misdirected invitation. If that
			// trade is revisited it should be by editing this case, not by
			// discovering the gate quietly stopped firing.
			name:  "a single owned team is still not auto-bound",
			team:  "",
			owned: []string{"team-3"},
			want:  lineupapi.ConnErrNoTeam,
		},
		{
			name:  "ownership could not be determined and no team was named",
			team:  "",
			owned: nil,
			want:  lineupapi.ConnErrNoTeam,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, class := proveTeam(tc.team, tc.owned)
			if class != tc.want {
				t.Fatalf("proveTeam(%q, %v) class = %q, want %q", tc.team, tc.owned, class, tc.want)
			}
			if tc.want == "" && proof.teamID != tc.team {
				t.Errorf("a proven team yielded proof %q, want %q", proof.teamID, tc.team)
			}
			if tc.want != "" && proof.teamID != "" {
				t.Errorf("a REJECTED team still yielded a usable proof %q", proof.teamID)
			}
		})
	}
}

// TestProveTeam_EmptyTeamNeverReportsProven states the security-carrying half
// on its own: a caller treating "" as proven marks a tenant verified without
// Fantrax having confirmed anything.
func TestProveTeam_EmptyTeamNeverReportsProven(t *testing.T) {
	for _, owned := range [][]string{nil, {}, {"team-1"}, {"team-1", "team-2"}} {
		if _, class := proveTeam("", owned); class == "" {
			t.Fatalf("proveTeam(\"\", %v) reported the team as proven; an empty team "+
				"means there is nothing MyTeamIDs could have confirmed", owned)
		}
	}
}

// TestMarkVerified_RefusesAnInertProof is what makes the gate structural rather
// than conventional, and it exists because the table above provably was not
// enough.
//
// Reverting runConnect's call site to the old inline `if conn.TeamID != ""`
// guard restores the original bug and leaves every test above GREEN — they
// exercise the decision function, not the caller. That is two of three parties
// agreeing while the third quietly walks away.
//
// Requiring a teamProof to reach the verified write closes it from the other
// side: a call site that drops proveTeam has no proof to pass and does not
// compile. Go cannot stop an in-package literal from forging `teamProof{}` —
// the same caveat applyAuthorization carries in internal/lineuprun — so the
// zero value is inert and refused here.
func TestMarkVerified_RefusesAnInertProof(t *testing.T) {
	conn := &lineupapi.FantraxConnection{UserID: "alice", Status: lineupapi.ConnPending}

	err := markVerified(conn, teamProof{}, []byte("sealed"), &fantraxUserInfo{UserID: "fx-1"}, time.Now())
	if err == nil {
		t.Fatal("markVerified accepted a zero-value teamProof; the forged-proof path is " +
			"the only one Go's type system cannot close, so it must be refused here")
	}
	if conn.Status == lineupapi.ConnVerified {
		t.Fatal("a refused markVerified still marked the connection verified")
	}
}

// TestMarkVerified_WritesTheVerifiedState is the happy half: given a real proof
// it records the session and the Fantrax identity, clears any previous error,
// and binds the team from the PROOF rather than from whatever the record
// happened to carry.
func TestMarkVerified_WritesTheVerifiedState(t *testing.T) {
	conn := &lineupapi.FantraxConnection{
		UserID: "alice", TeamID: "team-7",
		Status: lineupapi.ConnNeedsReconnect, LastError: lineupapi.ConnErrBadCredentials,
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	proof, class := proveTeam("team-7", []string{"team-7"})
	if class != "" {
		t.Fatalf("seeding proveTeam: %q", class)
	}

	if err := markVerified(conn, proof, []byte("sealed-fxrm"),
		&fantraxUserInfo{UserID: "fx-1", Email: "a@example.test"}, now); err != nil {
		t.Fatalf("markVerified: %v", err)
	}

	if conn.Status != lineupapi.ConnVerified {
		t.Errorf("status = %q, want %q", conn.Status, lineupapi.ConnVerified)
	}
	if conn.LastError != "" {
		t.Errorf("LastError = %q, want it cleared on a successful reconnect", conn.LastError)
	}
	if conn.TeamID != "team-7" {
		t.Errorf("TeamID = %q, want the proven team", conn.TeamID)
	}
	if string(conn.FXRMCiphertext) != "sealed-fxrm" {
		t.Errorf("FXRMCiphertext = %q, want the sealed session", conn.FXRMCiphertext)
	}
	if conn.FantraxUserID != "fx-1" || conn.FantraxEmail != "a@example.test" {
		t.Errorf("fantrax identity = %q/%q, want fx-1/a@example.test", conn.FantraxUserID, conn.FantraxEmail)
	}
	if !conn.LastVerifiedAt.Equal(now) {
		t.Errorf("LastVerifiedAt = %v, want %v", conn.LastVerifiedAt, now)
	}
}

// TestMarkVerified_RefusesAMissingSession: an FX_RM that never got sealed is
// not a verified connection either. Reaching verified with no session cookie
// would leave a tenant that every later job has to re-login for, recorded as
// though it were healthy.
func TestMarkVerified_RefusesAMissingSession(t *testing.T) {
	conn := &lineupapi.FantraxConnection{UserID: "alice", TeamID: "team-7", Status: lineupapi.ConnPending}
	proof, _ := proveTeam("team-7", []string{"team-7"})

	if err := markVerified(conn, proof, nil, &fantraxUserInfo{UserID: "fx-1"}, time.Now()); err == nil {
		t.Fatal("markVerified accepted an empty sealed session")
	}
	if conn.Status == lineupapi.ConnVerified {
		t.Fatal("a refused markVerified still marked the connection verified")
	}
}

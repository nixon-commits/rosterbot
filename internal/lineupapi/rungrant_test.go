package lineupapi

import "testing"

func verifiedConn(uid UserID) *FantraxConnection {
	return &FantraxConnection{
		UserID:          uid,
		TeamID:          "team-7",
		Status:          ConnVerified,
		CredsCiphertext: []byte("sealed-creds"),
		FXRMCiphertext:  []byte("sealed-fxrm"),
	}
}

// TestAuthorizeRun_VerifiedTenantIsGranted is the only path that yields a
// usable grant, and it carries the team from the RECORD rather than from the
// environment — the deployment's FANTRAX_TEAM_ID must never decide which roster
// a tenant's job writes to.
func TestAuthorizeRun_VerifiedTenantIsGranted(t *testing.T) {
	g, reason := AuthorizeRun("alice", verifiedConn("alice"))

	if reason != "" {
		t.Fatalf("a verified connection was refused: %s", reason)
	}
	if !g.Valid() {
		t.Fatal("granted but not Valid(); the zero value must be the only invalid one")
	}
	if g.TeamID != "team-7" {
		t.Errorf("TeamID = %q, want the team on the record", g.TeamID)
	}
	if string(g.FXRM) != "sealed-fxrm" || string(g.Creds) != "sealed-creds" {
		t.Error("the grant did not carry the sealed material the job needs")
	}
}

// TestAuthorizeRun_RefusalsNeverYieldAUsableGrant is the security property.
//
// Every refusal must produce a grant that cannot be acted on. A caller that
// ignores the reason string and uses the grant anyway must get nothing — which
// is what stops the dangerous fallback this ticket exists to prevent: a task
// with no tenant credential silently acting as the OPERATOR against whatever
// team id it was handed.
func TestAuthorizeRun_RefusalsNeverYieldAUsableGrant(t *testing.T) {
	noSession := verifiedConn("alice")
	noSession.FXRMCiphertext = nil

	noTeam := verifiedConn("alice")
	noTeam.TeamID = ""

	noCreds := verifiedConn("alice")
	noCreds.CredsCiphertext = nil

	// The two status cases carry a COMPLETE record and differ from a grantable
	// one only in Status. An earlier version of this table used bare records
	// with no team, which made the "refused grants carry nothing" assertion
	// vacuous for them: leaking conn.TeamID would have leaked "" and no test
	// would have noticed. Proven by mutation.
	pending := verifiedConn("alice")
	pending.Status = ConnPending

	needsReconnect := verifiedConn("alice")
	needsReconnect.Status = ConnNeedsReconnect

	for _, tc := range []struct {
		name string
		conn *FantraxConnection
		want string
	}{
		{"never connected", nil, RunDeniedNoConnection},
		{"still pending verification", pending, RunDeniedNotVerified},
		{"needs reconnect", needsReconnect, RunDeniedNeedsReconnect},
		{"verified but no session", noSession, RunDeniedNoSession},
		{"verified but no team", noTeam, RunDeniedNoTeam},
		{"verified but no credentials to re-login with", noCreds, RunDeniedNoCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, reason := AuthorizeRun("alice", tc.conn)
			if reason != tc.want {
				t.Errorf("reason = %q, want %q", reason, tc.want)
			}
			if g.Valid() {
				t.Fatal("a REFUSED authorization still produced a usable grant")
			}
			if g.TeamID != "" || g.FXRM != nil || g.Creds != nil {
				t.Error("a refused grant carried material a careless caller could act on")
			}
		})
	}
}

// TestAuthorizeRun_NeedsReconnectIsTheNoRetryRule is the heart of this ticket,
// and the reason it needs no cooldown timer or attempt counter.
//
// The bead states two requirements — refuse to run unless verified, and do NOT
// retry after a failed re-login — and they are the same gate. A failed re-login
// writes needs_reconnect; this check then refuses every subsequent scheduled
// run for free, because needs_reconnect is not verified.
//
// That matters concretely: the hourly Lineup job runs ~14 times a day. Retrying
// a rejected password on each one would lock a pilot out of their own Fantrax
// account with credentials they handed us, and Fantrax/Cloudflare would start
// treating our egress IP as a bot. The refusal has to be sticky until a human
// reconnects, and here it is sticky by construction rather than by a timer
// someone has to remember to check.
func TestAuthorizeRun_NeedsReconnectIsTheNoRetryRule(t *testing.T) {
	broken := verifiedConn("alice")
	broken.Status = ConnNeedsReconnect
	broken.LastError = ConnErrBadCredentials

	// Everything else about the record is still intact — sealed credentials,
	// a sealed session, a proven team. Only the status says stop. A gate that
	// keyed off "do we have material to try with?" would happily retry here,
	// fourteen times a day.
	for attempt := 1; attempt <= 3; attempt++ {
		g, reason := AuthorizeRun("alice", broken)
		if reason != RunDeniedNeedsReconnect {
			t.Fatalf("attempt %d: reason = %q, want %q", attempt, reason, RunDeniedNeedsReconnect)
		}
		if g.Valid() {
			t.Fatalf("attempt %d: a needs_reconnect tenant was allowed to run", attempt)
		}
	}
}

// TestAuthorizeRun_RefusesAMismatchedRecord guards against a record fetched for
// the wrong tenant being acted on — the failure that would have one user's job
// driving another user's roster.
func TestAuthorizeRun_RefusesAMismatchedRecord(t *testing.T) {
	g, reason := AuthorizeRun("alice", verifiedConn("bob"))

	if reason != RunDeniedWrongTenant {
		t.Errorf("reason = %q, want %q", reason, RunDeniedWrongTenant)
	}
	if g.Valid() {
		t.Fatal("a connection belonging to another tenant produced a usable grant")
	}
}

// TestRunGrant_ZeroValueIsInert states the invariant the refusal tests rely on,
// separately, because it is what makes "return an empty grant" a safe way to
// refuse at every site above.
func TestRunGrant_ZeroValueIsInert(t *testing.T) {
	var g RunGrant
	if g.Valid() {
		t.Fatal("the zero RunGrant reports itself usable; every refusal path returns exactly this")
	}
}

// TestRunGrant_EveryFieldIsRequired exercises Valid() against PARTIAL grants,
// which nothing else does.
//
// AuthorizeRun only ever returns the zero grant or a complete one, so its own
// tests cannot distinguish "Valid checks four fields" from "Valid checks
// UserID" — deleting three of the four conditions left the whole suite green.
// Valid() is the guard a future caller will lean on when it builds a grant some
// other way (a cached one, a fan-out passing it between phases), and it has to
// hold for those too.
func TestRunGrant_EveryFieldIsRequired(t *testing.T) {
	full := RunGrant{UserID: "alice", TeamID: "team-7", FXRM: []byte("s"), Creds: []byte("c")}
	if !full.Valid() {
		t.Fatal("a complete grant reported invalid")
	}

	for _, tc := range []struct {
		name string
		g    RunGrant
	}{
		{"no user", RunGrant{TeamID: "team-7", FXRM: []byte("s"), Creds: []byte("c")}},
		{"no team", RunGrant{UserID: "alice", FXRM: []byte("s"), Creds: []byte("c")}},
		{"no session", RunGrant{UserID: "alice", TeamID: "team-7", Creds: []byte("c")}},
		{"no credentials", RunGrant{UserID: "alice", TeamID: "team-7", FXRM: []byte("s")}},
		{"empty session slice", RunGrant{UserID: "alice", TeamID: "team-7", FXRM: []byte{}, Creds: []byte("c")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.g.Valid() {
				t.Errorf("a grant with %s reported itself usable", tc.name)
			}
		})
	}
}

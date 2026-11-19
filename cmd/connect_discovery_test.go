package cmd

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Team discovery at the CALL SITE (rosterbot-kr89). proveTeam's table pins the
// decision; these pin what connectTenant WRITES for each answer, which is the
// level the previous connect beads found their bugs at.

func teamlessConn(t *testing.T) *postAuthConns {
	t.Helper()
	conn := connFor(t, "alice", lineupapi.ConnPending)
	conn.TeamID = "" // a signed-up account: nobody named a team
	return &postAuthConns{conn: conn}
}

func ownedTeams(ids ...string) func() ([]lineupapi.OwnedTeam, error) {
	return func() ([]lineupapi.OwnedTeam, error) {
		out := make([]lineupapi.OwnedTeam, 0, len(ids))
		for _, id := range ids {
			out = append(out, lineupapi.OwnedTeam{ID: id, Name: "Team " + id})
		}
		return out, nil
	}
}

func lastPut(t *testing.T, conns *postAuthConns) lineupapi.FantraxConnection {
	t.Helper()
	if len(conns.put) == 0 {
		t.Fatal("nothing was written to the connection record")
	}
	return conns.put[len(conns.put)-1]
}

func TestConnectTenant_DiscoversTheSoleOwnedTeam(t *testing.T) {
	conns := teamlessConn(t)
	d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})
	d.teams = ownedTeams("7")

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant: %v", err)
	}

	got := lastPut(t, conns)
	if got.Status != lineupapi.ConnVerified || got.TeamID != "7" {
		t.Fatalf("wrote status %q team %q, want verified team 7", got.Status, got.TeamID)
	}
	if !reflect.DeepEqual(conns.claimed, []string{"7"}) {
		t.Errorf("ClaimTeam calls = %v, want exactly the discovered team", conns.claimed)
	}
	if len(got.OwnedTeams) != 0 {
		t.Errorf("a verified record carries an owned set %v; it is for the ambiguous class only", got.OwnedTeams)
	}
}

// The spec's D5 and the bead's acceptance line: an ambiguous connection never
// reaches ConnVerified and never calls ClaimTeam.
func TestConnectTenant_SeveralOwnedTeamsAreNeverResolvedByPicking(t *testing.T) {
	conns, feed := teamlessConn(t), &memFeed{}
	d := postAuthDeps(t, conns, feed, &bytes.Buffer{})
	d.teams = ownedTeams("7", "12")

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant: %v (a tenant-actionable outcome exits zero)", err)
	}

	for _, w := range conns.put {
		if w.Status == lineupapi.ConnVerified {
			t.Fatal("an ambiguous connection was written verified")
		}
	}
	if len(conns.claimed) != 0 {
		t.Fatalf("ClaimTeam was called with %v for an ambiguous connection", conns.claimed)
	}
	got := lastPut(t, conns)
	if got.Status != lineupapi.ConnNeedsReconnect || got.LastError != lineupapi.ConnErrAmbiguousTeam {
		t.Fatalf("wrote status %q class %q, want needs_reconnect/ambiguous_team", got.Status, got.LastError)
	}
	if got.TeamID != "" {
		t.Errorf("the record names team %q; nothing was chosen", got.TeamID)
	}
	want := []lineupapi.OwnedTeam{{ID: "7", Name: "Team 7"}, {ID: "12", Name: "Team 12"}}
	if !reflect.DeepEqual(got.OwnedTeams, want) {
		t.Errorf("OwnedTeams = %v, want %v — the choice is checked against this set", got.OwnedTeams, want)
	}
	// The credentials stay: the choice re-runs the check without asking for a
	// password Fantrax has just accepted.
	if len(got.CredsCiphertext) == 0 {
		t.Error("the ambiguous record lost its sealed credentials")
	}
	if len(feed.written) != 1 {
		t.Errorf("feed entries = %d, want the tenant told once", len(feed.written))
	}
}

// A set left by an earlier ambiguous run must not outlive the answer that
// replaced it, on the verified route or on a different failure.
func TestConnectTenant_AStaleOwnedSetIsCleared(t *testing.T) {
	stale := []lineupapi.OwnedTeam{{ID: "7"}, {ID: "12"}}

	t.Run("verified after a choice", func(t *testing.T) {
		conns := teamlessConn(t)
		conns.conn.TeamID = "7" // the choice the API accepted
		conns.conn.OwnedTeams = stale
		d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})
		d.teams = ownedTeams("7", "12")

		if err := connectTenant(context.Background(), "alice", d); err != nil {
			t.Fatalf("connectTenant: %v", err)
		}
		got := lastPut(t, conns)
		if got.Status != lineupapi.ConnVerified || got.TeamID != "7" {
			t.Fatalf("wrote status %q team %q, want verified team 7", got.Status, got.TeamID)
		}
		if len(got.OwnedTeams) != 0 {
			t.Errorf("OwnedTeams = %v after verification, want it cleared", got.OwnedTeams)
		}
	})

	t.Run("the chosen team is no longer owned", func(t *testing.T) {
		conns := teamlessConn(t)
		conns.conn.TeamID = "7"
		conns.conn.OwnedTeams = stale
		d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})
		d.teams = ownedTeams("12")

		if err := connectTenant(context.Background(), "alice", d); err != nil {
			t.Fatalf("connectTenant: %v", err)
		}
		got := lastPut(t, conns)
		if got.LastError != lineupapi.ConnErrTeamNotOwned {
			t.Fatalf("class = %q, want team_not_owned: a choice is PROVEN, not trusted", got.LastError)
		}
		if len(conns.claimed) != 0 {
			t.Errorf("ClaimTeam called with %v for a team Fantrax did not confirm", conns.claimed)
		}
		if len(got.OwnedTeams) != 0 {
			t.Errorf("OwnedTeams = %v on a team_not_owned record, want it cleared", got.OwnedTeams)
		}
	})
}

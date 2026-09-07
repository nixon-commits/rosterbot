package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func expiredProbe(string) error { return errors.New("fantrax API error WARNING_NOT_LOGGED_IN: x") }

func freshMint(lineupapi.FantraxCreds) loginEvidence {
	return loginEvidence{FXRM: "fresh-cookie", Info: &fantraxUserInfo{UserID: "fx-1"}}
}

func badPasswordMint(lineupapi.FantraxCreds) loginEvidence {
	return loginEvidence{
		Matched: map[string]bool{"login_form": true, "form_error": true},
		Texts:   map[string]string{"form_error": "Invalid username or password"},
	}
}

// TestSessionLadder_RefreshContinuesOnTheMintedSessionWhenTheRecordMoved: the
// ladder read the record, re-logged in with the credentials it read, and by
// the time it went to store the sealed session something newer had landed — a
// reconnect, a connect task's verdict, another job's ladder (rosterbot-wm9g).
// The session it minted is still a real Fantrax session for THIS run, so the
// run continues on it; what it must not do is store a cookie minted from the
// old credentials over the record that won, and what it must not do either
// is fail the run over a store race it lost benignly.
func TestSessionLadder_RefreshContinuesOnTheMintedSessionWhenTheRecordMoved(t *testing.T) {
	t.Setenv("FANTRAX_COOKIES", "FX_RM=expired")
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified), putErr: lineupapi.ErrConnectionConflict}
	var log bytes.Buffer
	lad := sessionLadder{conns: conns, sealer: stubSealer{}, out: &log, probe: expiredProbe, mint: freshMint}

	if err := lad.refresh(context.Background(), "alice", tenantCfg()); err != nil {
		t.Fatalf("refresh = %v; a lost race on the STORE must not cost this run the session it just minted", err)
	}
	if len(conns.put) != 0 {
		t.Fatalf("a refused write landed %d record(s)", len(conns.put))
	}
	if v := os.Getenv("FANTRAX_COOKIES"); v != "FX_RM=fresh-cookie" {
		t.Errorf("FANTRAX_COOKIES = %q; this run should continue on the session it minted", v)
	}
	if !strings.Contains(log.String(), "changed while") {
		t.Errorf("log does not name the race:\n%s", log.String())
	}
}

// TestSessionLadder_StopAbstainsAndTellsNobodyWhenTheRecordMoved is the bead's
// headline race with the roles named: a ladder that read verified credentials
// before a reconnect, failed to log in with them, and now holds a verdict —
// "needs_reconnect" — about credentials the tenant has ALREADY replaced.
// Writing it would demote fresh credentials; telling the tenant would send
// them to reconnect over a reconnect they just made. The run still stops,
// because there is no session, and the console says why.
func TestSessionLadder_StopAbstainsAndTellsNobodyWhenTheRecordMoved(t *testing.T) {
	t.Setenv("FANTRAX_COOKIES", "FX_RM=expired")
	t.Setenv("FANTRAX_USERNAME", "tenant@example.test")
	t.Setenv("FANTRAX_PASSWORD", "tenant-pass")

	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified), putErr: lineupapi.ErrConnectionConflict}
	feed := &memFeed{}
	var log bytes.Buffer
	lad := sessionLadder{conns: conns, sealer: stubSealer{}, feed: feed, out: &log, probe: expiredProbe, mint: badPasswordMint}

	err := lad.refresh(context.Background(), "alice", tenantCfg())
	if err == nil {
		t.Fatal("refresh returned nil; there is no session, so the run must still stop")
	}
	if !strings.Contains(err.Error(), "changed while") {
		t.Errorf("returned error %q does not name the race; that string is what opsalert quotes", err)
	}
	if len(conns.put) != 0 {
		t.Fatalf("a refused write landed %d record(s)", len(conns.put))
	}
	if len(feed.written) != 0 {
		t.Fatalf("wrote %d feed entries, want none — the record that won describes newer "+
			"credentials than the ones this verdict is about", len(feed.written))
	}
	for _, v := range []string{"FANTRAX_USERNAME", "FANTRAX_PASSWORD", "FANTRAX_COOKIES"} {
		if s := os.Getenv(v); s != "" {
			t.Errorf("%s = %q after an abstained stop; anything downstream could still try to authenticate", v, s)
		}
	}
}

// TestSessionLadder_StopStillTellsTheTenantWhenTheStoreFails pins the half of
// the old contract the abstain path must NOT take with it: a store that
// merely fails (not a conflict) still tells the tenant the run stopped. The
// feed write moved to AFTER the Put so a conflict could skip it; a plain
// error must not skip it too.
func TestSessionLadder_StopStillTellsTheTenantWhenTheStoreFails(t *testing.T) {
	t.Setenv("FANTRAX_COOKIES", "FX_RM=expired")
	conns := &recordingConns{conn: connFor(t, "alice", lineupapi.ConnVerified), putErr: errors.New("dynamo unavailable")}
	feed := &memFeed{}
	var log bytes.Buffer
	lad := sessionLadder{conns: conns, sealer: stubSealer{}, feed: feed, out: &log, probe: expiredProbe, mint: badPasswordMint}

	err := lad.refresh(context.Background(), "alice", tenantCfg())
	if err == nil || !strings.Contains(err.Error(), "could not be updated") {
		t.Fatalf("refresh = %v, want the store failure named", err)
	}
	if len(feed.written) != 1 {
		t.Fatalf("wrote %d feed entries, want 1 — a store that rejects the update must still tell the person the run stopped", len(feed.written))
	}
}

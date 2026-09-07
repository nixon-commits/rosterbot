package lineupapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestMemConnections_HonoursTheVersionContract pins the fake the handler
// tests below run against. conntest.Run is the real contract but cannot be
// imported from inside this package, so this is the in-package restatement of
// the three refusals the retry tests depend on. A fake that stopped refusing
// would let TestConnect_ReReadsAndRetriesWhenTheRecordMovesUnderIt pass with
// the retry loop deleted.
func TestMemConnections_HonoursTheVersionContract(t *testing.T) {
	ctx := context.Background()
	m := &memConnections{}

	c := &FantraxConnection{UserID: "alice", Status: ConnPending}
	if err := m.PutConnection(ctx, c); err != nil || c.Version == "" {
		t.Fatalf("create: err=%v version=%q", err, c.Version)
	}
	if err := m.PutConnection(ctx, &FantraxConnection{UserID: "alice"}); !errors.Is(err, ErrConnectionConflict) {
		t.Fatalf("create over an existing record = %v, want ErrConnectionConflict", err)
	}
	stale := *c
	c.Status = ConnVerified
	if err := m.PutConnection(ctx, c); err != nil {
		t.Fatalf("update at the current version: %v", err)
	}
	if err := m.PutConnection(ctx, &stale); !errors.Is(err, ErrConnectionConflict) {
		t.Fatalf("update at a stale version = %v, want ErrConnectionConflict", err)
	}

	// A seeded record with no Version is a pre-field row: it reads as
	// ConnUnversioned and takes exactly one write at that version.
	seeded := &memConnections{conn: &FantraxConnection{UserID: "bob", Status: ConnPending}}
	got, _, _ := seeded.GetConnection(ctx, "bob")
	if got.Version != ConnUnversioned {
		t.Fatalf("seeded record reads as %q, want ConnUnversioned", got.Version)
	}
	if err := seeded.PutConnection(ctx, got); err != nil {
		t.Fatalf("migration write: %v", err)
	}
	if err := seeded.PutConnection(ctx, &FantraxConnection{UserID: "bob", Version: ConnUnversioned}); !errors.Is(err, ErrConnectionConflict) {
		t.Fatalf("second write at ConnUnversioned = %v, want ErrConnectionConflict", err)
	}
}

func postConnect(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/connect", connectBody(t, "alice@fantrax", "hunter2"))
	r.Header = reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0).Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestConnect_ReReadsAndRetriesWhenTheRecordMovesUnderIt: the handler's write
// is a wholesale replacement — new credentials, pending — so unlike the ladder
// and the connect task it CAN re-apply after a lost race (rosterbot-wm9g). It
// must re-read rather than retry blind, because the in-flight guard has to be
// re-evaluated against the record that won: a connect task landing "verified"
// in the window is fine to replace, a resubmission landing "pending" is not.
func TestConnect_ReReadsAndRetriesWhenTheRecordMovesUnderIt(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{UserID: "alice", Status: ConnVerified, UpdatedAt: time.Now().Add(-time.Hour)}
	conns.ver = 1
	conns.conn.Version = "1"

	moved := false
	conns.afterGet = func() {
		if moved {
			return
		}
		moved = true
		conns.moveUnderneath() // a session ladder stores a refreshed cookie between the read and the write
	}

	rec := postConnect(t, h)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("connect = %d (%s), want 202 after one lost race", rec.Code, rec.Body.String())
	}
	if !moved {
		t.Fatal("the hook never fired; this test proves nothing without the simulated race")
	}
	if conns.gets != 2 {
		t.Fatalf("read the record %d times, want 2 — the retry must RE-READ so the in-flight "+
			"guard runs against the record that won, not retry the same write blind", conns.gets)
	}
	if conns.conn.Status != ConnPending {
		t.Fatalf("stored status = %q, want pending: the retried write did not land", conns.conn.Status)
	}
	if !jobs.launched {
		t.Fatal("the verification task never launched after the retried write")
	}
}

// TestConnect_ReadFailureIsRefusedRatherThanOverwritten inverts the handler's
// old "a store read failure proceeds" rule. A record that could not be read
// has an unknown version, and the only write the handler could make is one
// asserting no record exists — which the version now refuses over a real
// record anyway. Failing the request is the honest version of that refusal:
// the tenant retries in a moment instead of getting a 502 from a write that
// was never going to be allowed.
func TestConnect_ReadFailureIsRefusedRatherThanOverwritten(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{UserID: "alice", Status: ConnVerified, Version: "1", CredsCiphertext: []byte("sealed:good")}
	conns.ver = 1
	conns.getErr = errors.New("dynamo unavailable")

	rec := postConnect(t, h)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("connect = %d (%s), want 502", rec.Code, rec.Body.String())
	}
	if jobs.launched {
		t.Fatal("launched a verification task over a record it could not read")
	}
	if conns.puts != 0 {
		t.Fatalf("attempted %d write(s) over a record it could not read; the old handler "+
			"proceeded to a write here, and only the fake's version check stood between it "+
			"and a blind overwrite", conns.puts)
	}
	if string(conns.conn.CredsCiphertext) != "sealed:good" {
		t.Fatal("the stored credentials were replaced by a write over an unread record")
	}
}

// TestConnect_GivesUpAfterRepeatedConflicts bounds the retry: a record that
// moves on every read is real contention, and the answer is a 409 the client
// can act on, not a 502 that reads as an outage and not an unbounded loop.
func TestConnect_GivesUpAfterRepeatedConflicts(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.conn = &FantraxConnection{UserID: "alice", Status: ConnVerified, UpdatedAt: time.Now().Add(-time.Hour)}
	conns.ver = 1
	conns.conn.Version = "1"
	conns.afterGet = conns.moveUnderneath

	rec := postConnect(t, h)
	if rec.Code != http.StatusConflict {
		t.Fatalf("connect = %d (%s), want 409 once the bounded retry is exhausted", rec.Code, rec.Body.String())
	}
	if jobs.launched {
		t.Fatal("launched a verification task for credentials that were never stored")
	}
	if conns.gets > connectPutAttempts {
		t.Fatalf("read the record %d times for %d attempts; the loop is not bounded the way it claims", conns.gets, connectPutAttempts)
	}
}

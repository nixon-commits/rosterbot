package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// openerFunc adapts a plain function to lineupapi.Opener, for a case
// stubOpener cannot express: returning fixed bytes regardless of the
// ciphertext, so the decrypt itself succeeds but hands back a malformed
// credential blob.
type openerFunc func(context.Context, lineupapi.UserID, []byte) ([]byte, error)

func (f openerFunc) Open(ctx context.Context, uid lineupapi.UserID, ct []byte) ([]byte, error) {
	return f(ctx, uid, ct)
}

// TestConnectTenant_NoPreProofFailureIsLeftPending is rosterbot-spb9's own
// screen, the mirror of TestConnectTenant_NoPostProofFailureIsLeftPending one
// step earlier: every failure BEFORE Fantrax is ever asked anything must still
// leave a durable record, or the tenant's settings page shows "Checking your
// credentials…" with nothing that will ever resolve it — exactly the defect
// rosterbot-ch0s closed for the failures AFTER a proven login.
func TestConnectTenant_NoPreProofFailureIsLeftPending(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*connectDeps, *postAuthConns)
	}{
		{
			name: "the connection record could not be read",
			apply: func(_ *connectDeps, c *postAuthConns) {
				c.getErr = errors.New("dynamodb: throughput exceeded")
			},
		},
		{
			name: "the opener fails to decrypt the stored credentials",
			apply: func(d *connectDeps, _ *postAuthConns) {
				d.opener = stubOpener{err: errors.New("kms: access denied")}
			},
		},
		{
			name: "the decrypted credential blob is malformed",
			apply: func(d *connectDeps, _ *postAuthConns) {
				d.opener = openerFunc(func(context.Context, lineupapi.UserID, []byte) ([]byte, error) {
					return []byte("not json"), nil
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
			d := postAuthDeps(t, conns, feed, out)
			tc.apply(&d, conns)

			err := connectTenant(context.Background(), "alice", d)

			// t.Error, not t.Fatal, so a mutation that changes the route is
			// reported against every assertion rather than stopping at the
			// first — the same reasoning as the post-proof sibling test.
			if err == nil {
				t.Error("returned nil; a fault on our own side must still page the operator")
			}
			if len(conns.put) == 0 {
				t.Fatal("wrote no connection record: the tenant's page shows " +
					"\"Checking your credentials…\" with nothing that will ever resolve it")
			}
			got := conns.put[len(conns.put)-1]
			if got.Status == lineupapi.ConnPending {
				t.Errorf("left the record at %q", got.Status)
			}
			if got.Status != lineupapi.ConnCheckFailed {
				t.Errorf("Status = %q, want %q — check_failed, never interrupted, since "+
					"nothing was ever submitted to Fantrax here", got.Status, lineupapi.ConnCheckFailed)
			}
			if got.LastError != lineupapi.ConnErrCheckFailed {
				t.Errorf("LastError = %q, want %q", got.LastError, lineupapi.ConnErrCheckFailed)
			}
			if len(feed.written) == 0 {
				t.Error("told the tenant nothing about their own unconfirmed connection")
			}
		})
	}
}

// TestFailCheck_WritesABareRecordWhenNoneCouldBeRead pins failCheck's other
// half, used by runConnect's own construction failures (NewOpener, NewSealer):
// when the caller never had a connection record to begin with (existing ==
// nil — the read that would have produced one IS the fault), it still writes
// SOMETHING, naming only the tenant and the new status, rather than writing
// nothing because there was nothing to merge onto.
func TestFailCheck_WritesABareRecordWhenNoneCouldBeRead(t *testing.T) {
	conns, feed, out := &postAuthConns{}, &memFeed{}, &bytes.Buffer{}
	d := postAuthDeps(t, conns, feed, out)

	err := failCheck(context.Background(), d, "alice", nil,
		"constructing the credential opener", errors.New("kms: no such key"))

	if err == nil {
		t.Fatal("returned nil for a fault only the operator can fix")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.UserID != "alice" {
		t.Errorf("UserID = %q, want %q — a bare record still has to name whose it is",
			got.UserID, "alice")
	}
	if got.Status != lineupapi.ConnCheckFailed {
		t.Errorf("Status = %q, want %q", got.Status, lineupapi.ConnCheckFailed)
	}
	if len(feed.written) == 0 {
		t.Error("told the tenant nothing")
	}
}

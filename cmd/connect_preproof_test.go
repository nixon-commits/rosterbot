package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
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
		// wantRecord is false only for the one path that cannot write safely:
		// a record that could not be READ must not be overwritten blind (the
		// write would replace the tenant's sealed credentials with an empty
		// record for a DynamoDB blip), so that path tells the two people and
		// leaves the record alone.
		wantRecord bool
	}{
		{
			name: "the connection record could not be read",
			apply: func(_ *connectDeps, c *postAuthConns) {
				c.getErr = errors.New("dynamodb: throughput exceeded")
			},
			wantRecord: false,
		},
		{
			name: "the opener fails to decrypt the stored credentials",
			apply: func(d *connectDeps, _ *postAuthConns) {
				d.opener = stubOpener{err: errors.New("kms: access denied")}
			},
			wantRecord: true,
		},
		{
			name: "the decrypted credential blob is malformed",
			apply: func(d *connectDeps, _ *postAuthConns) {
				d.opener = openerFunc(func(context.Context, lineupapi.UserID, []byte) ([]byte, error) {
					return []byte("not json"), nil
				})
			},
			wantRecord: true,
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
			if len(feed.written) == 0 {
				t.Error("told the tenant nothing about their own unconfirmed connection")
			}
			if !tc.wantRecord {
				if len(conns.put) != 0 {
					t.Fatalf("wrote %d connection record(s) over a record it could not read back: "+
						"a blind write replaces the tenant's sealed credentials with an empty "+
						"record for what may be a DynamoDB blip", len(conns.put))
				}
				if !strings.Contains(out.String(), "could not be read back") {
					t.Errorf("console output does not say the record could not be read back:\n%s", out.String())
				}
				return
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
			if len(got.CredsCiphertext) == 0 {
				t.Errorf("the record written back lost the tenant's sealed credentials: %+v", got)
			}
		})
	}
}

// TestFailCheck_WritesABareRecordWhenNoneExists pins failCheck's fallback for
// a tenant with no connection record at all (GetConnection answers not-found):
// there is nothing to lose, so it still writes SOMETHING, naming only the
// tenant and the new status, rather than writing nothing because there was
// nothing to merge onto.
func TestFailCheck_WritesABareRecordWhenNoneExists(t *testing.T) {
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

// TestFailCheck_WritesOntoTheRecordItReadsBack is the common runConnect
// construction-failure case (NewOpener, NewSealer): the caller holds no record,
// but one exists, so failCheck reads it back and writes the status onto it —
// the tenant's sealed credentials survive, and the next resubmission does not
// have to start from nothing.
//
// MUTATION: skip the read and write a bare record whenever existing is nil —
// the ciphertext assertion below fails.
func TestFailCheck_WritesOntoTheRecordItReadsBack(t *testing.T) {
	conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
	conns.conn.CredsCiphertext = []byte("sealed:original")
	d := postAuthDeps(t, conns, feed, out)

	err := failCheck(context.Background(), d, "alice", nil,
		"constructing the credential sealer", errors.New("kms: no such key"))

	if err == nil {
		t.Fatal("returned nil for a fault only the operator can fix")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.Status != lineupapi.ConnCheckFailed {
		t.Errorf("Status = %q, want %q", got.Status, lineupapi.ConnCheckFailed)
	}
	if string(got.CredsCiphertext) != "sealed:original" {
		t.Errorf("CredsCiphertext = %q, want the record's own sealed credentials preserved",
			got.CredsCiphertext)
	}
	if len(feed.written) == 0 {
		t.Error("told the tenant nothing")
	}
}

// TestFailCheck_DoesNotOverwriteARecordItCannotRead is the one path where
// "every route writes" stops at the two people: when the record cannot be read
// back, a durable write would be a blind overwrite of the tenant's sealed
// credentials for what may be a transient DynamoDB fault. The tenant is still
// told (feed), the operator still paged (non-zero exit), and nothing is put.
func TestFailCheck_DoesNotOverwriteARecordItCannotRead(t *testing.T) {
	conns, feed, out := &postAuthConns{getErr: errors.New("dynamodb: throughput exceeded")},
		&memFeed{}, &bytes.Buffer{}
	d := postAuthDeps(t, conns, feed, out)

	err := failCheck(context.Background(), d, "alice", nil,
		"constructing the credential opener", errors.New("kms: no such key"))

	if err == nil {
		t.Fatal("returned nil for a fault only the operator can fix")
	}
	if len(conns.put) != 0 {
		t.Fatalf("wrote %d connection record(s) over a record it could not read back", len(conns.put))
	}
	if len(feed.written) == 0 {
		t.Error("told the tenant nothing")
	}
	if !strings.Contains(out.String(), "could not be read back") {
		t.Errorf("console output does not name the unreadable record:\n%s", out.String())
	}
}

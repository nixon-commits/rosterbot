package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestConnectTenant_VerdictIsNotRecordedOverARecordThatMoved: the connect task
// read a pending record, proved the credentials, and found on writing
// "verified" that the record had moved — a resubmission past the in-flight
// window, or a deletion (rosterbot-wm9g). The verdict is about the
// credentials THIS run read; the newer record's own connect run owns its
// outcome. Nothing durable is written, nobody is paged, and the console says
// why the run's outcome is not on the record.
func TestConnectTenant_VerdictIsNotRecordedOverARecordThatMoved(t *testing.T) {
	conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
	conns.putErr = lineupapi.ErrConnectionConflict
	d := postAuthDeps(t, conns, feed, out)

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant = %v; a record that moved is owned by the newer writer, and "+
			"nothing here is the operator's to fix", err)
	}
	if len(conns.put) != 0 {
		t.Fatalf("a refused write landed %d record(s)", len(conns.put))
	}
	if !strings.Contains(out.String(), "changed while") {
		t.Errorf("console does not name the race:\n%s", out.String())
	}
}

// TestConnectTenant_TenantFaultStillExitsZeroWhenTheRecordMoved: the exit-0
// route keeps its exit code across an abstained write — the tenant-actionable
// classification was made, the feed entry already went out (fail() tells
// before it writes, deliberately), and the record that won will be judged by
// its own run.
func TestConnectTenant_TenantFaultStillExitsZeroWhenTheRecordMoved(t *testing.T) {
	conns, feed, out := pendingConn(t), &memFeed{}, &bytes.Buffer{}
	conns.putErr = lineupapi.ErrConnectionConflict
	d := postAuthDeps(t, conns, feed, out)
	d.login = badPasswordMint

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant = %v, want nil on the tenant-actionable route", err)
	}
	if len(conns.put) != 0 {
		t.Fatalf("a refused write landed %d record(s)", len(conns.put))
	}
	if !strings.Contains(out.String(), "changed while") {
		t.Errorf("console does not name the race:\n%s", out.String())
	}
}

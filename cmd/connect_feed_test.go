package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

// errNotWritable is a sentinel used by the tests for a feed that rejects writes.
var errNotWritable = errors.New("feed not writable")

type memFeed struct {
	written []lineupapi.Notification
	err     error
}

func (m *memFeed) PutNotification(_ context.Context, n lineupapi.Notification) error {
	if m.err != nil {
		return m.err
	}
	m.written = append(m.written, n)
	return nil
}

// fakeDeliverSink records every (event, feedID) pair it is asked to deliver,
// standing in for notify.Default's real sinks (APNs, the cutover-window
// Pushover dual-send) without any network.
type fakeDeliverSink struct {
	events []notify.Event
	ids    []string
}

func (s *fakeDeliverSink) Name() string { return "fake" }
func (s *fakeDeliverSink) Deliver(_ context.Context, e notify.Event, feedID string) error {
	s.events = append(s.events, e)
	s.ids = append(s.ids, feedID)
	return nil
}

// withFakeDispatcher installs d as notify.Default for the duration of the
// test, restoring whatever was there before on cleanup — mirrors
// internal/notify's own TestSendWithNoDispatcherConfiguredIsANoOp pattern.
func withFakeDispatcher(t *testing.T, d *notify.Dispatcher) {
	t.Helper()
	old := notify.Default
	t.Cleanup(func() { notify.Default = old })
	notify.Default = d
}

// TestConnectFeed_TenantActionableReachesTheirFeed is rosterbot-crq.14's last
// structural gap.
//
// The design asks for tenant-actionable failures to reach "that user's in-app
// feed". They could not: the ONLY writer of a Notification was notify.Recorder,
// which fires INSIDE SendPushover, so nothing could reach a tenant's feed
// without also reaching the operator's phone — and PUSHOVER_USER_KEY is a
// single deployment-wide secret. The feed was a mirror of the operator's
// alerts, not a per-tenant channel.
//
// The connect task already runs with ROSTERBOT_USER_ID set to the tenant
// (handleConnect passes the caller to the launcher), so the store it composes
// is already theirs. What was missing is a writer that does not go through
// Pushover.
func TestConnectFeed_TenantActionableReachesTheirFeed(t *testing.T) {
	feed := &memFeed{}
	var pushes []string

	recordConnectFailure(context.Background(), feed,
		func(msg string) { pushes = append(pushes, msg) },
		"alice", lineupapi.ConnErrBadCredentials)

	if len(feed.written) != 1 {
		t.Fatalf("wrote %d feed entries, want 1", len(feed.written))
	}
	if len(pushes) != 0 {
		t.Errorf("a tenant-actionable failure pushed to the operator: %v — the whole "+
			"point is that they do not become the help desk for it", pushes)
	}
	n := feed.written[0]
	if n.Status != "failure" {
		t.Errorf("Status = %q, want failure", n.Status)
	}
	if n.Message == lineupapi.ConnErrBadCredentials {
		t.Error("the feed entry is the raw error class; a user needs an answer, not a status")
	}
}

// TestConnectFeed_OperatorActionableGoesToTheOperator is the other half of the
// taxonomy. A Cloudflare block is not the tenant's to fix, so telling them
// would waste their time — and NOT telling the operator would leave the one
// person who can act unaware.
func TestConnectFeed_OperatorActionableGoesToTheOperator(t *testing.T) {
	feed := &memFeed{}
	var pushes []string

	recordConnectFailure(context.Background(), feed,
		func(msg string) { pushes = append(pushes, msg) },
		"alice", lineupapi.ConnErrBotChallenge)

	if len(pushes) != 1 {
		t.Fatalf("pushed %d times, want 1 — only the operator can act on a bot challenge", len(pushes))
	}
	if len(feed.written) != 0 {
		t.Errorf("told the tenant about a failure they cannot act on: %+v", feed.written)
	}
}

// TestConnectFeed_FailuresAreSoft. This runs inside the connect task's own
// failure path, which already records the class on the connection record and
// exits 0 deliberately. A feed hiccup must not turn a recorded, diagnosable
// outcome into a crashed task.
func TestConnectFeed_FailuresAreSoft(t *testing.T) {
	feed := &memFeed{err: errNotWritable}
	recordConnectFailure(context.Background(), feed, func(string) {},
		"alice", lineupapi.ConnErrBadCredentials)
	// Reaching here without panicking is the assertion.
}

// TestConnectFeed_EveryClassIsRoutedSomewhere. A class added later must not
// silently reach nobody: the failure would be invisible to the tenant AND the
// operator, which is worse than telling the wrong one.
func TestConnectFeed_EveryClassIsRoutedSomewhere(t *testing.T) {
	for _, class := range []string{
		lineupapi.ConnErrBadCredentials,
		lineupapi.ConnErrTwoFactor,
		lineupapi.ConnErrBotChallenge,
		lineupapi.ConnErrLoginChallengeOrTimeout,
		lineupapi.ConnErrTeamNotOwned,
		lineupapi.ConnErrNoTeam,
		lineupapi.ConnErrTeamClaimed,
		lineupapi.ConnErrVerificationInterrupted,
		lineupapi.ConnErrCheckFailed,
	} {
		feed := &memFeed{}
		pushed := 0
		recordConnectFailure(context.Background(), feed, func(string) { pushed++ },
			"alice", class)
		if len(feed.written) == 0 && pushed == 0 {
			t.Errorf("class %q reaches nobody — invisible to the tenant and the operator", class)
		}
	}
}

// TestConnectFeed_TenantActionablePushesThroughDispatcherSinks is
// rosterbot-3has: the feed write alone reaches the in-app feed only; nothing
// pushed it. Once notify.Default has sinks, a tenant-actionable connect
// failure must fan out through them under the SAME id the feed write just
// produced — reusing it rather than going through notify.Send/Deliver
// minting a second record.
func TestConnectFeed_TenantActionablePushesThroughDispatcherSinks(t *testing.T) {
	feed := &memFeed{}
	sink := &fakeDeliverSink{}
	withFakeDispatcher(t, &notify.Dispatcher{Sinks: []notify.Sink{sink}})

	recordConnectFailure(context.Background(), feed, func(string) {},
		"alice", lineupapi.ConnErrBadCredentials)

	if len(feed.written) != 1 {
		t.Fatalf("wrote %d feed entries, want 1", len(feed.written))
	}
	if len(sink.ids) != 1 {
		t.Fatalf("delivered %d times, want 1", len(sink.ids))
	}
	if sink.ids[0] != feed.written[0].ID {
		t.Errorf("delivered under feed id %q, want the id PutNotification stored (%q)",
			sink.ids[0], feed.written[0].ID)
	}
}

// TestConnectFeed_NoDispatcherConfiguredStillWritesFeedAndDoesNotPanic covers
// the local-dev/no-dispatcher path: notify.Default is nil (never installed,
// or the standalone connect task before this bead). The feed write is the
// durable record either way, and calling into a nil dispatcher must not
// panic.
func TestConnectFeed_NoDispatcherConfiguredStillWritesFeedAndDoesNotPanic(t *testing.T) {
	feed := &memFeed{}
	withFakeDispatcher(t, nil)

	recordConnectFailure(context.Background(), feed, func(string) {},
		"alice", lineupapi.ConnErrBadCredentials)

	if len(feed.written) != 1 {
		t.Fatalf("wrote %d feed entries, want 1", len(feed.written))
	}
}

// TestConnectFeed_FeedWriteFailureNeverPushes is the absence half: a record
// that never landed durably must not be pushed, because its id would open
// nothing on tap.
func TestConnectFeed_FeedWriteFailureNeverPushes(t *testing.T) {
	feed := &memFeed{err: errNotWritable}
	sink := &fakeDeliverSink{}
	withFakeDispatcher(t, &notify.Dispatcher{Sinks: []notify.Sink{sink}})

	recordConnectFailure(context.Background(), feed, func(string) {},
		"alice", lineupapi.ConnErrBadCredentials)

	if len(sink.ids) != 0 {
		t.Errorf("delivered %d times after a failed feed write, want 0 — the id would open nothing",
			len(sink.ids))
	}
}

package notify

import (
	"context"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Event is one thing worth telling a manager about: a lineup applied, waiver
// claims filed, a trade graded.
//
// Kind is stated by the call site rather than inferred from Title. The old
// path guessed it with lineupapi.KindFromTitle's substring matching, which
// only worked because the titles happened not to collide; KindFromTitle
// survives for reading historical records, which were written without one.
type Event struct {
	Kind    string // lineup|waivers|claims|transactions|prospects|gs-check|alert
	Title   string
	Message string
	Changes []lineupapi.Change // optional; lineup only
}

// FeedSink writes the durable activity-feed record and returns its id.
//
// It is separate from Sink and runs first because its id is an INPUT to every
// delivery: the push payload carries it so tapping the notification can open
// that item. A feed write that happened afterwards could not supply it.
type FeedSink interface {
	Write(ctx context.Context, e Event) (id string, err error)
}

// Sink is one best-effort delivery channel.
type Sink interface {
	Name() string
	Deliver(ctx context.Context, e Event, feedID string) error
}

// Dispatcher fans an event out: durable record first, then delivery.
type Dispatcher struct {
	Feed  FeedSink
	Sinks []Sink
}

// Send writes the feed record and then delivers to every sink.
//
// It returns an error ONLY when the feed write fails, which is the single
// durable obligation. A sink's failure is logged and swallowed, matching the
// best-effort contract the old Recorder hook documented — a manager's lineup
// run must not fail because Apple had a bad minute.
func (d *Dispatcher) Send(ctx context.Context, e Event) error {
	if d == nil || d.Feed == nil {
		return nil
	}
	id, err := d.Feed.Write(ctx, e)
	if err != nil {
		return fmt.Errorf("notify: write feed record: %w", err)
	}
	for _, s := range d.Sinks {
		if err := s.Deliver(ctx, e, id); err != nil {
			fmt.Fprintf(os.Stderr, "warning: notify sink %s: %v\n", s.Name(), err)
		}
	}
	return nil
}

// Default is the process-wide dispatcher, set once by cmd.initShared. It
// mirrors the existing cache.Notify/OutputRecorder package globals rather
// than threading a dispatcher through every call chain — the recipient is the
// ambient tenant (ROSTERBOT_USER_ID), which the per-tenant job fan-out
// already sets per task. Nil outside a configured deployment (local runs,
// tests), where Send is a no-op.
var Default *Dispatcher

// Send emits an event through the process-wide dispatcher.
func Send(ctx context.Context, e Event) error { return Default.Send(ctx, e) }

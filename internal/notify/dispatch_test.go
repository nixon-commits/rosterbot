package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/notify"
)

type fakeFeed struct {
	written []notify.Event
	id      string
	err     error
}

func (f *fakeFeed) Write(_ context.Context, e notify.Event) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.written = append(f.written, e)
	return f.id, nil
}

type fakeSink struct {
	name   string
	gotID  string
	gotEvt notify.Event
	calls  int
	err    error
}

func (s *fakeSink) Name() string { return s.name }
func (s *fakeSink) Deliver(_ context.Context, e notify.Event, feedID string) error {
	s.calls++
	s.gotEvt, s.gotID = e, feedID
	return s.err
}

func TestFeedRecordSurvivesADeliveryFailure(t *testing.T) {
	// THE central regression. Before this design the feed write happened
	// INSIDE SendPushover, so it was a side effect of a delivery attempt. If
	// that ordering ever comes back, events vanish from the activity feed
	// whenever APNs has a bad day — and nothing reports it, because the
	// symptom is a record nobody knows to look for.
	feed := &fakeFeed{id: "notif-1"}
	dead := &fakeSink{name: "apns", err: errors.New("apns exploded")}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{dead}}

	if err := d.Send(context.Background(), notify.Event{Kind: "lineup", Title: "t", Message: "m"}); err != nil {
		t.Fatalf("a delivery failure must not fail Send: %v", err)
	}
	if len(feed.written) != 1 {
		t.Fatalf("feed record must be written regardless of delivery, got %d", len(feed.written))
	}
}

func TestSendFailsOnlyWhenTheFeedWriteFails(t *testing.T) {
	feed := &fakeFeed{err: errors.New("s3 down")}
	sink := &fakeSink{name: "apns"}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{sink}}

	if err := d.Send(context.Background(), notify.Event{Kind: "lineup"}); err == nil {
		t.Fatal("a failed feed write is the one durable obligation and must be returned")
	}
	if sink.calls != 0 {
		t.Error("no sink may be called once the feed write has failed: the payload has no id to carry")
	}
}

func TestEverySinkReceivesTheFeedID(t *testing.T) {
	feed := &fakeFeed{id: "notif-42"}
	a := &fakeSink{name: "apns"}
	b := &fakeSink{name: "pushover"}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{a, b}}

	evt := notify.Event{Kind: "waivers", Title: "Waivers", Message: "2 claims"}
	if err := d.Send(context.Background(), evt); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, s := range []*fakeSink{a, b} {
		if s.calls != 1 {
			t.Errorf("%s called %d times, want 1", s.name, s.calls)
		}
		if s.gotID != "notif-42" {
			t.Errorf("%s got feed id %q, want notif-42", s.name, s.gotID)
		}
		if s.gotEvt.Kind != "waivers" {
			t.Errorf("%s got kind %q", s.name, s.gotEvt.Kind)
		}
	}
}

func TestOneSinkFailureDoesNotSuppressTheOthers(t *testing.T) {
	feed := &fakeFeed{id: "n"}
	broken := &fakeSink{name: "apns", err: errors.New("nope")}
	healthy := &fakeSink{name: "pushover"}
	d := &notify.Dispatcher{Feed: feed, Sinks: []notify.Sink{broken, healthy}}

	if err := d.Send(context.Background(), notify.Event{Kind: "lineup"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if healthy.calls != 1 {
		t.Error("a sink listed after a failing one must still be delivered to")
	}
}

func TestSendWithNoDispatcherConfiguredIsANoOp(t *testing.T) {
	// Local runs and tests have no feed configured. Emitting an event must
	// not panic there; it simply goes nowhere.
	old := notify.Default
	t.Cleanup(func() { notify.Default = old })
	notify.Default = nil
	if err := notify.Send(context.Background(), notify.Event{Kind: "lineup"}); err != nil {
		t.Fatalf("want a silent no-op, got %v", err)
	}
}

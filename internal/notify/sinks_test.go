package notify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/apns"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

type stubPusher struct {
	sent []lineupapi.PushDevice
	got  []apns.Payload
	err  error
}

func (s *stubPusher) Push(_ context.Context, d lineupapi.PushDevice, p apns.Payload) error {
	s.sent = append(s.sent, d)
	s.got = append(s.got, p)
	return s.err
}

type memDevices struct {
	byUser  map[lineupapi.UserID][]lineupapi.PushDevice
	deleted []string
}

func (m *memDevices) PutPushDevice(context.Context, lineupapi.UserID, lineupapi.PushDevice) (lineupapi.PushDevice, error) {
	panic("not used by the sink")
}
func (m *memDevices) PushDevices(_ context.Context, uid lineupapi.UserID) ([]lineupapi.PushDevice, error) {
	return m.byUser[uid], nil
}
func (m *memDevices) DeletePushDevice(_ context.Context, _ lineupapi.UserID, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

func TestAPNsSinkFansOutToEveryDeviceOfTheTenant(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "d1", Token: "a"}, {ID: "d2", Token: "b"}},
	}}
	pusher := &stubPusher{}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	if err := sink.Deliver(context.Background(), notify.Event{Kind: "lineup", Title: "T", Message: "M"}, "notif-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(pusher.sent) != 2 {
		t.Fatalf("want a push per device, got %d", len(pusher.sent))
	}
	for _, p := range pusher.got {
		if p.NotificationID != "notif-1" {
			t.Errorf("payload must carry the feed id, got %q", p.NotificationID)
		}
	}
}

func TestAPNsSinkPrunesGoneDevices(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "dead", Token: "a"}},
	}}
	pusher := &stubPusher{err: apns.ErrDeviceGone}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	_ = sink.Deliver(context.Background(), notify.Event{Kind: "lineup"}, "n")

	if len(devices.deleted) != 1 || devices.deleted[0] != "dead" {
		t.Fatalf("a device APNs reports gone must be deleted, deleted=%v", devices.deleted)
	}
}

func TestAPNsSinkKeepsDevicesOnTransientErrors(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "live", Token: "a"}},
	}}
	pusher := &stubPusher{err: errors.New("503 service unavailable")}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	_ = sink.Deliver(context.Background(), notify.Event{Kind: "lineup"}, "n")

	if len(devices.deleted) != 0 {
		t.Fatalf("a transient failure must never delete a device, deleted=%v", devices.deleted)
	}
}

func TestAPNsSinkWithNoDevicesIsASilentSuccess(t *testing.T) {
	sink := notify.NewAPNsSink(&stubPusher{}, &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{}}, "u1")
	if err := sink.Deliver(context.Background(), notify.Event{Kind: "lineup"}, "n"); err != nil {
		t.Fatalf("a tenant with no registered devices is normal, not an error: %v", err)
	}
}

func TestAPNsSinkTruncatesTheBodyToASummary(t *testing.T) {
	devices := &memDevices{byUser: map[lineupapi.UserID][]lineupapi.PushDevice{
		"u1": {{ID: "d1", Token: "a"}},
	}}
	pusher := &stubPusher{}
	sink := notify.NewAPNsSink(pusher, devices, "u1")

	long := strings.Repeat("é", 500) // multibyte on purpose: truncation must count runes
	_ = sink.Deliver(context.Background(), notify.Event{Kind: "lineup", Message: long}, "n")

	if len(pusher.got) != 1 {
		t.Fatalf("want 1 push, got %d", len(pusher.got))
	}
	body := []rune(pusher.got[0].Body)
	if len(body) > 178 {
		t.Errorf("body must be summarized to at most 178 runes, got %d", len(body))
	}
	if body[len(body)-1] != '…' {
		t.Errorf("a truncated body must end in an ellipsis, ends %q", string(body[len(body)-1]))
	}
}

// --- feed sink ---------------------------------------------------------------

type memNotifWriter struct {
	put []lineupapi.Notification
}

func (m *memNotifWriter) PutNotification(_ context.Context, n lineupapi.Notification) error {
	m.put = append(m.put, n)
	return nil
}

func TestFeedWriterSinkStampsKindUserAndRun(t *testing.T) {
	w := &memNotifWriter{}
	f := &notify.FeedWriterSink{Writer: w, UserID: "u1", RunID: "run-9"}

	id, err := f.Write(context.Background(), notify.Event{
		Kind: "waivers", Title: "Waivers", Message: "2 adds",
		Changes: []lineupapi.Change{{Action: "activate", Player: "X"}},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id == "" {
		t.Fatal("Write must return the record id; the push payload carries it")
	}
	if len(w.put) != 1 {
		t.Fatalf("want 1 record, got %d", len(w.put))
	}
	n := w.put[0]
	if n.ID != id {
		t.Errorf("returned id %q is not the stored id %q", id, n.ID)
	}
	if n.Kind != "waivers" || n.UserID != "u1" || n.RunID != "run-9" {
		t.Errorf("record = %+v", n)
	}
	if len(n.Changes) != 1 {
		t.Errorf("changes must ride along, got %+v", n.Changes)
	}
	if n.Status == "" || n.CreatedAt == "" {
		t.Errorf("status and created_at must be stamped, got %+v", n)
	}
}

// --- pushover sink -----------------------------------------------------------

func TestPushoverSinkName(t *testing.T) {
	// Deliver hits the real Pushover API, so only the shape is asserted here;
	// the dual-send path is exercised end to end during cutover (plan Task 8).
	var s notify.Sink = &notify.PushoverSink{UserKey: "u", APIToken: "t"}
	if s.Name() != "pushover" {
		t.Errorf("Name() = %q", s.Name())
	}
}

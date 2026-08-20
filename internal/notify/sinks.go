package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/apns"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// --- Feed sink ---------------------------------------------------------------

// FeedWriterSink persists the durable activity-feed record. It replaces the
// old Recorder hook, which fired inside SendPushover — see Dispatcher for why
// the ordering had to invert.
type FeedWriterSink struct {
	Writer lineupapi.NotificationWriter
	UserID string // the ambient tenant; empty on a single-tenant deployment
	RunID  string
	Now    func() time.Time // nil means time.Now
}

func (f *FeedWriterSink) Write(ctx context.Context, e Event) (string, error) {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	t := now().UTC()
	id := fmt.Sprintf("%d", t.UnixNano())

	return id, f.Writer.PutNotification(ctx, lineupapi.Notification{
		ID:        id,
		Kind:      e.Kind,
		Status:    lineupapi.ClassifyStatus(e.Kind, e.Title, e.Message),
		Title:     e.Title,
		Message:   e.Message,
		CreatedAt: t.Format(time.RFC3339),
		RunID:     f.RunID,
		UserID:    f.UserID,
		Changes:   e.Changes,
	})
}

// --- APNs sink ---------------------------------------------------------------

// Pusher is the slice of *apns.Client this sink needs, named here so tests
// can substitute it without an HTTP server.
type Pusher interface {
	Push(ctx context.Context, d lineupapi.PushDevice, p apns.Payload) error
}

// APNsSink fans one event out to every registered device of one tenant. The
// tenant is fixed at construction from the ambient ROSTERBOT_USER_ID — the
// per-tenant job fan-out already runs one task per tenant, so a process only
// ever notifies on its own tenant's behalf.
type APNsSink struct {
	pusher  Pusher
	devices lineupapi.PushDeviceStore
	uid     lineupapi.UserID
}

func NewAPNsSink(p Pusher, devices lineupapi.PushDeviceStore, uid lineupapi.UserID) *APNsSink {
	return &APNsSink{pusher: p, devices: devices, uid: uid}
}

func (a *APNsSink) Name() string { return "apns" }

func (a *APNsSink) Deliver(ctx context.Context, e Event, feedID string) error {
	devices, err := a.devices.PushDevices(ctx, a.uid)
	if err != nil {
		return fmt.Errorf("apns sink: list devices: %w", err)
	}
	// A tenant with no registered devices is the normal state before anyone
	// installs the app. Not an error.
	for _, d := range devices {
		err := a.pusher.Push(ctx, d, apns.Payload{
			Title:          e.Title,
			Body:           summarize(e.Message),
			NotificationID: feedID,
		})
		switch {
		case err == nil:
		case errors.Is(err, apns.ErrDeviceGone):
			// Only this sentinel prunes. A transient failure must never
			// delete a live registration — see apns.ErrDeviceGone.
			if derr := a.devices.DeletePushDevice(ctx, a.uid, d.ID); derr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not prune dead device %s: %v\n", d.ID, derr)
			}
		default:
			fmt.Fprintf(os.Stderr, "warning: apns push to %s: %v\n", d.ID, err)
		}
	}
	return nil
}

// maxBodyRunes bounds the notification body. The payload carries a SUMMARY —
// the full message lives in the feed record the notification opens (D4: the
// existing messages are long, HTML-marked Pushover bodies, and reproducing
// them in a 4KB APNs payload would leave the same content living in two
// formats that drift) — and a lock screen shows roughly four lines anyway.
const maxBodyRunes = 178

func summarize(message string) string {
	r := []rune(message)
	if len(r) <= maxBodyRunes {
		return message
	}
	return string(r[:maxBodyRunes-1]) + "…"
}

// --- Pushover sink -----------------------------------------------------------

// PushoverSink carries fantasy events during the cutover window only. It is
// installed when PUSHOVER_FANTASY_DUAL_SEND is set; removing that variable
// completes the migration with no deploy. Deliberately NOT keyed off
// PUSHOVER_USER_KEY's presence: the operator channel owns that variable
// permanently, and tying the cutover to it would silence operator alerts as
// a side effect of finishing an unrelated migration.
type PushoverSink struct {
	UserKey, APIToken string
}

func (p *PushoverSink) Name() string { return "pushover" }

func (p *PushoverSink) Deliver(_ context.Context, e Event, _ string) error {
	return SendPushover(p.UserKey, p.APIToken, e.Title, e.Message)
}

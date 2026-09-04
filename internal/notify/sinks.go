package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
}

func (f *FeedWriterSink) Write(ctx context.Context, e Event) (string, error) {
	t := time.Now().UTC()
	id := lineupapi.NewNotificationID(t)

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
//
// Every tenant's dual-send traffic lands on this ONE UserKey/APIToken pair —
// the deployment's PUSHOVER_USER_KEY, i.e. the operator's own phone
// (rosterbot-b1oh). TenantLabel is the minimum fix the bead accepted in lieu
// of per-user Pushover keys: it prefixes the title with the tenant's display
// name so a tester's lineup push reads as theirs rather than as the
// operator's own team. Empty (the single-tenant/local-dev case, and the
// operator's own tenant — see resolveTenantLabel in cmd/notifications.go)
// leaves the title exactly as it was before this field existed.
type PushoverSink struct {
	UserKey, APIToken string
	TenantLabel       string
}

func (p *PushoverSink) Name() string { return "pushover" }

func (p *PushoverSink) Deliver(_ context.Context, e Event, _ string) error {
	return sendPushover(p.UserKey, p.APIToken, tagTitle(p.TenantLabel, e.Title), e.Message)
}

// sendPushover is the seam Deliver calls through. Production leaves it at
// SendPushover; tests override it to capture what a delivery would send
// without reaching the real Pushover API — the tag has to be proven applied
// at the point Deliver actually uses it, not merely in tagTitle's own table
// test, since a helper that is correct in isolation and never wired in would
// pass TestTagTitle while leaving every real push untagged.
var sendPushover = SendPushover

// tagTitle prefixes title with label, trimming whitespace and any brackets
// the caller's label already carries so a display name like "[Jon]" cannot
// double up into "[[Jon]] ...". An empty (post-trim) label leaves title
// untouched — the whole point for the single-tenant/local-dev and
// operator-tenant cases, where a self-tag would be pure noise.
func tagTitle(label, title string) string {
	label = strings.Trim(strings.TrimSpace(label), "[]")
	label = strings.TrimSpace(label)
	if label == "" {
		return title
	}
	return "[" + label + "] " + title
}

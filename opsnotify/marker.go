package main

import (
	"context"
	"log"
)

// markerPrefix is where one-shot alert markers live under STATE_BUCKET.
//
// Its own prefix, not notifications/ — that one is the user-facing activity feed
// the dashboard serves on GET /v1/notifications, and salting it with dedup
// bookkeeping would put internal state in front of a reader. The CDK expires
// this prefix on a lifecycle rule, so it does not grow without bound.
const markerPrefix = "opsalert/"

// markerStore records which alerts have already been sent, so a second delivery
// of the same event stays quiet.
//
// The ordering is deliberately check → send → mark, not claim → send. A claim
// taken before sending has to be released when the send fails, and a failed
// release leaves a permanent suppression — trading the bug this fixes (a
// duplicate alert) for a strictly worse one (a missing alert). Marking only
// after a confirmed send keeps the alert path at-least-once and makes
// deduplication best-effort, which is the correct way round for a channel whose
// entire job is to not stay quiet.
//
// The residual race — two deliveries in flight simultaneously, both reading
// "unmarked" before either marks — is left unhandled. EventBridge's duplicate
// deliveries and Lambda's async-invoke retries are separated by seconds to
// minutes, and closing it would require the conditional-write dance above.
type markerStore struct{ blob blobAPI }

// blobAPI is the slice of s3blob.Blob this needs, named so tests can substitute
// an in-memory double without reaching AWS.
type blobAPI interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, data []byte) error
}

// token returns the body recorded by the last alert under key, and whether a
// marker exists at all.
//
// A read error reports "no marker": the failure mode of a broken marker store
// must be a duplicate alert, never a swallowed one.
func (m *markerStore) token(ctx context.Context, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	data, found, err := m.blob.Get(ctx, key)
	if err != nil {
		log.Printf("marker read %s: %v (treating as unsent)", key, err)
		return "", false
	}
	if !found {
		return "", false
	}
	return string(data), true
}

// sent reports whether this alert has already gone out. Existence is the whole
// question for the task path, where one key names one stopped task; the
// heartbeat needs the token instead, because its key names a job that outlives
// any single run.
func (m *markerStore) sent(ctx context.Context, key string) bool {
	_, found := m.token(ctx, key)
	return found
}

// record marks the alert as sent. Best-effort: a write failure costs a duplicate
// on the next delivery, which is the same outcome as having no marker at all,
// so it must not fail the invocation and trigger an async-invoke retry.
func (m *markerStore) record(ctx context.Context, key, note string) {
	if m == nil {
		return
	}
	if err := m.blob.Put(ctx, key, []byte(note)); err != nil {
		log.Printf("marker write %s: %v (a repeat delivery will alert again)", key, err)
	}
}

// alert is one Pushover together with the identity that makes it at-most-once.
//
// note becomes the marker object's whole body. On the task path it is never read
// back — one key names one stopped task, so existence alone answers the
// question, and the note only tells an operator staring at the prefix which
// delivery of a multi-delivery event won. On the heartbeat path it is
// load-bearing: see opsalert.Missed.NeedsAlert.
type alert struct {
	key, note   string
	title, body string
}

// send delivers the alert and records its marker. It does NOT itself check
// whether the marker already exists — each caller decides that, because "already
// alerted" means something different on the two paths.
//
// The ordering is check → send → mark, never claim → send. A claim taken before
// sending has to be released when the send fails, and a failed release leaves a
// permanent suppression: trading the bug this fixes (a duplicate alert) for a
// strictly worse one (a missing alert).
func send1(ctx context.Context, m *markerStore, a alert) error {
	if a.title == "" {
		return nil
	}
	if err := sendOrLog(a.title, a.body); err != nil {
		return err
	}
	m.record(ctx, a.key, a.note)
	return nil
}

// sendOnce is send1 gated on the marker not already existing — the task path's
// rule, where one key names one stopped task.
func sendOnce(ctx context.Context, m *markerStore, a alert) error {
	if a.title == "" {
		return nil
	}
	if m.sent(ctx, a.key) {
		log.Printf("already alerted for %s; staying quiet", a.key)
		return nil
	}
	return send1(ctx, m, a)
}

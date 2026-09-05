package main

import (
	"context"
	"log"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// markerPrefix is where one-shot alert markers live under STATE_BUCKET.
//
// Its authority is layout.OpsAlertMarkers.S3Prefix — this Lambda imports the
// table directly, but the CDK lifecycle rule and IAM grant in infra/ cannot
// (infra/ is its own Go module with no compiler link here), so those two
// restate the same string as literals behind "must match" comments instead
// (the same arrangement as layout.TenantRoster, rosterbot-3vr).
var markerPrefix = layout.OpsAlertMarkers.S3Prefix

// markerStore records which alerts have already been sent, so a second
// delivery of the same event stays quiet. The check → send → mark discipline
// itself now lives in internal/alertmarker (rosterbot-chs); this type is a
// thin delegate that keeps this package's existing entry points
// (token/sent/record/send1/sendOnce) and their nil-safety, wired with this
// Lambda's own logger and key sanitization. See internal/alertmarker for the
// discipline: why it's check-then-send-then-mark, and why every marker-store
// failure degrades to a duplicate alert rather than silence.
type markerStore struct{ blob blobAPI }

// blobAPI is the slice of s3blob.Blob this needs, named so tests can substitute
// an in-memory double without reaching AWS.
type blobAPI interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, data []byte) error
}

// blobStoreAdapter satisfies alertmarker.Store over a blobAPI.
type blobStoreAdapter struct{ blob blobAPI }

func (a blobStoreAdapter) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return a.blob.Get(ctx, key)
}

// Publish deliberately outlives the event ctx — alertmarker.Store carries no
// context parameter, matching the old record()'s best-effort semantics: a
// marker write must not be aborted by the invocation's own deadline, since a
// failure here only ever costs a duplicate alert, never a fatal one.
func (a blobStoreAdapter) Publish(key string, data []byte) error {
	return a.blob.Put(context.Background(), key, data)
}

// alertMarker builds the internal/alertmarker.Marker this delegates to, wired
// with this Lambda's log.Printf-compatible logger and logSafe key
// sanitization. Built on demand rather than cached on the struct, so
// markerStore stays constructible as a plain {blob: ...} literal — the seam
// marker_test.go's blobAPI fakes drive.
func (m *markerStore) alertMarker() *alertmarker.Marker {
	if m == nil {
		return nil
	}
	return alertmarker.New(blobStoreAdapter{blob: m.blob},
		alertmarker.WithLogf(log.Printf),
		alertmarker.WithKeyDisplay(logSafe),
	)
}

// token returns the body recorded by the last alert under key, and whether a
// marker exists at all.
func (m *markerStore) token(ctx context.Context, key string) (string, bool) {
	return m.alertMarker().Token(ctx, key)
}

// sent reports whether this alert has already gone out. Existence is the whole
// question for the task path, where one key names one stopped task; the
// heartbeat needs the token instead, because its key names a job that outlives
// any single run.
func (m *markerStore) sent(ctx context.Context, key string) bool {
	return m.alertMarker().Sent(ctx, key)
}

// record marks the alert as sent. Best-effort: a write failure costs a
// duplicate on the next delivery, which is the same outcome as having no
// marker at all, so it must not fail the invocation and trigger an
// async-invoke retry.
func (m *markerStore) record(key, note string) {
	m.alertMarker().Record(key, []byte(note))
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

// send1 delivers the alert and records its marker: check → send → mark, never
// claim → send. A failed send returns its error with nothing marked, so the
// next delivery retries. It does NOT itself check whether the marker already
// exists — each caller decides that, because "already alerted" means
// something different on the two paths. The empty-title "stay quiet" skip
// stays at this layer: internal/alertmarker deliberately leaves skip-logging
// and key grammar to call sites.
func send1(ctx context.Context, m *markerStore, a alert) error {
	if a.title == "" {
		return nil
	}
	if err := sendOrLog(ctx, a.title, a.body); err != nil {
		return err
	}
	m.record(a.key, a.note)
	return nil
}

// sendOnce is send1 gated on the marker not already existing — the task path's
// rule, where one key names one stopped task.
func sendOnce(ctx context.Context, m *markerStore, a alert) error {
	if a.title == "" {
		return nil
	}
	if m.sent(ctx, a.key) {
		log.Printf("already alerted for %s; staying quiet", logSafe(a.key))
		return nil
	}
	return send1(ctx, m, a)
}

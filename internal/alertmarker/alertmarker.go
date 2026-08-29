// Package alertmarker owns the check → send → mark discipline behind every
// one-shot operational alert (rosterbot-chs).
//
// The rule it encodes, once: an alert is deduplicated by a durable marker, the
// marker is written only AFTER a confirmed send, and every marker-store
// failure degrades to a DUPLICATE alert, never to silence. A claim taken
// before sending would have to be released when the send fails, and a failed
// release leaves a permanent suppression — trading the bug this fixes (a
// duplicate) for a strictly worse one (a missing alert). Deduplication is
// best-effort; delivery is the job.
//
// A nil *Marker, or a Marker over a nil Store, disables dedup and NOT the
// alert: every call sends. That is the correct local-dev default (alert every
// run rather than stay quiet about something there is no record of), and it is
// also the correct degraded mode when a marker store cannot even be
// constructed — callers must warn and pass nil, never abort the run
// (cmd/football_trades.go once hard-failed there, which silenced the alert a
// transient S3 hiccup should only have duplicated).
//
// Durable markers, not in-process state, because every scheduled run is a
// fresh container: without a marker that outlives the process, each run
// re-announces the same standing condition — the 30-pushes-a-day flood the
// stale-cache alert produced during the 2026-08-19 FanGraphs outage.
//
// This package is a stdlib-only leaf on purpose. internal/cache imports it, so
// it must never grow a dependency on internal/lineupapi (whose BlobStore
// satisfies Store structurally) or anything heavier.
//
// What stays at the call site, deliberately: the key grammar (which facts
// identify "this alert" is a per-site decision — see ilStartMarkerKey,
// gsFloorMarkerKey, staleMarkerKey), the dry-run guard (a dry run must skip
// send AND mark, before ever reaching this package — marking on a dry run
// would mute a later real alert), and any logging of the skip path.
package alertmarker

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Store is the durable marker seam. lineupapi.BlobStore satisfies it
// structurally, which is how statestore's marker constructors wire in without
// this leaf importing lineupapi. Get reports absence as found=false with a nil
// error; Publish overwrites unconditionally.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Publish(key string, data []byte) error
}

// Marker runs the discipline over one Store. The zero value and nil are both
// usable: they send every time and record nothing.
type Marker struct {
	store   Store
	logf    func(format string, args ...any)
	display func(key string) string
}

// Option configures a Marker.
type Option func(*Marker)

// WithLogf routes the module's degrade warnings (marker read/write failures)
// through the caller's logger, so each site keeps its own prefix and writer —
// "  il-start check: …" to the run output, "warning: …" to stderr, log.Printf
// in a Lambda. The default writes "warning: <msg>" lines to stderr.
func WithLogf(logf func(format string, args ...any)) Option {
	return func(m *Marker) { m.logf = logf }
}

// WithKeyDisplay sanitizes a key before it is interpolated into a log line —
// opsnotify's logSafe, for keys derived from externally-sourced values. The
// default is identity.
func WithKeyDisplay(display func(key string) string) Option {
	return func(m *Marker) { m.display = display }
}

// New builds a Marker over store. A nil store is a working configuration: no
// dedup, alert every run.
func New(store Store, opts ...Option) *Marker {
	m := &Marker{store: store}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Token returns the trimmed body recorded by the last alert under key, and
// whether a marker exists at all. A read failure reports "no marker" and logs
// — the failure mode of a broken marker store must be a duplicate alert, never
// a swallowed one.
func (m *Marker) Token(ctx context.Context, key string) (string, bool) {
	if m == nil || m.store == nil {
		return "", false
	}
	body, found, err := m.store.Get(ctx, key)
	if err != nil {
		m.warnf("marker read %s: %v (treating as unsent)", m.disp(key), err)
		return "", false
	}
	if !found {
		return "", false
	}
	return strings.TrimSpace(string(body)), true
}

// Sent reports whether any marker exists under key. Existence is the whole
// question for sites whose key names one event (a transaction, a start date);
// sites whose key outlives its episodes compare Token instead.
func (m *Marker) Sent(ctx context.Context, key string) bool {
	_, found := m.Token(ctx, key)
	return found
}

// Record marks the alert as sent, best-effort: a write failure costs a
// duplicate on the next run, which is the same outcome as having no marker at
// all, so it is logged and never returned. Writing an empty note is how a
// standing alert is cleared on a seam with no Delete — an empty token reads
// like an absent one to Token-comparing sites.
func (m *Marker) Record(key string, note []byte) {
	if m == nil || m.store == nil {
		return
	}
	if err := m.store.Publish(key, note); err != nil {
		m.warnf("marker write %s: %v (a later run will alert again)", m.disp(key), err)
	}
}

// Send delivers unconditionally, then records: check → send → mark, never
// claim-then-send. A failed send returns its error with nothing marked, so the
// next run retries — a failed send must retry, never go silent.
func (m *Marker) Send(key string, note []byte, send func() error) error {
	if err := send(); err != nil {
		return err
	}
	m.Record(key, note)
	return nil
}

// SendOnce is Send gated on no marker existing yet — the rule for sites whose
// key names exactly one alertable event. It returns whether the send was
// attempted: (false, nil) means an earlier run already alerted.
func (m *Marker) SendOnce(ctx context.Context, key string, note []byte, send func() error) (bool, error) {
	if m.Sent(ctx, key) {
		return false, nil
	}
	if err := m.Send(key, note, send); err != nil {
		return false, err
	}
	return true, nil
}

// SendOnChange is Send gated on the recorded token differing from note — the
// rule for sites whose key is stable while the condition it reports has
// episodes (the stale-cache alert, keyed by cache key, with the episode
// identity in the body). The comparison trims both sides, matching Token.
func (m *Marker) SendOnChange(ctx context.Context, key string, note []byte, send func() error) (bool, error) {
	if tok, found := m.Token(ctx, key); found && tok == strings.TrimSpace(string(note)) {
		return false, nil
	}
	if err := m.Send(key, note, send); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Marker) warnf(format string, args ...any) {
	if m != nil && m.logf != nil {
		m.logf(format, args...)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

func (m *Marker) disp(key string) string {
	if m != nil && m.display != nil {
		return m.display(key)
	}
	return key
}

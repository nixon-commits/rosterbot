// Package wiretime owns the one timestamp shape every /v1 HTTP response emits:
// RFC3339 UTC with NO fractional seconds.
//
// WHY A TYPE RATHER THAN A CONVENTION. Declaring the field a string and
// formatting it at construction is the pre-existing idiom at a dozen sites
// (internal/lineuprun/publish.go, internal/dynasty, internal/lineupgap,
// internal/report, internal/progress, internal/transactions, internal/opsalert,
// lineupapi's own types.go/pushdevice.go/notifications.go). It works, but it is
// a MANUAL-DISCIPLINE control: the failure mode is not a typo in the layout
// string, it is omitting the step entirely and letting encoding/json fall back
// to time.Time's RFC3339Nano. That has now been forgotten three times
// (internal/availablepool, internal/tradeboard twice), each time shipping a
// field a strict client cannot parse. A control that must be remembered at N
// sites and has already failed three times does not scale to N (rosterbot-4e1j).
//
// WHAT GOES WRONG WITHOUT IT. time.Time marshals as RFC3339Nano, whose
// fractional component is VARIABLE-LENGTH and vanishes entirely when the
// nanosecond count happens to be zero:
//
//	nanos=902729184 -> "2026-08-24T21:47:27.902729184Z"
//	nanos=900000000 -> "2026-08-24T21:47:27.9Z"
//	nanos=0         -> "2026-08-24T21:47:27Z"
//
// So WHICH CALL PATH SUPPLIES the value decides whether the bug is live or
// merely latent: a field fed a UTC-midnight date has only ever emitted the
// third shape and passes every test written against the live endpoint, then
// breaks the day somebody changes that caller to time.Now() — a one-word edit
// with no visible connection to the wire format. The exposure is the iOS
// client, whose ISO8601DateFormatter([.withInternetDateTime]) returns nil on a
// fractional timestamp, and a nil date there is typically not an error but a
// staleness check that silently never fires.
//
// SCOPE. This is for fields on /v1/* HTTP responses only. Internal artifacts
// that Go writes and Go reads back — internal/cache, internal/analysis, the run
// ledger, the backtest snapshots, the DynamoDB identity records — round-trip
// RFC3339Nano through encoding/json perfectly and must NOT be churned onto this
// type. Where a durable record is also served on the wire (lineupapi.Membership
// is stored in the user record AND returned by GET /v1/memberships), the
// conversion belongs in the response DTO, not in the stored struct. That is not
// merely conservatism: DynamoDB records go through attributevalue, which ignores
// json tags and encodes a struct as a timestamp only when its type is
// ConvertibleTo(time.Time). The unexported field below means Time is NOT
// convertible, so attributevalue takes the generic struct path and — every field
// being unexported — writes an empty map. Measured: a time.Time field marshals
// to an S attribute, this type to an M with no members.
//
// Stdlib-only leaf, so every producer of a /v1 payload can import it without
// dragging anything behind it.
package wiretime

import (
	"encoding/json"
	"fmt"
	"time"
)

// Time is a timestamp that can only marshal one way.
//
// The wrapped value is UNEXPORTED and every constructor canonicalizes it to UTC
// whole seconds, so there is no way to seat a value that would marshal
// differently — which is the whole point of the type over the string idiom it
// replaces. The zero Time is the zero time.Time and marshals as
// "0001-01-01T00:00:00Z", byte-identical to what a raw time.Time field emits
// today, so converting a field does not change what an unset value looks like
// on the wire.
type Time struct {
	t time.Time
}

// New canonicalizes t for the wire: UTC, whole seconds.
//
// Truncation happens HERE rather than only in MarshalJSON so that the value a
// caller can read back with Time() is the value the client will see. A type
// whose accessor disagreed with its own JSON would just relocate the surprise.
func New(t time.Time) Time {
	if t.IsZero() {
		return Time{}
	}
	return Time{t: t.UTC().Truncate(time.Second)}
}

// Now is New(time.Now()) — the call path that makes the defect LIVE rather than
// latent, which is why it is spelled once here instead of at each call site.
func Now() Time { return New(time.Now()) }

// Time returns the underlying instant: UTC, truncated to the second.
func (w Time) Time() time.Time { return w.t }

// IsZero reports whether this is the zero timestamp. It exists so callers can
// tag a field `omitzero` — `omitempty` has never omitted a time.Time (a struct
// is never "empty") and does not omit this one either.
func (w Time) IsZero() bool { return w.t.IsZero() }

// String is the wire form, so a value printed into a log or an error reads the
// same as the value a client received.
func (w Time) String() string { return w.t.UTC().Format(time.RFC3339) }

// MarshalJSON emits RFC3339 UTC with no fraction.
//
// It re-applies UTC rather than trusting the constructor. That is not
// belt-and-braces: the zero value is reachable without any constructor at all
// (a struct literal that omits the field), and a future constructor that forgot
// to normalize would otherwise reintroduce exactly the class this type exists to
// make unreachable. time.RFC3339's layout carries no fractional-second element,
// so Format drops the fraction whatever the input holds.
func (w Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(w.t.UTC().Format(time.RFC3339))
}

// UnmarshalJSON accepts anything RFC3339 — fraction included — and canonicalizes
// it.
//
// Being liberal on the way in is deliberate: this type is on both sides of a
// round trip in tests and in any client written in Go, and it must also be able
// to read a payload produced BEFORE the field was converted, which carries the
// full RFC3339Nano fraction. time.Parse with the RFC3339 layout already accepts
// an optional fraction, so one layout covers both.
//
// null and "" both decode to the zero Time. An absent value is the ordinary
// state for every omitempty timestamp on this API, and rejecting it would turn
// "not connected yet" into a decode error.
func (w *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// null decodes into a *Time as a no-op before reaching here for a
		// pointer field; for a value field encoding/json hands us the literal.
		if string(b) == "null" {
			*w = Time{}
			return nil
		}
		return fmt.Errorf("wiretime: %w", err)
	}
	if s == "" {
		*w = Time{}
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("wiretime: parse %q: %w", s, err)
	}
	*w = New(t)
	return nil
}

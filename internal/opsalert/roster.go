package opsalert

import "encoding/json"

// RosterKey is the object key, relative to layout.TenantRoster's prefix, that
// the fan-out dispatcher writes and the ops notifier reads.
//
// It lives in this stdlib-only leaf, alongside Marshal/ParseRoster, because the
// writer (lambda/dispatch) and the reader (opsnotify) are in separate Go
// modules and nothing else links them. Both import this package already — the
// writer for nothing else, admittedly, but that is the price of the two sides
// being unable to drift. A key restated as a literal on each side is precisely
// how the run ledger's reader ended up looking somewhere its writer had left
// (rosterbot-3vr).
const RosterKey = "active.json"

// Roster is the active tenant list.
//
// It is a struct rather than a bare []string so the wire format can gain a
// field — a generation stamp, a per-tenant status — without every reader
// needing to change shape on the same deploy. The two sides ship
// independently: the dispatcher rides the container image, the notifier is a
// Lambda bundle.
type Roster struct {
	UserIDs []string `json:"user_ids"`
}

// MarshalRoster renders the roster for storage.
func MarshalRoster(userIDs []string) ([]byte, error) {
	if userIDs == nil {
		userIDs = []string{}
	}
	return json.Marshal(Roster{UserIDs: userIDs})
}

// ParseRoster reads a stored roster.
//
// A malformed or empty document yields no tenants and NO ERROR by design.
// Every caller's fallback is the ledger-derived tenant set, which is what this
// augments rather than replaces — so a bad roster degrades alerting to what it
// was before crq.4, and never below it. Returning an error would invite a
// caller to abort the heartbeat entirely, trading a narrow blind spot for total
// silence.
func ParseRoster(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var r Roster
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	out := make([]string, 0, len(r.UserIDs))
	for _, id := range r.UserIDs {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

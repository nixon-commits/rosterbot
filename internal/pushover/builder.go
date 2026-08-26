package pushover

import (
	"strings"
	"unicode/utf8"
)

// MaxMessageLen is Pushover's documented message-length cap, in bytes. One
// constant, one owner: every formatter that budgets whole blocks (via
// Builder) and Send's own backstop (Truncate) measure against this same
// value — the divergent 1000-vs-1024 hand margins this replaces were exactly
// the drift a shared owner prevents.
const MaxMessageLen = 1024

// Builder assembles a Pushover message from whole blocks, never exceeding
// MaxMessageLen. Add appends a block verbatim when it fits the remaining
// budget and reports whether it did; a refusal does NOT close the builder —
// a later, smaller block may still fit, which the prospects body relies on
// (its upgrades section follows an alerts section that may have overflowed).
// Blocks carry their own separators; the builder inserts nothing between
// them. The zero value is ready to use.
type Builder struct {
	b strings.Builder
}

// Add appends block iff it fits, reporting whether it did.
func (b *Builder) Add(block string) bool {
	if b.b.Len()+len(block) > MaxMessageLen {
		return false
	}
	b.b.WriteString(block)
	return true
}

// Len reports the bytes accepted so far.
func (b *Builder) Len() int { return b.b.Len() }

// String returns the assembled message.
func (b *Builder) String() string { return b.b.String() }

// Truncate returns s unchanged when it fits MaxMessageLen; otherwise the
// longest prefix that leaves room for the "…" it appends, cut on a rune
// boundary — never mid-rune. The backstop for messages assembled without a
// Builder.
func Truncate(s string) string {
	if len(s) <= MaxMessageLen {
		return s
	}
	const ellipsis = "…"
	cut := MaxMessageLen - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// ShortName collapses "First Last…" to "F. Last…" to save message bytes.
// Moved here from the byte-identical waivers/prospects helpers; it lives
// beside the budget because fitting names into MaxMessageLen is its whole
// reason to exist.
func ShortName(name string) string {
	parts := strings.Fields(name)
	if len(parts) < 2 {
		return name
	}
	first := []rune(parts[0])
	if len(first) == 0 {
		return name
	}
	return string(first[:1]) + ". " + strings.Join(parts[1:], " ")
}

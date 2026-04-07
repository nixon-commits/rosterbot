package playername

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize produces a canonical player name for cross-source matching.
// It strips diacritics, lowercases, removes suffixes (Jr./Sr./II/III/IV),
// removes periods, and collapses whitespace.
func Normalize(name string) string {
	// Strip diacritics via Unicode NFD decomposition.
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.TrimSpace(name)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	s := strings.ToLower(b.String())

	// Strip common name suffixes.
	for _, suffix := range []string{" jr.", " jr", " sr.", " sr", " iv", " iii", " ii"} {
		s = strings.TrimSuffix(s, suffix)
	}

	// Remove periods (e.g., "A.J." → "AJ").
	s = strings.ReplaceAll(s, ".", "")

	// Collapse multiple spaces and trim.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

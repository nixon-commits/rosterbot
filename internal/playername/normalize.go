package playername

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize produces a canonical player name for cross-source matching.
// It strips diacritics, lowercases, removes suffixes (Jr./Sr./II/III/IV),
// removes periods/commas/apostrophes, treats hyphens as word breaks, and
// collapses whitespace.
func Normalize(name string) string {
	// Strip diacritics via Unicode NFD decomposition.
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.TrimSpace(name)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	s := strings.ToLower(b.String())

	// Drop punctuation that varies by source without changing word
	// boundaries: periods ("A.J." vs "AJ"), a comma before a suffix
	// ("Guerrero, Jr." vs "Guerrero Jr."), and apostrophes in any encoding a
	// source might use for one — a straight U+0027, the left/right single
	// quotation marks (U+2018/U+2019) a "smart quote" text pipeline commonly
	// substitutes, or a bare grave accent (U+0060) — so "O'Neill" matches
	// regardless of which one a given source wrote. Removed rather than
	// replaced with a space, matching how the period case already worked.
	s = dropRunes(s, '.', ',', '\'', '‘', '’', '`')

	// Hyphens vary by source too ("Ha-Seong Kim" vs "Ha Seong Kim");
	// treated as a word boundary, not deleted outright, so both forms
	// collapse to the same space-separated tokens.
	s = strings.ReplaceAll(s, "-", " ")

	// Strip common name suffixes (periods/commas above already ran, so only
	// the bare space-separated forms are needed here).
	for _, suffix := range []string{" jr", " sr", " iv", " iii", " ii"} {
		s = strings.TrimSuffix(s, suffix)
	}

	// Collapse multiple spaces and trim.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// dropRunes returns s with every rune in drop removed.
func dropRunes(s string, drop ...rune) string {
	set := make(map[rune]bool, len(drop))
	for _, r := range drop {
		set[r] = true
	}
	return strings.Map(func(r rune) rune {
		if set[r] {
			return -1
		}
		return r
	}, s)
}

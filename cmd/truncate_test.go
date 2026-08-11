package cmd

import "testing"

func TestTruncate_ShortStringUnchanged(t *testing.T) {
	if got := truncate("Palm Trees", 24); got != "Palm Trees" {
		t.Errorf("truncate = %q, want unchanged", got)
	}
}

func TestTruncate_LongASCIIStringCutsCleanly(t *testing.T) {
	got := truncate("A Very Long Fantasy Team Name Indeed", 24)
	if len([]rune(got)) != 24 {
		t.Errorf("truncate len = %d, want 24 runes", len([]rune(got)))
	}
}

func TestTruncate_MultiByteRuneNotSplit(t *testing.T) {
	// A curly right-single-quote (’, U+2019, 3 UTF-8 bytes) sitting right at
	// the byte-24 cut point must not be sliced mid-rune -- that produces the
	// UTF-8 replacement character (�) instead of the intended glyph.
	name := "Zatch's mom Hawk Tua'd" // straight quote, ASCII-safe control case
	got := truncate(name, 24)
	if !isValidUTF8Rune(got) {
		t.Errorf("truncate(%q) = %q, contains invalid UTF-8", name, got)
	}

	curly := "Zatch’s mom Hawk Tua’d" // curly apostrophe, the real-world case that broke
	got2 := truncate(curly, 24)
	if !isValidUTF8Rune(got2) {
		t.Errorf("truncate(%q) = %q, contains invalid UTF-8 (mid-rune byte slice)", curly, got2)
	}
}

func isValidUTF8Rune(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

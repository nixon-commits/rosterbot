package pushover

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMaxMessageLen_IsPushoversDocumentedCap(t *testing.T) {
	if MaxMessageLen != 1024 {
		t.Fatalf("MaxMessageLen = %d, want 1024", MaxMessageLen)
	}
}

func TestBuilder_AddWholeBlocksUnderTheCap(t *testing.T) {
	var b Builder // zero value ready to use
	if !b.Add(strings.Repeat("a", 1000)) {
		t.Fatal("a block within budget must be accepted")
	}
	if b.Add(strings.Repeat("b", 100)) {
		t.Fatal("a block that would overflow must be refused")
	}
	if b.Len() != 1000 {
		t.Fatalf("a refused block must not be appended: Len = %d, want 1000", b.Len())
	}
	// No latch: after a refusal a smaller block still fits — the prospects
	// body relies on this (its upgrades section follows an alerts section
	// that may have overflowed).
	if !b.Add(strings.Repeat("c", 24)) {
		t.Fatal("a smaller block after a refusal must still be accepted")
	}
	got := b.String()
	if len(got) != MaxMessageLen {
		t.Fatalf("assembled length = %d, want exactly %d", len(got), MaxMessageLen)
	}
	if !strings.HasSuffix(got, "c") {
		t.Fatal("accepted blocks must append in order")
	}
	if b.Add("x") {
		t.Fatal("an exactly-full builder must refuse any non-empty block")
	}
}

func TestBuilder_ExactFitIsAccepted(t *testing.T) {
	var b Builder
	if !b.Add(strings.Repeat("a", MaxMessageLen)) {
		t.Fatal("a block of exactly MaxMessageLen must be accepted")
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short"); got != "short" {
		t.Fatalf("short input must pass through unchanged, got %q", got)
	}
	exact := strings.Repeat("a", MaxMessageLen)
	if got := Truncate(exact); got != exact {
		t.Fatal("input of exactly MaxMessageLen must pass through unchanged")
	}

	// 600 two-byte runes = 1200 bytes; the cut point lands mid-rune and must
	// back up to a boundary — the mid-rune split is the failure every
	// per-formatter budget exists to avoid, so the shared backstop must not
	// reintroduce it.
	long := strings.Repeat("é", 600)
	got := Truncate(long)
	if len(got) > MaxMessageLen {
		t.Fatalf("truncated length = %d, want <= %d", len(got), MaxMessageLen)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a multi-byte rune")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("a truncated message must end with an ellipsis")
	}
	for _, r := range strings.TrimSuffix(got, "…") {
		if r != 'é' {
			t.Fatalf("unexpected rune %q survived truncation", r)
		}
	}
}

func TestShortName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Aaron Judge", "A. Judge"},
		{"José Ramírez", "J. Ramírez"}, // first initial is a rune, not a byte
		{"Ha-Seong Kim", "H. Kim"},
		{"Vladimir Guerrero Jr.", "V. Guerrero Jr."},
		{"Ichiro", "Ichiro"}, // single token passes through
		{"", ""},
	}
	for _, c := range cases {
		if got := ShortName(c.in); got != c.want {
			t.Errorf("ShortName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

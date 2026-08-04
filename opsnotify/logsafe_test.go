package main

import (
	"strings"
	"testing"
)

func TestLogSafe(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ordinary value passes through", "optimize --matchup", "optimize --matchup"},
		{"a real task arn suffix is untouched", "680a88e087484779ac7eabb32d8cd85c", "680a88e087484779ac7eabb32d8cd85c"},
		{"newline is dropped", "abc\ndef", "abcdef"},
		{"carriage return is dropped", "abc\r\ndef", "abcdef"},
		{"tab and other C0 controls are dropped", "a\tb\x00c\x1bd", "abcd"},
		{"DEL is dropped", "a\x7fb", "ab"},
		{"non-ASCII text survives", "café ✓", "café ✓"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logSafe(tt.in); got != tt.want {
				t.Errorf("logSafe(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The point of the whole function: a value carrying a newline must not be able
// to render as two log lines, because an operator reading this log is doing so
// mid-incident and has to be able to trust the line count.
func TestLogSafe_CannotForgeASecondLogLine(t *testing.T) {
	forged := "task123\n2026/08/04 12:00:00 heartbeat: all 13 jobs within cadence"
	got := logSafe(forged)
	if strings.Contains(got, "\n") {
		t.Fatalf("logSafe kept a newline: %q", got)
	}
	if strings.Count(got+"\n", "\n") != 1 {
		t.Errorf("logSafe(%q) still spans multiple lines: %q", forged, got)
	}
}

// An unbounded value would bury the surrounding lines, which is the same denial
// of legibility by a different route.
func TestLogSafe_TruncatesRunawayValues(t *testing.T) {
	got := logSafe(strings.Repeat("x", maxLogValue*3))
	if len([]rune(got)) != maxLogValue+1 { // +1 for the ellipsis
		t.Errorf("len = %d runes, want %d", len([]rune(got)), maxLogValue+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("want a truncation marker, got %q", got[len(got)-8:])
	}
}

// Truncation counts runes, not bytes — cutting mid-rune would turn a legible
// value into mojibake at exactly the boundary.
func TestLogSafe_TruncatesOnRuneBoundaries(t *testing.T) {
	got := logSafe(strings.Repeat("é", maxLogValue+50))
	if !strings.HasPrefix(got, strings.Repeat("é", maxLogValue)) {
		t.Errorf("truncation split a multi-byte rune: %q", got)
	}
}

package cmd

import "testing"

func TestResolveCursorPath(t *testing.T) {
	if got := resolveCursorPath(""); got != "" {
		t.Fatalf("empty env should yield empty (run.go applies default), got %q", got)
	}
	if got := resolveCursorPath(".waivers/last-claims.json"); got != ".waivers/last-claims.json" {
		t.Fatalf("env override not honored, got %q", got)
	}
}

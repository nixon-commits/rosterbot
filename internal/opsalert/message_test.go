package opsalert

import (
	"strings"
	"testing"
)

func failed(command, logTail string, exit int) *Record {
	return &Record{ID: "t1", Command: command, Status: StatusFailed, ExitCode: &exit, LogTail: logTail}
}

const staleClient = `Usage:
  rosterbot optimize [flags]

fantrax client: fantrax auth client: failed to fetch user info during client initialization: failed to read login response body: fantrax API error STALE_CLIENT: Your browser is using an outdated cached version.`

func TestFormatTask_Started(t *testing.T) {
	v := Verdict{
		Kind:    Started,
		Command: "optimize --matchup --archive-projections",
		Streak:  1,
		Failure: failed("optimize --matchup --archive-projections", staleClient, 1),
	}
	title, body := FormatTask(v)

	if title != "Rosterbot: optimize failed" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"❌", "--matchup", "exit 1", "STALE_CLIENT"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
	// Only the last non-empty line of the tail is quoted, not the usage banner.
	if strings.Contains(body, "Usage:") {
		t.Errorf("body quoted the whole log tail, want only its last line: %q", body)
	}
}

func TestFormatTask_Escalated(t *testing.T) {
	v := Verdict{
		Kind:    Escalated,
		Command: "optimize --matchup",
		Streak:  3,
		Failure: failed("optimize --matchup", "boom", 1),
	}
	title, body := FormatTask(v)
	if title != "Rosterbot: optimize still failing" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"🔥", "3", "in a row"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestFormatTask_Recovered(t *testing.T) {
	title, body := FormatTask(Verdict{Kind: Recovered, Command: "shadow", Streak: 11})
	if title != "Rosterbot: shadow recovered" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"✅", "11 failures"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestFormatTask_RecoveredSingularFailure(t *testing.T) {
	_, body := FormatTask(Verdict{Kind: Recovered, Command: "grade", Streak: 1})
	if !strings.Contains(body, "1 failure") || strings.Contains(body, "1 failures") {
		t.Errorf("body %q should say %q", body, "1 failure")
	}
}

// None must render nothing at all — the caller uses the empty title as its
// "do not send" signal, so a stray body here becomes a spurious push.
func TestFormatTask_NoneRendersNothing(t *testing.T) {
	title, body := FormatTask(Verdict{Kind: None, Command: "optimize", Streak: 2})
	if title != "" || body != "" {
		t.Errorf("got (%q, %q), want both empty", title, body)
	}
}

// SendPushover truncates silently at 1024 chars, so the cause must be bounded
// before it is appended — otherwise a long tail eats the command and exit code.
func TestFormatTask_CauseIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, body := FormatTask(Verdict{
		Kind:    Started,
		Command: "optimize",
		Streak:  1,
		Failure: failed("optimize", long, 1),
	})
	if len(body) > MaxCause+200 {
		t.Errorf("body is %d chars, want bounded near MaxCause=%d", len(body), MaxCause)
	}
	if !strings.Contains(body, "…") {
		t.Errorf("truncated body %q should be marked with an ellipsis", body)
	}
	if !strings.Contains(body, "exit 1") {
		t.Errorf("truncation ate the exit code: %q", body)
	}
}

func TestFormatTask_NoLogTail(t *testing.T) {
	v := Verdict{Kind: Started, Command: "grade", Streak: 1, Failure: failed("grade", "", 2)}
	_, body := FormatTask(v)
	if !strings.Contains(body, "exit 2") {
		t.Errorf("body %q missing exit code", body)
	}
	if strings.HasSuffix(body, "\n") {
		t.Errorf("body %q has a dangling newline where the cause would go", body)
	}
}

// A Started verdict with no Failure record should not panic.
func TestFormatTask_StartedWithNilFailure(t *testing.T) {
	_, body := FormatTask(Verdict{Kind: Started, Command: "grade", Streak: 1})
	if !strings.Contains(body, "grade") {
		t.Errorf("body = %q", body)
	}
}

func TestFormatCrash(t *testing.T) {
	title, body := FormatCrash("optimize --matchup", "abc123", "OutOfMemoryError: Container killed due to memory usage")
	if title != "Rosterbot: optimize died" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"💀", "no ledger record", "OutOfMemoryError", "abc123"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
}

func TestJobName(t *testing.T) {
	tests := map[string]string{
		"optimize --matchup --archive-projections": "optimize",
		"shadow":                "shadow",
		"recap-site --out dist": "recap-site",
		"":                      "task",
		"   ":                   "task",
	}
	for in, want := range tests {
		if got := JobName(in); got != want {
			t.Errorf("JobName(%q) = %q, want %q", in, got, want)
		}
	}
}

package opsalert

import (
	"fmt"
	"strings"
)

// MaxCause bounds the log-tail excerpt. notify.SendPushover truncates the whole
// message at 1024 characters silently, so the cause is capped before it is
// appended rather than after — otherwise a long stack trace would eat the
// command and exit code that make the alert triageable.
const MaxCause = 300

// FormatTask renders the Pushover (title, body) for a verdict. Kind None
// returns two empty strings; callers treat an empty title as "do not send".
func FormatTask(v Verdict) (title, body string) {
	job := JobName(v.Command)

	who := TenantTag(v.UserID)

	switch v.Kind {
	case Started:
		title = "Rosterbot: " + job + " failed"
		body = "❌ " + v.Command + who + exitSuffix(v.Failure)
	case Escalated:
		title = "Rosterbot: " + job + " still failing"
		body = fmt.Sprintf("🔥 %s%s failed %d× in a row%s", v.Command, who, v.Streak, exitSuffix(v.Failure))
	case Recovered:
		title = "Rosterbot: " + job + " recovered"
		return title, fmt.Sprintf("✅ %s%s recovered after %d %s",
			v.Command, who, v.Streak, plural(v.Streak, "failure", "failures"))
	default:
		return "", ""
	}

	if c := cause(v.Failure); c != "" {
		body += "\n" + c
	}
	return title, body
}

// FormatCrash renders the alert for a task that stopped without leaving a
// ledger record at all — the entrypoint never reached its final run-ledger
// write. That means OOM, an image-pull failure, or SIGKILL to pid 1: the class
// of failure the bot structurally cannot report on itself, and the reason this
// detector lives in a Lambda rather than in a bot subcommand.
//
// No streak is computable, so this always sends.
func FormatCrash(command, taskID, stoppedReason string) (title, body string) {
	title = "Rosterbot: " + JobName(command) + " died"

	body = "💀 " + strings.TrimSpace(command) + " · no ledger record"
	if stoppedReason != "" {
		body += "\n" + truncate(strings.TrimSpace(stoppedReason), MaxCause)
	}
	if taskID != "" {
		body += "\ntask " + taskID
	}
	return title, body
}

// maxTenantTag bounds the rendered tenant id. A tenant id is a WebAuthn user
// handle — 64 random bytes — and SendPushover truncates the whole message at
// 1024 characters silently, so an unbounded id would eat the cause it is meant
// to sit beside. This is display only and never a key; MarkerKey uses the id in
// full.
const maxTenantTag = 12

// TenantTag renders whose run a message is about, or "" when there is no tenant
// — so every alert predating per-tenant fan-out reads exactly as it did.
//
// The tag goes in the message *body*, beside the command, rather than in the
// title. Two tenants failing the same job produce two alerts whose titles are
// identical by construction, and the body's first line is what tells them apart
// on a lock screen; changing the title would also change every existing
// single-tenant alert for no gain.
func TenantTag(userID string) string {
	if userID == "" {
		return ""
	}
	return " · user " + truncate(userID, maxTenantTag)
}

// JobName is the leading word of a command — "optimize" out of
// "optimize --matchup --archive-projections" — so the Pushover title is
// triageable from a lock screen.
func JobName(command string) string {
	if f := strings.Fields(command); len(f) > 0 {
		return f[0]
	}
	return "task"
}

// exitSuffix renders " · exit N" when the record carries an exit code.
func exitSuffix(r *Record) string {
	if r == nil || r.ExitCode == nil {
		return ""
	}
	return fmt.Sprintf(" · exit %d", *r.ExitCode)
}

// cause is the last non-empty line of the record's captured log tail — for the
// STALE_CLIENT incident that is the error itself, which is the difference
// between an alert that says "something broke" and one that says what.
func cause(r *Record) string {
	if r == nil {
		return ""
	}
	lines := strings.Split(r.LogTail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return truncate(s, MaxCause)
		}
	}
	return ""
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

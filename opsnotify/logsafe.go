package main

import "strings"

// maxLogValue bounds one interpolated value. A ledger command or an ARN is tens
// of characters; anything approaching this is malformed, and letting it run is
// how one bad event buries the surrounding lines.
const maxLogValue = 200

// logSafe renders an externally-sourced value for a log line.
//
// Nothing this Lambda logs is data it authored: task ids and commands come out
// of an EventBridge event body, marker keys are derived from those, and the
// heartbeat's previous-alert token is read back out of S3. A newline in any of
// them forges a second CloudWatch line, so an operator reading the log during an
// incident cannot trust what they are looking at — and this log is only ever
// read during an incident. That is the entire exposure: none of these values
// reaches a query, a shell, or a user-facing surface.
//
// Realistically AWS generates the ARNs and the CDK generates the commands, so
// the odds of a hostile newline are low. The fix costs one function call, and
// the failure it prevents lands at exactly the moment clarity matters most.
//
// The explicit ReplaceAll of "\n" and "\r" is redundant with the control-rune
// filter below and kept deliberately: it is the form static analysis recognizes
// as a log-injection sanitizer, and a comment claiming safety is worth less than
// a pattern the scanner can see.
func logSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1 // drop the remaining C0 controls and DEL
		}
		return r
	}, s)

	// Truncate by rune, not byte, so a multi-byte character is never cut in half
	// into mojibake.
	if runes := []rune(s); len(runes) > maxLogValue {
		return string(runes[:maxLogValue]) + "…"
	}
	return s
}

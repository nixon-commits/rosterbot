package main

import (
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// formatMessage renders a Pushover (title, body) from a CodeBuild state-change
// event. Pure — no I/O — so it is unit-tested directly.
func formatMessage(ev events.CodeBuildEvent) (title, body string) {
	status := string(ev.Detail.BuildStatus)
	var emoji string
	switch ev.Detail.BuildStatus {
	case events.CodeBuildPhaseStatusSucceeded:
		emoji = "✅"
	case events.CodeBuildPhaseStatusFailed:
		emoji = "❌"
	case events.CodeBuildPhaseStatusStopped:
		emoji = "⏹️"
	default:
		emoji = "ℹ️"
	}

	title = "Rosterbot build " + status

	parts := []string{emoji}
	if sha := shortSHA(ev.Detail.AdditionalInformation.SourceVersion); sha != "" {
		parts = append(parts, sha)
	}
	body = strings.Join(parts, " ")
	if link := ev.Detail.AdditionalInformation.Logs.DeepLink; link != "" {
		body += " · " + link
	}
	return title, body
}

// shortSHA trims a git commit SHA to its 7-char prefix; short/empty values pass
// through unchanged.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

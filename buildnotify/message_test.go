package main

import (
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func evt(status events.CodeBuildPhaseStatus, sha, link string) events.CodeBuildEvent {
	var ev events.CodeBuildEvent
	ev.Detail.BuildStatus = status
	ev.Detail.ProjectName = "Build"
	ev.Detail.AdditionalInformation.SourceVersion = sha
	ev.Detail.AdditionalInformation.Logs.DeepLink = link
	return ev
}

func TestFormatMessage(t *testing.T) {
	tests := []struct {
		name        string
		ev          events.CodeBuildEvent
		wantTitle   string
		wantEmoji   string
		wantInBody  []string // substrings that must be present
		wantNotBody []string // substrings that must be absent
	}{
		{
			name:       "succeeded",
			ev:         evt(events.CodeBuildPhaseStatusSucceeded, "abc1234def5678", "https://logs.example/deep"),
			wantTitle:  "Rosterbot build SUCCEEDED",
			wantEmoji:  "✅",
			wantInBody: []string{"abc1234", "https://logs.example/deep"},
			// long SHA truncated to 7 chars — the 8th char must not appear glued on
			wantNotBody: []string{"abc1234d"},
		},
		{
			name:       "failed",
			ev:         evt(events.CodeBuildPhaseStatusFailed, "deadbeef", "https://logs.example/x"),
			wantTitle:  "Rosterbot build FAILED",
			wantEmoji:  "❌",
			wantInBody: []string{"deadbee", "https://logs.example/x"},
		},
		{
			name:        "stopped",
			ev:          evt(events.CodeBuildPhaseStatusStopped, "cafef00d", ""),
			wantTitle:   "Rosterbot build STOPPED",
			wantEmoji:   "⏹️",
			wantInBody:  []string{"cafef00"},
			wantNotBody: []string{" · "}, // no link separator when DeepLink empty
		},
		{
			name:        "no sha, no link",
			ev:          evt(events.CodeBuildPhaseStatusSucceeded, "", ""),
			wantTitle:   "Rosterbot build SUCCEEDED",
			wantEmoji:   "✅",
			wantNotBody: []string{" · "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, body := formatMessage(tt.ev)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if !strings.HasPrefix(body, tt.wantEmoji) {
				t.Errorf("body %q does not start with emoji %q", body, tt.wantEmoji)
			}
			for _, s := range tt.wantInBody {
				if !strings.Contains(body, s) {
					t.Errorf("body %q missing %q", body, s)
				}
			}
			for _, s := range tt.wantNotBody {
				if strings.Contains(body, s) {
					t.Errorf("body %q should not contain %q", body, s)
				}
			}
		})
	}
}

package gscheck

import (
	"strings"
	"testing"
)

func TestBuildReport_MaxViolations(t *testing.T) {
	violations := []Violation{
		{TeamName: "Team Alpha", GSUsed: 14, Kind: ViolationMax},
		{TeamName: "Team Beta", GSUsed: 13, Kind: ViolationMax},
	}
	periodLabel := "Scoring Period 5 (2026-03-30 – 2026-04-05)"

	title, summary := BuildReport(violations, periodLabel, 12, 0)

	if !strings.Contains(title, "2 violation(s)") {
		t.Errorf("title missing violation count: %s", title)
	}
	if !strings.Contains(title, periodLabel) {
		t.Errorf("title missing period label: %s", title)
	}
	if !strings.Contains(summary, "Team Alpha (14 GS, +2 over max)") {
		t.Errorf("summary missing Team Alpha: %s", summary)
	}
	if !strings.Contains(summary, "Team Beta (13 GS, +1 over max)") {
		t.Errorf("summary missing Team Beta: %s", summary)
	}
	if !strings.Contains(summary, "max 12") {
		t.Errorf("summary missing max: %s", summary)
	}
}

func TestBuildReport_MinViolation(t *testing.T) {
	violations := []Violation{
		{TeamName: "Slackers", GSUsed: 5, Kind: ViolationMin},
	}

	_, summary := BuildReport(violations, "Period 1", 12, 7)
	if !strings.Contains(summary, "Slackers (5 GS, 2 under min)") {
		t.Errorf("summary missing min violation: %s", summary)
	}
	if !strings.Contains(summary, "min 7") {
		t.Errorf("summary missing min label: %s", summary)
	}
}

func TestBuildReport_SingleViolation(t *testing.T) {
	violations := []Violation{
		{TeamName: "Violators", GSUsed: 15, Kind: ViolationMax},
	}

	title, _ := BuildReport(violations, "Period 1", 10, 0)
	if !strings.Contains(title, "1 violation(s)") {
		t.Errorf("title should show 1 violation: %s", title)
	}
}

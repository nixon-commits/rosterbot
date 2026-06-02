package gscheck

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func TestBuildReport_MaxViolations(t *testing.T) {
	violations := []Violation{
		{
			TeamName: "Team Alpha", GSUsed: 14, Kind: ViolationMax,
			Deductions: []fantrax.PitcherStart{
				{PitcherName: "Gerrit Cole", FPts: 28.5},
				{PitcherName: "Max Scherzer", FPts: 22.0},
			},
		},
		{TeamName: "Team Beta", GSUsed: 13, Kind: ViolationMax},
	}
	periodLabel := "Scoring Period 5 (2026-03-30 – 2026-04-05)"

	title, body := BuildReport(violations, periodLabel, 12, 0)

	if !strings.Contains(title, periodLabel) {
		t.Errorf("title missing period label: %s", title)
	}
	if !strings.Contains(body, "Team Alpha") || !strings.Contains(body, "14 GS") || !strings.Contains(body, "+2") {
		t.Errorf("body missing Team Alpha details: %s", body)
	}
	if !strings.Contains(body, "Gerrit Cole (28.5 pts)") {
		t.Errorf("body missing deduction for Gerrit Cole: %s", body)
	}
	if !strings.Contains(body, "Max Scherzer (22.0 pts)") {
		t.Errorf("body missing deduction for Max Scherzer: %s", body)
	}
	if !strings.Contains(body, "Deduct:") {
		t.Errorf("body missing Deduct label: %s", body)
	}
	// Team Beta has no deductions — should not show "Deduct" for them.
	if !strings.Contains(body, "Team Beta") || !strings.Contains(body, "13 GS") || !strings.Contains(body, "+1") {
		t.Errorf("body missing Team Beta details: %s", body)
	}
	if !strings.Contains(body, "Over Max (12)") {
		t.Errorf("body missing over max section: %s", body)
	}
}

func TestBuildReport_MinViolation(t *testing.T) {
	violations := []Violation{
		{TeamName: "Slackers", GSUsed: 5, Kind: ViolationMin},
	}

	_, body := BuildReport(violations, "Period 1", 12, 7)
	if !strings.Contains(body, "Slackers") || !strings.Contains(body, "5 GS") || !strings.Contains(body, "-2") {
		t.Errorf("body missing min violation: %s", body)
	}
	if !strings.Contains(body, "Under Min (7)") {
		t.Errorf("body missing under min section: %s", body)
	}
}

func TestBuildReport_SingleViolation(t *testing.T) {
	violations := []Violation{
		{TeamName: "Violators", GSUsed: 15, Kind: ViolationMax},
	}

	_, body := BuildReport(violations, "Period 1", 10, 0)
	if !strings.Contains(body, "1 violation") {
		t.Errorf("body should show 1 violation: %s", body)
	}
}

func TestBuildReport_MixedViolations(t *testing.T) {
	violations := []Violation{
		{TeamName: "Over Team", GSUsed: 15, Kind: ViolationMax},
		{TeamName: "Under Team", GSUsed: 5, Kind: ViolationMin},
	}

	_, body := BuildReport(violations, "Period 1", 12, 10)
	if !strings.Contains(body, "Over Max (12)") {
		t.Errorf("body missing over section: %s", body)
	}
	if !strings.Contains(body, "Under Min (10)") {
		t.Errorf("body missing under section: %s", body)
	}
}

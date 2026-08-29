package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func alarmEvent(name, state, desc, reason string) alarmDetail {
	var d alarmDetail
	d.AlarmName = name
	d.State.Value = state
	d.State.Reason = reason
	d.Configuration.Description = desc
	return d
}

func TestFormatAlarm_LeadsWithTheHumanDescriptionNotTheThresholdText(t *testing.T) {
	d := alarmEvent("rosterbot-cis-CloudWatch.1", "ALARM",
		"CIS CloudWatch.1: root user was used",
		"Threshold Crossed: 1 datapoint [1.0] was greater than the threshold (0.0).")

	title, body := formatAlarm(d)

	if !strings.Contains(title, "CloudWatch.1") {
		t.Errorf("title should name the control, got %q", title)
	}
	if strings.HasPrefix(body, "Threshold") {
		t.Errorf("body must lead with what happened, not CloudWatch's threshold text: %q", body)
	}
	if !strings.HasPrefix(body, "root user was used") {
		t.Errorf("body should lead with the description, got %q", body)
	}
	// The redundant "CIS CloudWatch.1: " lead-in is already in the title.
	if strings.Contains(body, "CIS CloudWatch.1:") {
		t.Errorf("body repeats the control id already in the title: %q", body)
	}
}

// A widened event pattern must not turn recovery notices into pushes: these
// alarms fire on something that already happened, so "OK" reports nothing.
func TestFormatAlarm_StaysQuietOnNonAlarmTransitions(t *testing.T) {
	for _, state := range []string{"OK", "INSUFFICIENT_DATA"} {
		title, body := formatAlarm(alarmEvent("rosterbot-cis-CloudWatch.1", state, "CIS CloudWatch.1: root user was used", "back to normal"))
		if title != "" || body != "" {
			t.Errorf("state %s should stay quiet, got title=%q body=%q", state, title, body)
		}
	}
}

// An alarm with no description still has to be reportable — a nameless push is
// worse than a vague one.
func TestFormatAlarm_SurvivesAMissingDescription(t *testing.T) {
	title, body := formatAlarm(alarmEvent("rosterbot-cis-CloudWatch.7", "ALARM", "", ""))
	if title == "" {
		t.Fatal("an ALARM transition must produce a title")
	}
	if body == "" {
		t.Error("body must not be empty")
	}
}

func TestAlarmControl_FallsBackToTheWholeNameWhenUnprefixed(t *testing.T) {
	if got := alarmEvent("rosterbot-cis-CloudWatch.3", "ALARM", "", "").control(); got != "CloudWatch.3" {
		t.Errorf("control() = %q, want CloudWatch.3", got)
	}
	if got := alarmEvent("some-other-alarm", "ALARM", "", "").control(); got != "some-other-alarm" {
		t.Errorf("unprefixed alarm should pass through, got %q", got)
	}
}

// cisAlarmPrefix is duplicated in infra/cloudtrail.go because the CDK is a
// separate Go module. Nothing but this test can notice if the two drift, and the
// symptom would be silent: the EventBridge rule would match alarms whose names
// this handler no longer recognises, so every push would arrive labelled with a
// raw alarm name instead of its control.
func TestCisAlarmPrefix_MatchesTheCDKThatNamesTheAlarms(t *testing.T) {
	src, err := os.ReadFile("../infra/cloudtrail.go")
	if err != nil {
		t.Skipf("infra/cloudtrail.go unreadable: %v", err)
	}
	m := regexp.MustCompile(`cisAlarmPrefix\s*=\s*"([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find cisAlarmPrefix in infra/cloudtrail.go")
	}
	if got := string(m[1]); got != cisAlarmPrefix {
		t.Errorf("prefix drift: infra has %q, opsnotify has %q", got, cisAlarmPrefix)
	}
}

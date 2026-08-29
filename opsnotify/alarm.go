package main

import (
	"context"
	"encoding/json"
	"strings"
)

// alarmDetailType is the EventBridge detail-type CloudWatch publishes on every
// alarm state transition. It must match the DetailType in the CisAlarmRule the
// CDK creates (infra/infra.go) — the two are joined by this string alone.
const alarmDetailType = "CloudWatch Alarm State Change"

// cisAlarmPrefix mirrors the constant of the same name in infra/cloudtrail.go.
// Duplicated rather than shared because the CDK is a separate Go module with no
// compiler link to here — the same arrangement documented on the tenant-roster
// prefix, and the same reason it is called out: nothing but a test can notice
// if the two drift.
const cisAlarmPrefix = "rosterbot-cis-"

// alarmDetail is the subset of a CloudWatch Alarm State Change event this
// handler reads. Hand-rolled because aws-lambda-go ships no type for it.
type alarmDetail struct {
	AlarmName string `json:"alarmName"`
	State     struct {
		Value     string `json:"value"`
		Reason    string `json:"reason"`
		Timestamp string `json:"timestamp"`
	} `json:"state"`
	Configuration struct {
		Description string `json:"description"`
	} `json:"configuration"`
}

// control returns the CIS control id ("CloudWatch.1") from the alarm name, or
// the whole name if it carries no recognised prefix — an unexpected alarm should
// still be reportable rather than arriving nameless.
func (d alarmDetail) control() string {
	return strings.TrimPrefix(d.AlarmName, cisAlarmPrefix)
}

// handleAlarm turns a CIS CloudTrail alarm into a Pushover push.
//
// Deduplication keys on (alarm, transition timestamp) rather than on the alarm
// alone. CloudWatch only emits on a state TRANSITION, so one breach produces one
// event and the timestamp names it; keying on the alarm name alone would mean a
// second, genuinely new breach of the same control weeks later found its marker
// already present and stayed silent forever. Same rule as the IL-start markers,
// where the start date is in the key for exactly this reason.
func handleAlarm(ctx context.Context, raw json.RawMessage) error {
	var d alarmDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	title, body := formatAlarm(d)
	return sendOnce(ctx, markers, alert{
		key:   "alarm/" + d.AlarmName + "/" + d.State.Timestamp,
		note:  d.State.Value,
		title: title,
		body:  body,
	})
}

// formatAlarm renders the Pushover (title, body). Pure, so it is unit-tested
// directly.
//
// A non-ALARM transition returns an empty title — the codebase's "stay quiet"
// signal, which send1 honours. The CisAlarmRule already filters to ALARM, so
// this is belt-and-braces; it exists because widening that event pattern is a
// one-line change that would otherwise start paging an OK notice for an event
// that has no recovery.
func formatAlarm(d alarmDetail) (title, body string) {
	if d.State.Value != "ALARM" {
		return "", ""
	}

	title = "🔐 Rosterbot security: " + d.control()

	// The description is ours (the CDK writes "CIS CloudWatch.1: root user was
	// used"), so it is the sentence a half-awake operator can act on. The
	// reason is CloudWatch's own threshold text and is appended, not led with,
	// because it says "Threshold Crossed" rather than what happened.
	desc := strings.TrimSpace(d.Configuration.Description)
	if i := strings.Index(desc, ": "); i >= 0 {
		desc = desc[i+2:] // drop the redundant "CIS CloudWatch.N: " lead-in
	}
	if desc == "" {
		desc = "alarm entered ALARM state"
	}

	body = desc
	if r := strings.TrimSpace(d.State.Reason); r != "" {
		body += " · " + r
	}
	return title, body
}

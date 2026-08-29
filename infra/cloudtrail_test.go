package main

import (
	"strings"
	"testing"
)

// The whole point of the paging split is that a `cdk deploy` must not page.
// These nine controls all fire on this repo's own CodeBuild deploys, so adding
// any of them to pages() would reproduce the alert flood the split exists to
// prevent — and it would do so silently, one push per deploy, looking exactly
// like the feature working.
func TestPagingSet_ExcludesEveryDeployDrivenControl(t *testing.T) {
	deployDriven := []string{
		"CloudWatch.4",  // IAM policy changes — every role/policy the CDK touches
		"CloudWatch.5",  // CloudTrail config — this stack's own first deploy
		"CloudWatch.8",  // S3 bucket policy
		"CloudWatch.9",  // AWS Config
		"CloudWatch.10", // security groups
		"CloudWatch.11", // NACLs
		"CloudWatch.12", // gateways
		"CloudWatch.13", // route tables
		"CloudWatch.14", // VPC
	}
	byControl := map[string]cisAlarm{}
	for _, s := range cisAlarmSpecs() {
		byControl[s.control] = s
	}
	for _, c := range deployDriven {
		spec, ok := byControl[c]
		if !ok {
			t.Fatalf("control %s missing from cisAlarmSpecs", c)
		}
		if spec.pages() {
			t.Errorf("%s pages, but it fires on our own deploys — see pages() doc", c)
		}
	}
}

func TestPagingSet_IsExactlyTheAnomalousFive(t *testing.T) {
	want := map[string]bool{
		"CloudWatch.1": true, "CloudWatch.2": true, "CloudWatch.3": true,
		"CloudWatch.6": true, "CloudWatch.7": true,
	}
	got := map[string]bool{}
	for _, s := range cisAlarmSpecs() {
		if s.pages() {
			got[s.control] = true
		}
	}
	if len(got) != len(want) {
		t.Errorf("paging set size = %d, want %d (got %v)", len(got), len(want), got)
	}
	for c := range want {
		if !got[c] {
			t.Errorf("%s should page but does not", c)
		}
	}
	for c := range got {
		if !want[c] {
			t.Errorf("%s pages unexpectedly", c)
		}
	}
}

// A duplicate construct id silently drops a resource (CDK overwrites), and a
// duplicate metric name silently merges two controls into one alarm — both fail
// as a missing check rather than an error.
func TestCisAlarmSpecs_AreWellFormed(t *testing.T) {
	specs := cisAlarmSpecs()
	if len(specs) != 14 {
		t.Errorf("got %d specs, want all 14 CIS CloudWatch controls", len(specs))
	}
	seen := map[string]string{}
	for _, s := range specs {
		for field, v := range map[string]string{"control": s.control, "id": s.id, "metric": s.metric} {
			if v == "" {
				t.Errorf("%s: empty %s", s.control, field)
				continue
			}
			k := field + "/" + v
			if prev, dup := seen[k]; dup {
				t.Errorf("duplicate %s %q on %s and %s", field, v, prev, s.control)
			}
			seen[k] = s.control
		}
		if !strings.HasPrefix(s.pattern, "{") || !strings.HasSuffix(s.pattern, "}") {
			t.Errorf("%s: filter pattern is not a CloudWatch JSON pattern: %q", s.control, s.pattern)
		}
		if s.desc == "" {
			t.Errorf("%s: empty description — it is the sentence the Pushover body leads with", s.control)
		}
	}
}

// The EventBridge rule matches on these exact strings; opsnotify trims the same
// prefix to recover the control id.
func TestCisAlarmName_IsPrefixedAndCarriesTheControl(t *testing.T) {
	for _, s := range cisAlarmSpecs() {
		name := s.alarmName()
		if !strings.HasPrefix(name, cisAlarmPrefix) {
			t.Errorf("%s: alarm name %q lacks the prefix the EventBridge rule matches", s.control, name)
		}
		if strings.TrimPrefix(name, cisAlarmPrefix) != s.control {
			t.Errorf("%s: alarm name %q does not round-trip to its control", s.control, name)
		}
	}
}

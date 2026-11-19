package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The task role is what the job's own code (and every dependency it loads,
// chromium included) can call AWS with. Secrets reach the container through
// the EXECUTION role, so the task role needs no secret read at all: the one
// runtime SSM read in the bot is the dashboard distribution id, a plain String
// parameter (cmd/sync.go dashboardCFDistID).
//
// Until this test the task role held ssm:GetParameter(s) on /rosterbot/*, which
// let any job process read the admin API token, the dashboard session secret,
// the Sleeper token and the APNs key. No statement in the stack may grant an
// SSM read on that wildcard again.
func TestNoRoleReadsTheWholeRosterbotParameterTree(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)

	sawDistID := false
	statements := 0
	for name, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::IAM::Policy" {
			continue
		}
		p, _ := res["Properties"].(map[string]any)
		doc, _ := p["PolicyDocument"].(map[string]any)
		stmts, _ := doc["Statement"].([]any)
		for _, s := range stmts {
			b, _ := json.Marshal(s)
			js := string(b)
			if !strings.Contains(js, "ssm:GetParameter") {
				continue
			}
			statements++
			if strings.Contains(js, "parameter/rosterbot/*") {
				t.Errorf("%s grants an SSM read on the whole /rosterbot/* tree: %s", name, js)
			}
			if strings.Contains(js, "parameter/rosterbot/DASHBOARD_CF_DIST_ID") {
				sawDistID = true
			}
		}
	}
	// Positive controls: a walk that matched nothing would pass the check
	// above for the wrong reason.
	if statements == 0 {
		t.Fatal("found no ssm:GetParameter statement at all; the walk is blind")
	}
	if !sawDistID {
		t.Error("no statement grants ssm:GetParameter on /rosterbot/DASHBOARD_CF_DIST_ID; " +
			"the site publish would lose its CloudFront invalidation")
	}
}

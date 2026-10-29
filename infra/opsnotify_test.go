package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// opsNotifyFnProps returns the Properties of the OpsNotify Lambda, found as the
// one function whose environment carries BUILD_PROJECT — the drift check's
// coordinate, set only under `-c enableBuild=true`, which infraTemplate passes.
func opsNotifyFnProps(t *testing.T) map[string]any {
	t.Helper()
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)
	var props map[string]any
	n := 0
	for _, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::Lambda::Function" {
			continue
		}
		p, _ := res["Properties"].(map[string]any)
		env, _ := p["Environment"].(map[string]any)
		vars, _ := env["Variables"].(map[string]any)
		if _, ok := vars["BUILD_PROJECT"]; !ok {
			continue
		}
		n++
		props = p
	}
	if n != 1 {
		t.Fatalf("template holds %d functions carrying BUILD_PROJECT, want exactly 1; "+
			"if 0, the drift check is no longer wired and these pins see nothing", n)
	}
	return props
}

// The drift check reads main's head from GitHub with a token once the
// repository is private: an anonymous read answers 404, and the check goes
// blind. The parameter NAME reaches the Lambda from the CDK beside GITHUB_REPO
// so the two cannot drift, and the role is granted exactly that one parameter.
func TestOpsNotify_ReadsTheGitHubTokenParameter(t *testing.T) {
	const param = "/rosterbot/GITHUB_READ_TOKEN"
	props := opsNotifyFnProps(t)
	env, _ := props["Environment"].(map[string]any)
	vars, _ := env["Variables"].(map[string]any)
	if got, _ := vars["GITHUB_TOKEN_PARAM"].(string); got != param {
		t.Errorf("GITHUB_TOKEN_PARAM = %q, want %q", got, param)
	}

	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)
	granted := false
	for _, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::IAM::Policy" {
			continue
		}
		p, _ := res["Properties"].(map[string]any)
		doc, _ := p["PolicyDocument"].(map[string]any)
		stmts, _ := doc["Statement"].([]any)
		for _, s := range stmts {
			b, _ := json.Marshal(s)
			if strings.Contains(string(b), "ssm:GetParameter") &&
				strings.Contains(string(b), "parameter"+param) {
				granted = true
			}
		}
	}
	if !granted {
		t.Errorf("no IAM statement grants ssm:GetParameter on %s; the Lambda would read it and be denied", param)
	}
}

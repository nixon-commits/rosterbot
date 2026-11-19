package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every behavior of every distribution carries a response headers policy
// (rosterbot-j05q). CloudFront attaches a policy PER BEHAVIOR, so a policy on
// the default behavior alone leaves the /v1/* API responses without headers,
// and a new behavior added later would ship bare. This walks all of them.
func TestEveryCloudFrontBehaviorSendsSecurityHeaders(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)

	dists, behaviors := 0, 0
	for name, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::CloudFront::Distribution" {
			continue
		}
		dists++
		props, _ := res["Properties"].(map[string]any)
		cfg, _ := props["DistributionConfig"].(map[string]any)
		all := []any{cfg["DefaultCacheBehavior"]}
		extra, _ := cfg["CacheBehaviors"].([]any)
		all = append(all, extra...)
		for _, b := range all {
			beh, _ := b.(map[string]any)
			behaviors++
			if beh["ResponseHeadersPolicyId"] == nil {
				t.Errorf("%s: behavior %v has no ResponseHeadersPolicyId", name, beh["PathPattern"])
			}
		}
	}
	// Positive controls: both distributions and the /v1/* behavior were seen.
	if dists != 2 || behaviors != 3 {
		t.Fatalf("walked %d distributions and %d behaviors, want 2 and 3; "+
			"update this test deliberately if a distribution or behavior was added", dists, behaviors)
	}
}

// The shipped policies carry the headers the bead asked for, and the
// dashboard's script-src stays free of 'unsafe-inline': that one token would
// make the content policy useless against injected script.
func TestSecurityHeaderPoliciesCarryTheHeaders(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)

	var policies []string
	for _, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::CloudFront::ResponseHeadersPolicy" {
			continue
		}
		b, _ := json.Marshal(res["Properties"])
		policies = append(policies, string(b))
	}
	if len(policies) != 2 {
		t.Fatalf("template holds %d response headers policies, want 2", len(policies))
	}
	for _, p := range policies {
		for _, want := range []string{
			"StrictTransportSecurity", "ContentTypeOptions", "ReferrerPolicy",
			`"FrameOption":"DENY"`, "frame-ancestors 'none'", "object-src 'none'",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("policy lacks %s: %s", want, p)
			}
		}
		if strings.Contains(p, `"Override":false`) {
			t.Errorf("a header does not override the origin: %s", p)
		}
	}

	scriptSrc := ""
	for _, d := range strings.Split(dashboardCSP, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			scriptSrc = d
		}
	}
	if scriptSrc != "script-src 'self'" {
		t.Errorf("dashboard script-src = %q, want exactly \"script-src 'self'\"", scriptSrc)
	}
}

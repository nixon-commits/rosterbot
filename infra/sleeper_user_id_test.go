package main

import "testing"

// TestSleeperUserID_IsADeployTimeParameterNotALaunchTimeSecret pins where
// SLEEPER_USER_ID lives. It is a public account id, and an ECS secret is
// resolved at EVERY task launch: wired as one on the shared task, a missing
// SSM parameter failed every scheduled job (ResourceInitializationError)
// rather than the one deploy. As a deploy-time String parameter a missing
// value fails `cdk deploy` before any task is touched. Nothing else notices
// the difference: both forms synthesize, and both reach the container.
func TestSleeperUserID_IsADeployTimeParameterNotALaunchTimeSecret(t *testing.T) {
	_, tmpl := infraTemplate(t)
	resources, _ := tmpl["Resources"].(map[string]any)

	var envRefs int
	for id, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::ECS::TaskDefinition" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		containers, _ := props["ContainerDefinitions"].([]any)
		for _, c := range containers {
			cd, _ := c.(map[string]any)
			secrets, _ := cd["Secrets"].([]any)
			for _, sec := range secrets {
				sm, _ := sec.(map[string]any)
				if sm["Name"] == "SLEEPER_USER_ID" {
					t.Errorf("%s: SLEEPER_USER_ID is an ECS Secret; a missing SSM parameter would fail every task launch on it", id)
				}
			}
			env, _ := cd["Environment"].([]any)
			for _, e := range env {
				em, _ := e.(map[string]any)
				if em["Name"] != "SLEEPER_USER_ID" {
					continue
				}
				val, _ := em["Value"].(map[string]any)
				ref, ok := val["Ref"].(string)
				if !ok {
					t.Errorf("%s: SLEEPER_USER_ID environment value is %v, want a {\"Ref\": <SSM-backed CFN parameter>}", id, em["Value"])
					continue
				}
				param, _ := tmpl["Parameters"].(map[string]any)
				pm, _ := param[ref].(map[string]any)
				if pm["Type"] != "AWS::SSM::Parameter::Value<String>" {
					t.Errorf("%s: SLEEPER_USER_ID resolves through parameter %s of type %v, want AWS::SSM::Parameter::Value<String> (read at deploy time)", id, ref, pm["Type"])
				}
				envRefs++
			}
		}
	}
	if envRefs == 0 {
		t.Fatal("no task definition carries SLEEPER_USER_ID in its environment; the multi-league football jobs fail fast without it")
	}
}

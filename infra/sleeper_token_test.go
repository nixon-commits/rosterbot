package main

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The logical ids CDK gives the two task definitions: construct id plus an
// 8-hex-digit hash. Anchored on both ends so "Task" cannot match OffersTask,
// TaskSg or any future resource that merely starts with it.
var (
	sharedTaskIDRe = regexp.MustCompile(`^Task[0-9A-F]{8}$`)
	offersTaskIDRe = regexp.MustCompile(`^OffersTask[0-9A-F]{8}$`)
)

// resourcesOfType returns the template's resources of one CloudFormation type,
// keyed by logical id.
func resourcesOfType(t *testing.T, tmpl map[string]any, typ string) map[string]map[string]any {
	t.Helper()
	resources, ok := tmpl["Resources"].(map[string]any)
	if !ok {
		t.Fatal("template has no Resources map")
	}
	out := map[string]map[string]any{}
	for id, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] == typ {
			out[id] = res
		}
	}
	return out
}

// containerSecretNames returns the names of every ECS secret on every
// container of a task definition resource.
func containerSecretNames(res map[string]any) map[string]bool {
	out := map[string]bool{}
	props, _ := res["Properties"].(map[string]any)
	containers, _ := props["ContainerDefinitions"].([]any)
	for _, c := range containers {
		cd, _ := c.(map[string]any)
		secrets, _ := cd["Secrets"].([]any)
		for _, s := range secrets {
			sm, _ := s.(map[string]any)
			if name, ok := sm["Name"].(string); ok {
				out[name] = true
			}
		}
	}
	return out
}

// ruleTaskDefRefs returns, for one EventBridge rule, the logical id each ECS
// target's TaskDefinitionArn points at (a {"Ref": id}). A rule with no ECS
// target (the per-tenant jobs aimed at the dispatcher Lambda, the two
// notification rules) returns nothing.
func ruleTaskDefRefs(t *testing.T, ruleID string, rule map[string]any) []string {
	t.Helper()
	props, _ := rule["Properties"].(map[string]any)
	targets, _ := props["Targets"].([]any)
	var refs []string
	for _, tg := range targets {
		target, _ := tg.(map[string]any)
		ecs, ok := target["EcsParameters"].(map[string]any)
		if !ok {
			continue
		}
		arn, _ := ecs["TaskDefinitionArn"].(map[string]any)
		ref, ok := arn["Ref"].(string)
		if !ok {
			t.Errorf("rule %s: ECS target's TaskDefinitionArn is %v, want a {\"Ref\": <task definition>}", ruleID, ecs["TaskDefinitionArn"])
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// TestSleeperToken_ReachesOnlyTheOffersTask pins the boundary the football-
// offers job exists to create. SLEEPER_TOKEN is the operator's Sleeper session
// token: account-scoped, full access, roughly a year's lifetime. It belongs on
// exactly one task definition (OffersTask) and exactly one schedule
// (FootballOffers) may launch it.
//
// Nothing else guards this. The jobs table, the season gate and the
// leagueWideJobs list all pass whether or not the token leaks, so without this
// test a later edit that adds SLEEPER_TOKEN to the shared container's Secrets
// would put it in the environment of every job -- the hourly lineup run, the
// API-launched ones, the per-tenant fan-out -- with every check green. The
// second half of the boundary is the wiring: a rename that desynchronizes the
// jobTaskDefs key from the schedule row id would silently launch FootballOffers
// on the shared task (which has no token) and the job would fail every run.
func TestSleeperToken_ReachesOnlyTheOffersTask(t *testing.T) {
	_, tmpl := infraTemplate(t)

	// 1. The token is on exactly one task definition, and it is OffersTask.
	taskDefs := resourcesOfType(t, tmpl, "AWS::ECS::TaskDefinition")
	var holders, sharedIDs, offersIDs []string
	for id, res := range taskDefs {
		if containerSecretNames(res)["SLEEPER_TOKEN"] {
			holders = append(holders, id)
		}
		switch {
		case sharedTaskIDRe.MatchString(id):
			sharedIDs = append(sharedIDs, id)
		case offersTaskIDRe.MatchString(id):
			offersIDs = append(offersIDs, id)
		}
	}
	sort.Strings(holders)
	if len(sharedIDs) != 1 || len(offersIDs) != 1 {
		// Both must be findable, or every assertion below is vacuous.
		t.Fatalf("want exactly one shared Task and one OffersTask task definition, got shared=%v offers=%v (all task definitions: %d)",
			sharedIDs, offersIDs, len(taskDefs))
	}
	sharedID, offersID := sharedIDs[0], offersIDs[0]
	if !reflect.DeepEqual(holders, []string{offersID}) {
		t.Errorf("SLEEPER_TOKEN is a secret on task definitions %v, want exactly [%s]. "+
			"The token grants full access to the operator's Sleeper account; every job "+
			"that runs on a task definition carrying it holds it in its environment.",
			holders, offersID)
	}
	if containerSecretNames(taskDefs[sharedID])["SLEEPER_TOKEN"] {
		t.Errorf("the shared %s task definition carries SLEEPER_TOKEN; it is the task every "+
			"other schedule and the API launch, and must not hold it", sharedID)
	}

	// 2. FootballOffers launches the OffersTask, on its own schedule, running
	// the football-offers command.
	rules := resourcesOfType(t, tmpl, "AWS::Events::Rule")
	var offersRuleIDs []string
	for id := range rules {
		if strings.HasPrefix(id, "FootballOffersRule") {
			offersRuleIDs = append(offersRuleIDs, id)
		}
	}
	if len(offersRuleIDs) != 1 {
		t.Fatalf("want exactly one FootballOffersRule*, got %v", offersRuleIDs)
	}
	offersRuleID := offersRuleIDs[0]
	offersRule := rules[offersRuleID]
	props, _ := offersRule["Properties"].(map[string]any)

	if got := props["ScheduleExpression"]; got != "cron(20 * * * ? *)" {
		t.Errorf("%s ScheduleExpression = %v, want cron(20 * * * ? *)", offersRuleID, got)
	}
	if refs := ruleTaskDefRefs(t, offersRuleID, offersRule); !reflect.DeepEqual(refs, []string{offersID}) {
		t.Errorf("%s targets task definition(s) %v, want exactly [%s]. A FootballOffers schedule "+
			"on any other task definition runs without the token and fails every run; check "+
			"that the jobTaskDefs key matches the schedule row's id.", offersRuleID, refs, offersID)
	}
	targets, _ := props["Targets"].([]any)
	if len(targets) != 1 {
		t.Fatalf("%s has %d targets, want 1", offersRuleID, len(targets))
	}
	target, _ := targets[0].(map[string]any)
	input, ok := target["Input"].(string)
	if !ok {
		t.Fatalf("%s target Input = %v, want the container-override JSON string", offersRuleID, target["Input"])
	}
	var override struct {
		ContainerOverrides []struct {
			Name    string   `json:"name"`
			Command []string `json:"command"`
		} `json:"containerOverrides"`
	}
	if err := json.Unmarshal([]byte(input), &override); err != nil {
		t.Fatalf("%s target Input is not the expected JSON: %v", offersRuleID, err)
	}
	if len(override.ContainerOverrides) != 1 ||
		override.ContainerOverrides[0].Name != "bot" ||
		!reflect.DeepEqual(override.ContainerOverrides[0].Command, []string{"football-offers"}) {
		t.Errorf("%s container override = %+v, want one override of container \"bot\" with command [football-offers]",
			offersRuleID, override.ContainerOverrides)
	}

	// 3. Every other rule that launches an ECS task launches the shared one, so
	// no other schedule can drift onto the token-bearing task definition.
	otherECSRules := 0
	for id, rule := range rules {
		if id == offersRuleID {
			continue
		}
		refs := ruleTaskDefRefs(t, id, rule)
		if len(refs) == 0 {
			continue
		}
		otherECSRules++
		for _, ref := range refs {
			if ref != sharedID {
				t.Errorf("rule %s launches task definition %s, want the shared %s; only "+
					"FootballOffers may run on the token-bearing task", id, ref, sharedID)
			}
		}
	}
	if otherECSRules == 0 {
		t.Error("found no other ECS-targeting rules; step 3 would pass vacuously")
	}
}

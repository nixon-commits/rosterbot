package ecsrun

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// captureAPI is a stub implementing the ecsrun.API seam. It records the
// *ecs.RunTaskInput it receives so the test can assert on the container
// override's environment mapping without touching AWS.
type captureAPI struct {
	input *ecs.RunTaskInput
	// out, if set, is returned instead of the default single-task success
	// response.
	out *ecs.RunTaskOutput
	err error
}

func (c *captureAPI) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	c.input = in
	if c.err != nil {
		return nil, c.err
	}
	if c.out != nil {
		return c.out, nil
	}
	arn := "arn:aws:ecs:us-west-1:123456789012:task/my-cluster/abc123taskid"
	return &ecs.RunTaskOutput{
		Tasks: []ecstypes.Task{{TaskArn: &arn}},
	}, nil
}

func newTestRunner(t *testing.T, api API) *Runner {
	t.Helper()
	t.Setenv("CLUSTER", "my-cluster")
	t.Setenv("TASK_DEF", "my-taskdef")
	t.Setenv("SUBNETS", "subnet-1,subnet-2")
	t.Setenv("SECURITY_GROUPS", "sg-1")
	t.Setenv("CONTAINER_NAME", "rosterbot")
	r, err := New(api)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// envMap flattens a RunTaskInput's single container override's environment
// into a map, failing the test if the shape isn't what Run/RunWithEnv build
// (exactly one container override).
func envMap(t *testing.T, in *ecs.RunTaskInput) map[string]string {
	t.Helper()
	if in == nil || in.Overrides == nil || len(in.Overrides.ContainerOverrides) != 1 {
		t.Fatalf("expected exactly one container override, got %#v", in)
	}
	out := map[string]string{}
	for _, kv := range in.Overrides.ContainerOverrides[0].Environment {
		if kv.Name == nil || kv.Value == nil {
			t.Fatalf("nil-valued env entry: %#v", kv)
		}
		out[*kv.Name] = *kv.Value
	}
	return out
}

// TestRun_NonEmptyCaller_SetsBothTenantVars pins the mapping the doc comment
// on Run promises: a non-empty caller becomes BOTH ROSTERBOT_USER_ID (which
// PerTenant S3 prefixes read) and RUN_USER_ID (which entrypoint.sh stamps on
// the run-ledger record). Losing either silently misattributes the run to
// the operator while still reporting success — the exact defect this
// parameter was added to close.
func TestRun_NonEmptyCaller_SetsBothTenantVars(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	if _, err := r.Run(context.Background(), lineupapi.UserID("tenant-abc"), []string{"optimize"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := envMap(t, api.input)
	if got := env["ROSTERBOT_USER_ID"]; got != "tenant-abc" {
		t.Errorf("ROSTERBOT_USER_ID = %q, want %q", got, "tenant-abc")
	}
	if got := env["RUN_USER_ID"]; got != "tenant-abc" {
		t.Errorf("RUN_USER_ID = %q, want %q", got, "tenant-abc")
	}
}

// TestRun_EmptyCaller_LeavesTenantVarsUnset pins the other half of Run's
// contract: an EMPTY caller (the bearer-token / break-glass path) must leave
// BOTH tenant variables unset so the task definition's own default
// (ROSTERBOT_USER_ID baked in at deploy time) applies. Setting either to the
// empty string would be a different bug — an explicit empty override is not
// the same as no override, and some consumers may treat "" as a real,
// if degenerate, tenant id rather than "value not set".
func TestRun_EmptyCaller_LeavesTenantVarsUnset(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	if _, err := r.Run(context.Background(), lineupapi.UserID(""), []string{"optimize"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := envMap(t, api.input)
	if v, ok := env["ROSTERBOT_USER_ID"]; ok {
		t.Errorf("ROSTERBOT_USER_ID should be unset for an empty caller, got %q", v)
	}
	if v, ok := env["RUN_USER_ID"]; ok {
		t.Errorf("RUN_USER_ID should be unset for an empty caller, got %q", v)
	}
}

// TestRun_SetsRunTriggerManual pins that Run (the user-initiated path) always
// stamps RUN_TRIGGER=manual, distinguishing it in the run ledger from the
// scheduled dispatcher's runs (see runledger-manual-runs-look-scheduled).
func TestRun_SetsRunTriggerManual(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	if _, err := r.Run(context.Background(), lineupapi.UserID("tenant-abc"), []string{"optimize"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	env := envMap(t, api.input)
	if got := env["RUN_TRIGGER"]; got != "manual" {
		t.Errorf("RUN_TRIGGER = %q, want %q", got, "manual")
	}
}

// TestRunWithEnv_DoesNotModifyCommand pins the constraint the doc comment on
// RunWithEnv calls out explicitly: the command slice must reach ECS verbatim,
// with no caller context appended, because internal/opsalert matches the
// launched command string against JOB_SCHEDULES exactly. Any mutation here
// would silently break Overdue/Streak alerting for that job with no error
// anywhere.
func TestRunWithEnv_DoesNotModifyCommand(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	command := []string{"optimize", "--dates", "2026-08-24"}
	if _, err := r.RunWithEnv(context.Background(), command, map[string]string{"RUN_TRIGGER": "schedule"}); err != nil {
		t.Fatalf("RunWithEnv: %v", err)
	}

	got := api.input.Overrides.ContainerOverrides[0].Command
	if len(got) != len(command) {
		t.Fatalf("command length changed: got %v, want %v", got, command)
	}
	for i := range command {
		if got[i] != command[i] {
			t.Errorf("command[%d] = %q, want %q", i, got[i], command[i])
		}
	}
}

// TestRunWithEnv_EnvironmentIsSortedByName pins determinism: the environment
// slice sent to ECS must be built in a stable order (sorted by variable
// name), not whatever order a Go map ranges in. An unsorted, map-ordered
// slice would make two calls with identical logical input produce different
// RunTaskInput values from run to run, which is exactly what the comment
// above the sort in RunWithEnv says the sort exists to prevent — and what
// would make this package untestable by simple equality.
func TestRunWithEnv_EnvironmentIsSortedByName(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	env := map[string]string{
		"ZEBRA":  "z",
		"ALPHA":  "a",
		"MIDDLE": "m",
	}
	if _, err := r.RunWithEnv(context.Background(), []string{"optimize"}, env); err != nil {
		t.Fatalf("RunWithEnv: %v", err)
	}

	kvs := api.input.Overrides.ContainerOverrides[0].Environment
	if len(kvs) != len(env) {
		t.Fatalf("got %d env entries, want %d", len(kvs), len(env))
	}
	names := make([]string, len(kvs))
	for i, kv := range kvs {
		names[i] = *kv.Name
	}
	want := []string{"ALPHA", "MIDDLE", "ZEBRA"}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("env order = %v, want alphabetical %v", names, want)
		}
	}

	// Run it again to confirm the ordering isn't an accident of Go's map
	// iteration randomization happening to agree once.
	api2 := &captureAPI{}
	r2 := newTestRunner(t, api2)
	if _, err := r2.RunWithEnv(context.Background(), []string{"optimize"}, env); err != nil {
		t.Fatalf("RunWithEnv (second call): %v", err)
	}
	names2 := make([]string, len(api2.input.Overrides.ContainerOverrides[0].Environment))
	for i, kv := range api2.input.Overrides.ContainerOverrides[0].Environment {
		names2[i] = *kv.Name
	}
	for i, n := range names2 {
		if n != names[i] {
			t.Fatalf("second call order = %v, differs from first call order = %v", names2, names)
		}
	}
}

// TestRunWithEnv_UsesTaskLaunchConfigurationFromEnvironment pins that the
// cluster, task definition, subnets, security groups and container name
// New() read from the process environment actually land on the RunTaskInput
// — the fields containerOverrides cannot touch (CLAUDE.md: ECS
// containerOverrides override environment, not the task definition's launch
// configuration).
func TestRunWithEnv_UsesTaskLaunchConfigurationFromEnvironment(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	if _, err := r.RunWithEnv(context.Background(), []string{"optimize"}, nil); err != nil {
		t.Fatalf("RunWithEnv: %v", err)
	}

	in := api.input
	if in.Cluster == nil || *in.Cluster != "my-cluster" {
		t.Errorf("Cluster = %v, want my-cluster", in.Cluster)
	}
	if in.TaskDefinition == nil || *in.TaskDefinition != "my-taskdef" {
		t.Errorf("TaskDefinition = %v, want my-taskdef", in.TaskDefinition)
	}
	if in.LaunchType != ecstypes.LaunchTypeFargate {
		t.Errorf("LaunchType = %v, want Fargate", in.LaunchType)
	}
	subnets := in.NetworkConfiguration.AwsvpcConfiguration.Subnets
	if len(subnets) != 2 || subnets[0] != "subnet-1" || subnets[1] != "subnet-2" {
		t.Errorf("Subnets = %v, want [subnet-1 subnet-2]", subnets)
	}
	groups := in.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups
	if len(groups) != 1 || groups[0] != "sg-1" {
		t.Errorf("SecurityGroups = %v, want [sg-1]", groups)
	}
	if in.Overrides.ContainerOverrides[0].Name == nil || *in.Overrides.ContainerOverrides[0].Name != "rosterbot" {
		t.Errorf("container override Name = %v, want rosterbot", in.Overrides.ContainerOverrides[0].Name)
	}
}

// TestRunWithEnv_EmptyCommand_ReturnsErrorWithoutCallingAPI pins the guard
// clause: an empty command must fail fast rather than reach ECS at all
// (there is nothing sensible to run, and RunTask has real-world side
// effects it's worth not triggering on a caller bug).
func TestRunWithEnv_EmptyCommand_ReturnsErrorWithoutCallingAPI(t *testing.T) {
	api := &captureAPI{}
	r := newTestRunner(t, api)

	if _, err := r.RunWithEnv(context.Background(), nil, nil); err == nil {
		t.Fatal("expected an error for an empty command")
	}
	if api.input != nil {
		t.Error("RunTask should not have been called for an empty command")
	}
}

// TestRunWithEnv_ExtractsTaskIDFromARN pins the id-parsing behavior the
// caller relies on: the returned id is the last path segment of the task
// ARN, not the full ARN.
func TestRunWithEnv_ExtractsTaskIDFromARN(t *testing.T) {
	arn := "arn:aws:ecs:us-west-1:123456789012:task/my-cluster/abc123taskid"
	api := &captureAPI{out: &ecs.RunTaskOutput{Tasks: []ecstypes.Task{{TaskArn: &arn}}}}
	r := newTestRunner(t, api)

	id, err := r.RunWithEnv(context.Background(), []string{"optimize"}, nil)
	if err != nil {
		t.Fatalf("RunWithEnv: %v", err)
	}
	if id != "abc123taskid" {
		t.Errorf("id = %q, want %q", id, "abc123taskid")
	}
}

// TestRunWithEnv_FailurePropagatesReason pins that an ECS-reported task
// launch failure (as opposed to a transport error) surfaces its Reason to
// the caller rather than a generic "no task" message — the reason is the
// only clue an operator gets for why a manually-triggered job never started.
func TestRunWithEnv_FailurePropagatesReason(t *testing.T) {
	reason := "RESOURCE:FARGATE"
	api := &captureAPI{out: &ecs.RunTaskOutput{
		Failures: []ecstypes.Failure{{Reason: &reason}},
	}}
	r := newTestRunner(t, api)

	_, err := r.RunWithEnv(context.Background(), []string{"optimize"}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != "run task failed: RESOURCE:FARGATE" {
		t.Errorf("error = %q, want it to contain the failure reason", got)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"":                nil,
		"  ":              nil,
		"a":               {"a"},
		"a,b":             {"a", "b"},
		"a, b ,, c":       {"a", "b", "c"},
		" subnet-1 ,sg-1": {"subnet-1", "sg-1"},
	}
	for in, want := range cases {
		got := SplitCSV(in)
		if len(got) != len(want) {
			t.Errorf("SplitCSV(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("SplitCSV(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

// TestNew_MissingRequiredEnv_Errors pins that New refuses to construct a
// Runner missing any of its four required launch-configuration variables,
// rather than launching tasks with a zero-value cluster/task-def/subnet
// silently.
func TestNew_MissingRequiredEnv_Errors(t *testing.T) {
	for _, missing := range []string{"CLUSTER", "TASK_DEF", "SUBNETS", "CONTAINER_NAME"} {
		t.Run(missing, func(t *testing.T) {
			env := map[string]string{
				"CLUSTER":         "my-cluster",
				"TASK_DEF":        "my-taskdef",
				"SUBNETS":         "subnet-1",
				"CONTAINER_NAME":  "rosterbot",
				"SECURITY_GROUPS": "sg-1",
			}
			for k, v := range env {
				if k == missing {
					continue
				}
				t.Setenv(k, v)
			}
			t.Setenv(missing, "")
			// os.Unsetenv covers the "never set at all" case identically to
			// an empty string, since New treats "" as absent for all four.
			_ = os.Unsetenv(missing)

			if _, err := New(&captureAPI{}); err == nil {
				t.Errorf("expected New to error with %s missing", missing)
			}
		})
	}
}

// Package ecsrun launches rosterbot jobs as Fargate tasks.
//
// It is shared by the two Lambda entry points in this module: the API function,
// which launches a job a user asked for, and the dispatcher, which launches one
// task per tenant on a schedule. They differ only in what they put in the task's
// environment, so the RunTask call itself lives here rather than being copied —
// the second copy is where the two would quietly stop agreeing about which
// cluster, subnets or launch type a job runs with.
package ecsrun

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// API is the slice of the ECS client this package uses, so callers can test
// against a double.
type API interface {
	RunTask(context.Context, *ecs.RunTaskInput, ...func(*ecs.Options)) (*ecs.RunTaskOutput, error)
}

// Runner launches a job as a Fargate task using the same task definition the
// EventBridge schedules use, with the command overridden.
type Runner struct {
	client    API
	cluster   string
	taskDef   string
	subnets   []string
	groups    []string
	container string
}

// New reads the task's launch configuration from the environment.
func New(client API) (*Runner, error) {
	r := &Runner{
		client:    client,
		cluster:   os.Getenv("CLUSTER"),
		taskDef:   os.Getenv("TASK_DEF"),
		subnets:   SplitCSV(os.Getenv("SUBNETS")),
		groups:    SplitCSV(os.Getenv("SECURITY_GROUPS")),
		container: os.Getenv("CONTAINER_NAME"),
	}
	if r.cluster == "" || r.taskDef == "" || len(r.subnets) == 0 || r.container == "" {
		return nil, fmt.Errorf("ecs runner misconfigured: need CLUSTER, TASK_DEF, SUBNETS, CONTAINER_NAME")
	}
	return r, nil
}

// Run launches a user-initiated job AS THE CALLER. Implements
// lineupapi.JobRunner.
//
// It sets the same two tenant variables the scheduled dispatcher does, and for
// the same reasons: ROSTERBOT_USER_ID decides the S3 prefixes every PerTenant
// artifact is written under, and RUN_USER_ID is what entrypoint.sh stamps on
// the run-ledger record. Setting neither was the critical defect this
// parameter exists to close — ROSTERBOT_USER_ID lives on the shared task
// definition, so an omitted override silently resolved to the operator, and a
// member's job ran against the operator's credentials, roster and auto_apply
// while reporting success.
//
// An EMPTY caller is the bearer-token path, which has no UserID by
// construction. Both variables are then left unset so the task definition's
// default applies — that is the deployment's own tenant, which is the right
// answer for break-glass access and is decided here, once, rather than by every
// call site.
func (r *Runner) Run(ctx context.Context, caller lineupapi.UserID, command []string) (string, error) {
	env := map[string]string{"RUN_TRIGGER": "manual"}
	if caller != "" {
		env["ROSTERBOT_USER_ID"] = string(caller)
		env["RUN_USER_ID"] = string(caller)
	}
	return r.RunWithEnv(ctx, command, env)
}

// RunWithEnv launches a job with extra container environment variables.
//
// THE COMMAND IS NEVER MODIFIED TO CARRY CALLER CONTEXT, and that constraint is
// why this takes an env map at all. infra.go builds JOB_SCHEDULES by
// space-joining each job's argv, entrypoint.sh reports the run as CMD="$*", and
// internal/opsalert looks the schedule up by that exact string. Appending so
// much as a --user flag makes the joined command stop matching its
// JOB_SCHEDULES entry, and every Overdue and Streak assertion for that job
// silently stops firing — an alerting outage with no error anywhere.
func (r *Runner) RunWithEnv(ctx context.Context, command []string, env map[string]string) (string, error) {
	if len(command) == 0 {
		return "", fmt.Errorf("ecsrun: empty command")
	}

	// Sorted so the request is identical for identical input, which is what
	// makes it assertable in a test.
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	kvs := make([]ecstypes.KeyValuePair, 0, len(names))
	for _, name := range names {
		kvs = append(kvs, ecstypes.KeyValuePair{Name: strPtr(name), Value: strPtr(env[name])})
	}

	out, err := r.client.RunTask(ctx, &ecs.RunTaskInput{
		Cluster:        &r.cluster,
		TaskDefinition: &r.taskDef,
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        r.subnets,
				SecurityGroups: r.groups,
				AssignPublicIp: ecstypes.AssignPublicIpEnabled,
			},
		},
		Overrides: &ecstypes.TaskOverride{
			ContainerOverrides: []ecstypes.ContainerOverride{{
				Name:        &r.container,
				Command:     command,
				Environment: kvs,
			}},
		},
	})
	if err != nil {
		return "", err
	}
	if len(out.Tasks) == 0 || out.Tasks[0].TaskArn == nil {
		if len(out.Failures) > 0 && out.Failures[0].Reason != nil {
			return "", fmt.Errorf("run task failed: %v", *out.Failures[0].Reason)
		}
		return "", fmt.Errorf("run task returned no task")
	}
	// id = last segment of arn:aws:ecs:...:task/<cluster>/<taskId>
	arn := *out.Tasks[0].TaskArn
	return arn[strings.LastIndex(arn, "/")+1:], nil
}

// SplitCSV splits a comma-separated env var, dropping empties.
func SplitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func strPtr(s string) *string { return &s }

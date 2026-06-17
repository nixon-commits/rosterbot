package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ecsRunner launches a job as a Fargate task (the same task definition the
// EventBridge schedules use), with the command overridden and RUN_TRIGGER set to
// "manual" so the run ledger tags it as user-initiated. Implements
// lineupapi.JobRunner.
type ecsRunner struct {
	client    *ecs.Client
	cluster   string
	taskDef   string
	subnets   []string
	groups    []string
	container string
}

func newECSRunner(client *ecs.Client) (*ecsRunner, error) {
	r := &ecsRunner{
		client:    client,
		cluster:   os.Getenv("CLUSTER"),
		taskDef:   os.Getenv("TASK_DEF"),
		subnets:   splitCSV(os.Getenv("SUBNETS")),
		groups:    splitCSV(os.Getenv("SECURITY_GROUPS")),
		container: os.Getenv("CONTAINER_NAME"),
	}
	if r.cluster == "" || r.taskDef == "" || len(r.subnets) == 0 || r.container == "" {
		return nil, fmt.Errorf("ecs runner misconfigured: need CLUSTER, TASK_DEF, SUBNETS, CONTAINER_NAME")
	}
	return r, nil
}

func (r *ecsRunner) Run(ctx context.Context, command []string) (string, error) {
	cmd := make([]*string, len(command))
	for i := range command {
		cmd[i] = &command[i]
	}
	trigKey, trigVal := "RUN_TRIGGER", "manual"

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
				Environment: []ecstypes.KeyValuePair{{Name: &trigKey, Value: &trigVal}},
			}},
		},
	})
	if err != nil {
		return "", err
	}
	if len(out.Tasks) == 0 || out.Tasks[0].TaskArn == nil {
		if len(out.Failures) > 0 {
			return "", fmt.Errorf("run task failed: %v", *out.Failures[0].Reason)
		}
		return "", fmt.Errorf("run task returned no task")
	}
	// id = last segment of arn:aws:ecs:...:task/<cluster>/<taskId>
	arn := *out.Tasks[0].TaskArn
	return arn[strings.LastIndex(arn, "/")+1:], nil
}

func splitCSV(s string) []string {
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

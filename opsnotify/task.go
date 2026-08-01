package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// botContainer is the container name the task definition and every EventBridge
// target override use.
const botContainer = "bot"

// ecsTaskDetail is the subset of an "ECS Task State Change" detail body this
// handler reads. aws-lambda-go ships no type for this event, so it is declared
// here.
type ecsTaskDetail struct {
	ClusterArn    string `json:"clusterArn"`
	TaskArn       string `json:"taskArn"`
	LastStatus    string `json:"lastStatus"`
	StoppedReason string `json:"stoppedReason"`
	Containers    []struct {
		Name     string `json:"name"`
		ExitCode *int   `json:"exitCode"`
	} `json:"containers"`
	Overrides struct {
		ContainerOverrides []struct {
			Name    string   `json:"name"`
			Command []string `json:"command"`
		} `json:"containerOverrides"`
	} `json:"overrides"`
}

// failed reports whether this stopped task is a failure. The judgement lives in
// Go rather than in the EventBridge event pattern: patterns cannot express
// "exit code absent OR non-zero" over an array of objects without subtlety, and
// a table-testable function is worth ~700 extra invocations a month.
//
// A task with no containers at all never placed, which is a failure — not
// vacuously "everything exited zero".
func (d ecsTaskDetail) failed() bool {
	if len(d.Containers) == 0 {
		return true
	}
	for _, c := range d.Containers {
		if c.ExitCode == nil || *c.ExitCode != 0 {
			return true
		}
	}
	return false
}

// taskID is the ledger's run id: the last ARN segment, matching how
// entrypoint.sh's run_id() derives it from the task metadata endpoint.
func (d ecsTaskDetail) taskID() string {
	if i := strings.LastIndex(d.TaskArn, "/"); i >= 0 {
		return d.TaskArn[i+1:]
	}
	return d.TaskArn
}

// command is the joined container command override, which is exactly the string
// entrypoint.sh passes to `run-ledger --command` — so it is the key the two
// sides agree on. Empty means the task carried no override and is not ours.
func (d ecsTaskDetail) command() string {
	for _, o := range d.Overrides.ContainerOverrides {
		if o.Name == botContainer && len(o.Command) > 0 {
			return strings.Join(o.Command, " ")
		}
	}
	for _, o := range d.Overrides.ContainerOverrides {
		if len(o.Command) > 0 {
			return strings.Join(o.Command, " ")
		}
	}
	return ""
}

// handleTask turns an ECS Task State Change into at most one Pushover.
func handleTask(ctx context.Context, detail json.RawMessage) error {
	var d ecsTaskDetail
	if err := json.Unmarshal(detail, &d); err != nil {
		return err
	}
	if d.LastStatus != "STOPPED" {
		return nil
	}
	command := d.command()
	if command == "" {
		log.Printf("task %s has no container command override; ignoring", d.taskID())
		return nil
	}
	if ledger == nil {
		log.Printf("no ledger reader configured (STATE_BUCKET unset); ignoring task %s", d.taskID())
		return nil
	}

	recs, err := ledger.recent(ctx, ledgerWindow)
	if err != nil {
		return err
	}

	// No ledger record for this task means the entrypoint never reached its
	// final run-ledger write. No streak is computable, and this class of
	// failure is severe and rare, so it always alerts.
	if d.failed() && !hasID(recs, d.taskID()) {
		title, body := opsalert.FormatCrash(command, d.taskID(), d.StoppedReason)
		return sendOrLog(title, body)
	}

	title, body := opsalert.FormatTask(opsalert.Streak(recs, command))
	return sendOrLog(title, body)
}

func hasID(recs []opsalert.Record, id string) bool {
	for _, r := range recs {
		if r.ID == id {
			return true
		}
	}
	return false
}

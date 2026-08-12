package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	Version       int    `json:"version"`
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

// dedupeKey identifies the at-most-one alert this task is allowed to produce.
//
// The task id alone, deliberately NOT id+detail.version. Version is what AWS
// documents for de-duplicating an ECS event, and keying on it would satisfy the
// letter of "a duplicate delivery produces one Pushover" — but it would miss the
// neighbouring case, because ECS can emit several distinct STOPPED events for
// one task (different versions, same terminal outcome) as container-level state
// settles. Those are not duplicate deliveries and version would let each of them
// through, yet they describe the same dead run and deserve one alert between
// them. Keying on the task is strictly stronger and matches what handleTask
// already promises: at most one Pushover per stopped task. Version is kept on
// the struct and written into the marker body, where it dates the delivery that
// won without weakening the key.
//
// This needs no tenant dimension, unlike the heartbeat's per-job MarkerKey.
// Fan-out runs one ECS task per tenant per job, so distinct tenants already
// yield distinct task ids and cannot suppress each other here. That holds only
// while the key stays derived from the task id, which is why
// TestEcsTaskDetail_DedupeKeyIsPerTaskAndThereforePerTenant pins it.
func (d ecsTaskDetail) dedupeKey() string {
	return "task/" + d.taskID()
}

// alert pairs a rendered message with this task's one-shot identity.
func (d ecsTaskDetail) alert(title, body string) alert {
	return alert{
		key:   d.dedupeKey(),
		note:  fmt.Sprintf("version=%d lastStatus=%s", d.Version, d.LastStatus),
		title: title,
		body:  body,
	}
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
		log.Printf("task %s has no container command override; ignoring", logSafe(d.taskID()))
		return nil
	}
	if ledger == nil {
		log.Printf("no ledger reader configured (STATE_BUCKET unset); ignoring task %s", logSafe(d.taskID()))
		return nil
	}

	recs, err := ledger.recent(ctx, ledgerWindow)
	if err != nil {
		return err
	}

	// entrypoint.sh writes a RUNNING record at the start of a run and
	// overwrites the very same ledger key with the terminal SUCCESS/FAILED
	// record when it finishes (both writes carry the same --id and
	// --started, and lineupapi.RunKey derives the storage key from exactly
	// those two). A healthy run therefore always leaves exactly one record
	// behind — a RUNNING record is what a run in progress looks like, not
	// what a finished run looks like. A task that stopped without a
	// *terminal* record for its own id means the entrypoint never reached
	// that second write: OOM, SIGKILL to pid 1, or — when there is no record
	// at all — an image-pull failure that never ran the entrypoint in the
	// first place. Those two outcomes are not the same and must not share a
	// branch:
	own, ok := terminalRecord(recs, d.taskID())
	if !ok {
		if d.failed() {
			// No streak is computable for a run the ledger never finished
			// describing, and this class of failure is severe and rare, so
			// it always alerts — once per task, see dedupeKey.
			title, body := opsalert.FormatCrash(command, d.taskID(), d.StoppedReason)
			return sendOnce(ctx, markers, d.alert(title, body))
		}
		// Succeeded, but the ledger write was lost (the entrypoint's final
		// run-ledger call is best-effort — `|| true` — so this does happen).
		// The history no longer describes this run either way: falling
		// through to Streak here would judge this run's outcome by whatever
		// came before it, and if that history's most recent terminal record
		// happens to be FAILED, Streak would open a false "failed" streak on
		// a run that in fact succeeded. Silence is the only honest answer.
		log.Printf("task %s succeeded with no terminal ledger record; nothing to judge", logSafe(d.taskID()))
		return nil
	}

	// The verdict is a pure function of the ledger, and the ledger does not
	// change between two deliveries of the same event — so a repeat delivery
	// recomputes the identical verdict and would push it again. Streak
	// deduplicates across *runs*; only the marker deduplicates across
	// deliveries of one run.
	//
	// The tenant comes from this run's own ledger record, not from the ECS
	// event: the record is already in hand, and the event carries no tenant of
	// its own. It is empty before per-tenant fan-out, which Streak reads as the
	// single tenant the pre-fan-out ledger describes.
	title, body := opsalert.FormatTask(opsalert.Streak(recs, command, own.UserID))
	return sendOnce(ctx, markers, d.alert(title, body))
}

// terminalRecord returns the SUCCESS or FAILED record for id, and whether recs
// held one. The record is returned rather than just its existence because it
// carries the tenant the streak must be judged under.
//
// A RUNNING record must NOT count as terminal here. entrypoint.sh writes
// RUNNING at the start of a run and overwrites that same ledger key with the
// terminal SUCCESS/FAILED record when it finishes, so a healthy run leaves
// exactly one record behind and its status alone is what tells "still
// writing this row" apart from "wrote it, then the terminal write never
// landed". A run killed mid-flight leaves its RUNNING record sitting at that
// key forever: if RUNNING counted as terminal here, terminalRecord would
// report true, the crash branch above would never fire, and Streak — which
// itself filters RUNNING out when building its history — would have nothing
// to see either. That is exactly how this class of failure went unalerted in
// production: not "no record", but "a record that never became terminal".
func terminalRecord(recs []opsalert.Record, id string) (opsalert.Record, bool) {
	for _, r := range recs {
		if r.ID == id && (r.Status == opsalert.StatusSuccess || r.Status == opsalert.StatusFailed) {
			return r, true
		}
	}
	return opsalert.Record{}, false
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// NOTE: this file must NOT import internal/lineupapi, even in a test. `go mod
// tidy` in this module resolves the test imports of the module's own packages,
// so a test-only import of lineupapi would pull fantrax and chromedp into
// opsnotify/go.sum — the exact weight opsalert.Record exists to avoid. The
// fidelity of Record against the real ledger type is guarded instead by
// internal/opsalert/contract_test.go, which lives in the root module where that
// dependency is free.

// ecsDetail builds an ECS Task State Change detail body.
func ecsDetail(taskID, lastStatus, stoppedReason string, exitCode *int, command []string) json.RawMessage {
	container := map[string]any{"name": "bot", "lastStatus": lastStatus}
	if exitCode != nil {
		container["exitCode"] = *exitCode
	}
	d := map[string]any{
		"clusterArn":    "arn:aws:ecs:us-west-1:476646938644:cluster/InfraStack-Cluster",
		"taskArn":       "arn:aws:ecs:us-west-1:476646938644:task/InfraStack-Cluster/" + taskID,
		"lastStatus":    lastStatus,
		"stoppedReason": stoppedReason,
		"containers":    []any{container},
		"overrides": map[string]any{
			"containerOverrides": []any{map[string]any{"name": "bot", "command": command}},
		},
	}
	b, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	return b
}

// fakeLedger installs a ledgerReader backed by an in-memory S3, seeded with the
// given records newest-first, and restores the previous reader afterwards.
func fakeLedger(t *testing.T, recs []opsalert.Record) {
	t.Helper()
	objects := map[string][]byte{}
	for i, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		// Ledger keys sort newest-first ascending, so an increasing index
		// reproduces the real inverted-timestamp ordering.
		objects[fmt.Sprintf("runledger/%010d-%s.json", i, r.ID)] = b
	}
	fake := s3blobtest.With(objects)
	prev := ledger
	ledger = &ledgerReader{blob: fake.Blob("state", "runledger/")}
	t.Cleanup(func() { ledger = prev })
}

func run(id, command, status string, exit int) opsalert.Record {
	r := opsalert.Record{ID: id, Command: command, Status: status}
	if status != opsalert.StatusRunning {
		e := exit
		r.ExitCode = &e
	}
	if status == opsalert.StatusFailed {
		r.LogTail = "fantrax API error STALE_CLIENT: outdated cached version"
	}
	return r
}

const optimizeCmd = "optimize --matchup --archive-projections"

func TestHandleTask_FirstFailureAlerts(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	one := 1
	detail := ecsDetail("task1", "STOPPED", "Essential container in task exited", &one,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	msg := (*got)[0]
	for _, want := range []string{"optimize failed", "exit 1", "STALE_CLIENT"} {
		if !strings.Contains(msg, want) {
			t.Errorf("send %q missing %q", msg, want)
		}
	}
}

func TestHandleTask_SecondFailureIsSilent(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task2", optimizeCmd, opsalert.StatusFailed, 1),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	one := 1
	if err := handleTask(context.Background(),
		ecsDetail("task2", "STOPPED", "", &one, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0: %v", len(*got), *got)
	}
}

func TestHandleTask_SuccessAfterFailuresRecovers(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task3", optimizeCmd, opsalert.StatusSuccess, 0),
		run("task2", optimizeCmd, opsalert.StatusFailed, 1),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	zero := 0
	if err := handleTask(context.Background(),
		ecsDetail("task3", "STOPPED", "", &zero, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "recovered after 2 failures") {
		t.Errorf("send = %q", (*got)[0])
	}
}

// This is what production actually looks like for an OOM kill:
// entrypoint.sh's start-of-run write left a RUNNING record behind at the same
// ledger key the terminal write would have overwritten, and the container
// died before it could ever make that terminal write. No streak is
// computable — the ledger's most recent word on this run is "still running",
// which is not a status this run will ever update — so it always alerts.
//
// This fixture used to omit the RUNNING record entirely, which only matches
// an image-pull failure (see TestHandleTask_ImagePullFailureAlertsUnconditionally
// below) — the entrypoint never runs in that case, so there is truly no
// record of any kind. A stopped task whose RUNNING record survives is the
// far more common crash shape, and the one the ledger-lookup guard must
// still catch.
func TestHandleTask_OOMWithSurvivingRunningRecordAlerts(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("ghost", optimizeCmd, opsalert.StatusRunning, 0),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	// OOM kills leave no exit code at all — the container is killed out from
	// under the process, not given the chance to exit.
	detail := ecsDetail("ghost", "STOPPED", "OutOfMemoryError: Container killed", nil,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	for _, want := range []string{"died", "OutOfMemoryError", "ghost"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("send %q missing %q", (*got)[0], want)
		}
	}
}

// SIGKILL to pid 1 is the other common crash shape sharing the same signature
// as the OOM case: a surviving RUNNING record, but this time with a non-zero
// exit code rather than none at all.
func TestHandleTask_SIGKILLWithSurvivingRunningRecordAlerts(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("victim", optimizeCmd, opsalert.StatusRunning, 0),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	oneThirtySeven := 137
	detail := ecsDetail("victim", "STOPPED", "Essential container in task exited", &oneThirtySeven,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	for _, want := range []string{"died", "victim"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("send %q missing %q", (*got)[0], want)
		}
	}
}

// An image-pull failure never runs the entrypoint at all, so it is the one
// crash shape that genuinely leaves no ledger record of any kind — not even
// a RUNNING one. This must keep alerting unconditionally too.
func TestHandleTask_ImagePullFailureAlertsUnconditionally(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	detail := ecsDetail("imgpull", "STOPPED", "CannotPullContainerError: pull access denied", nil,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	for _, want := range []string{"died", "CannotPullContainerError", "imgpull"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("send %q missing %q", (*got)[0], want)
		}
	}
}

// entrypoint.sh's terminal run-ledger write is best-effort (`|| true`), so a
// run that actually succeeded can also end with only its RUNNING record
// surviving — the same ledger shape as a crash, but with exit code 0. Before
// the fix this fell through to Streak against whatever history preceded it,
// which here includes a prior FAILED run for the same command: Streak would
// read that as the newest terminal record and open a false "failed" streak
// on a run that in fact succeeded. There is nothing true to say about this
// run's outcome — the ledger simply doesn't describe it — so the right
// answer is silence, not a guess in either direction.
func TestHandleTask_LostTerminalWriteOnSuccessIsSilent(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task2", optimizeCmd, opsalert.StatusRunning, 0),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	zero := 0
	detail := ecsDetail("task2", "STOPPED", "", &zero, strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("got %d sends, want 0 (no false failed alert): %v", len(*got), *got)
	}
}

// The STATE_BUCKET-unset path: every other test in this file installs a fake
// ledger before calling handleTask, so this is the only coverage of what
// actually happens when main never wired one up. It must stay a quiet no-op,
// not a hard failure — CDK always sets STATE_BUCKET in production, and a hard
// init failure here would take CodeBuild build notifications down with it,
// since both event sources share this one Lambda.
func TestHandleTask_NilLedgerNoPanic(t *testing.T) {
	prev := ledger
	ledger = nil
	t.Cleanup(func() { ledger = prev })
	got := capture(t)

	one := 1
	detail := ecsDetail("task1", "STOPPED", "", &one, strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0: %v", len(*got), *got)
	}
}

// Non-terminal transitions arrive on the same rule; only STOPPED is actionable.
func TestHandleTask_IgnoresNonStoppedStates(t *testing.T) {
	fakeLedger(t, nil)
	got := capture(t)
	if err := handleTask(context.Background(),
		ecsDetail("task1", "RUNNING", "", nil, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0", len(*got))
	}
}

// A task with no container command override is not one of ours.
func TestHandleTask_IgnoresTasksWithNoCommand(t *testing.T) {
	fakeLedger(t, nil)
	got := capture(t)
	one := 1
	if err := handleTask(context.Background(),
		ecsDetail("task1", "STOPPED", "", &one, nil)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0", len(*got))
	}
}

func TestEcsTaskDetail_Failed(t *testing.T) {
	zero, one := 0, 1
	tests := []struct {
		name string
		exit *int
		want bool
	}{
		{"clean exit is not a failure", &zero, false},
		{"non-zero exit is a failure", &one, true},
		{"absent exit code means the container never ran", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d ecsTaskDetail
			if err := json.Unmarshal(ecsDetail("t", "STOPPED", "", tt.exit, []string{"grade"}), &d); err != nil {
				t.Fatal(err)
			}
			if got := d.failed(); got != tt.want {
				t.Errorf("failed() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A task that never placed has no containers at all — treat that as a failure
// rather than silently reading it as "everything exited zero".
func TestEcsTaskDetail_NoContainersIsAFailure(t *testing.T) {
	var d ecsTaskDetail
	if err := json.Unmarshal([]byte(`{"lastStatus":"STOPPED","containers":[]}`), &d); err != nil {
		t.Fatal(err)
	}
	if !d.failed() {
		t.Error("failed() = false for a task with no containers, want true")
	}
}

func TestEcsTaskDetail_TaskIDAndCommand(t *testing.T) {
	var d ecsTaskDetail
	one := 1
	if err := json.Unmarshal(ecsDetail("abc123", "STOPPED", "", &one, strings.Fields(optimizeCmd)), &d); err != nil {
		t.Fatal(err)
	}
	if got := d.taskID(); got != "abc123" {
		t.Errorf("taskID() = %q, want %q", got, "abc123")
	}
	if got := d.command(); got != optimizeCmd {
		t.Errorf("command() = %q, want %q", got, optimizeCmd)
	}
}

func TestLedgerReader_ReadsNewestFirst(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("newest", optimizeCmd, opsalert.StatusFailed, 1),
		run("middle", optimizeCmd, opsalert.StatusSuccess, 0),
		run("oldest", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	recs, err := ledger.recent(context.Background(), ledgerWindow)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	if recs[0].ID != "newest" {
		t.Errorf("recs[0].ID = %q, want %q", recs[0].ID, "newest")
	}
	if recs[0].LogTail == "" {
		t.Error("log tail was dropped in decoding")
	}
}

func TestLedgerReader_RespectsTheWindow(t *testing.T) {
	var recs []opsalert.Record
	for i := 0; i < 10; i++ {
		recs = append(recs, run(fmt.Sprintf("t%02d", i), optimizeCmd, opsalert.StatusSuccess, 0))
	}
	fakeLedger(t, recs)
	got, err := ledger.recent(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("got %d records, want 4", len(got))
	}
}

// --- Duplicate delivery (rosterbot-chs) ---

// EventBridge can deliver one event more than once, and async-invoke retries on
// a returned error. The verdict is a pure function of the ledger and the ledger
// does not move between deliveries, so without a marker the second delivery
// recomputes the identical alert and pushes it again.
func TestHandleTask_DuplicateDeliveryAlertsOnce(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)
	fakeMarkers(t)

	one := 1
	detail := ecsDetail("task1", "STOPPED", "", &one, strings.Fields(optimizeCmd))
	for i := 0; i < 3; i++ {
		if err := handleTask(context.Background(), detail); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends across 3 deliveries, want 1: %v", len(*got), *got)
	}
}

// The crash path (no terminal ledger record) has no streak to deduplicate it and
// always sends, so it needs the marker just as much.
func TestHandleTask_DuplicateDeliveryOfACrashAlertsOnce(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)
	fakeMarkers(t)

	detail := ecsDetail("task9", "STOPPED", "OutOfMemoryError: Container killed due to memory usage",
		nil, strings.Fields(optimizeCmd))
	for i := 0; i < 2; i++ {
		if err := handleTask(context.Background(), detail); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends across 2 deliveries, want 1: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "died") {
		t.Errorf("send = %q, want the crash alert", (*got)[0])
	}
}

// Deduplication is per task, so a genuinely different failed run still alerts —
// the marker must not become a mute on the command.
func TestHandleTask_ADifferentTaskStillAlerts(t *testing.T) {
	got := capture(t)
	fakeMarkers(t)
	one := 1

	fakeLedger(t, []opsalert.Record{
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	if err := handleTask(context.Background(),
		ecsDetail("task1", "STOPPED", "", &one, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}

	// A later, unrelated outage on the same command: one success in between
	// closes the first streak, so this is a fresh "Started".
	fakeLedger(t, []opsalert.Record{
		run("task3", optimizeCmd, opsalert.StatusFailed, 1),
		run("task2", optimizeCmd, opsalert.StatusSuccess, 0),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
	})
	if err := handleTask(context.Background(),
		ecsDetail("task3", "STOPPED", "", &one, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 2 {
		t.Fatalf("got %d sends, want 2 (one per outage): %v", len(*got), *got)
	}
}

// A silent verdict must not claim the task's marker: if the same task is later
// re-delivered in a state that does warrant an alert, the marker left by the
// quiet pass would suppress it.
func TestHandleTask_SilentVerdictLeavesTheMarkerUnclaimed(t *testing.T) {
	got := capture(t)
	fake := fakeMarkers(t)

	fakeLedger(t, []opsalert.Record{
		run("task2", optimizeCmd, opsalert.StatusFailed, 1),
		run("task1", optimizeCmd, opsalert.StatusFailed, 1),
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	one := 1
	if err := handleTask(context.Background(),
		ecsDetail("task2", "STOPPED", "", &one, strings.Fields(optimizeCmd))); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("got %d sends, want 0", len(*got))
	}
	if len(fake.Keys()) != 0 {
		t.Errorf("wrote markers %v for a silent verdict", fake.Keys())
	}
}

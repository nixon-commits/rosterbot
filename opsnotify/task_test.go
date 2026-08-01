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

// A task that stopped with no ledger record never reached the entrypoint's
// final run-ledger write: OOM, image-pull failure, SIGKILL to pid 1. No streak
// is computable, so it always alerts.
func TestHandleTask_NoLedgerRecordAlertsUnconditionally(t *testing.T) {
	fakeLedger(t, []opsalert.Record{
		run("task0", optimizeCmd, opsalert.StatusSuccess, 0),
	})
	got := capture(t)

	detail := ecsDetail("ghost", "STOPPED", "OutOfMemoryError: Container killed", nil,
		strings.Fields(optimizeCmd))
	if err := handleTask(context.Background(), detail); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	for _, want := range []string{"died", "no ledger record", "OutOfMemoryError", "ghost"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("send %q missing %q", (*got)[0], want)
		}
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

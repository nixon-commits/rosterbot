package opsalert_test

import (
	"encoding/json"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// opsalert.Record redeclares a subset of lineupapi.RunDetail rather than
// importing it, because lineupapi transitively pulls internal/fantrax (and
// therefore chromedp) into anything that imports it — unacceptable weight for
// a notification Lambda. This test is what makes that duplication safe: it
// fails the moment the two wire contracts drift.
func TestRecordDecodesARealLedgerRecord(t *testing.T) {
	exit := 1
	want := lineupapi.RunDetail{
		Run: lineupapi.Run{
			ID:        "abc123",
			Command:   "optimize --matchup --archive-projections",
			Status:    "FAILED",
			ExitCode:  &exit,
			StartedAt: "2026-07-01T17:00:53Z",
			EndedAt:   "2026-07-01T17:00:53Z",
			Trigger:   "schedule",
		},
		LogTail: "fantrax API error STALE_CLIENT",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	var got opsalert.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Command != want.Command {
		t.Errorf("Command = %q, want %q", got.Command, want.Command)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.ExitCode == nil || *got.ExitCode != exit {
		t.Errorf("ExitCode = %v, want %d", got.ExitCode, exit)
	}
	if got.LogTail != want.LogTail {
		t.Errorf("LogTail = %q, want %q", got.LogTail, want.LogTail)
	}
}

// A RUNNING record has no exit code and no log tail; decoding must leave them
// zero rather than erroring, because the streak logic sees these too.
func TestRecordDecodesARunningLedgerRecord(t *testing.T) {
	data, err := json.Marshal(lineupapi.RunDetail{
		Run: lineupapi.Run{ID: "x", Command: "grade", Status: "RUNNING", StartedAt: "2026-07-01T17:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got opsalert.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil", got.ExitCode)
	}
	if got.LogTail != "" {
		t.Errorf("LogTail = %q, want empty", got.LogTail)
	}
}

package lineupapi_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// opsalert.Record redeclares a subset of lineupapi.RunDetail rather than
// importing it, because lineupapi transitively pulls internal/fantrax (and
// therefore chromedp) into anything that imports it — unacceptable weight for
// a notification Lambda. This test is what makes that duplication safe: it
// fails the moment the two wire contracts drift.
//
// It lives here rather than in internal/opsalert so that opsalert itself never
// imports lineupapi, even in a test — opsnotify's `go mod tidy` resolves the
// test imports of the module's own packages, so a test-only import there would
// pull lineupapi -> fantrax -> chromedp into opsnotify/go.sum, the exact weight
// opsalert.Record exists to avoid.
func TestRecordDecodesARealLedgerRecord(t *testing.T) {
	exit := 1
	want := lineupapi.RunDetail{
		Run: lineupapi.Run{
			ID:        "abc123",
			Command:   "optimize --matchup --archive-projections",
			UserID:    "u-9f3a",
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
	// The heartbeat check reads this to answer "when did this job last launch?".
	// A drift here would not error — it would decode to the empty string, which
	// opsalert.Record.Started reads as "unknown" and Overdue then skips, so every
	// job would look permanently overdue while every test still passed.
	if got.StartedAt != want.StartedAt {
		t.Errorf("StartedAt = %q, want %q", got.StartedAt, want.StartedAt)
	}
	if !got.Started().Equal(time.Date(2026, 7, 1, 17, 0, 53, 0, time.UTC)) {
		t.Errorf("Started() = %v, want 2026-07-01T17:00:53Z", got.Started())
	}
	// Both opsalert decisions key on (command, user_id). A drift here would not
	// error either — it would decode to the empty string, which reads as the
	// pre-fan-out single tenant, so every tenant's runs would collapse into one
	// history: tenant B failing every hour graded healthy on tenant A's
	// successes, and one dark tenant invisible behind any sibling that ran.
	// Green tests, green dashboard, inverted alerting.
	if got.UserID != want.UserID {
		t.Errorf("UserID = %q, want %q", got.UserID, want.UserID)
	}
}

// TestRecordDecodesTheTenantActionableOutcome is the rosterbot-cnn9 half of
// this contract: opsalert.Streak keys its handling of a tenant-actionable
// exit-0 connect run on Record.Outcome, and the two packages' string
// constants (lineupapi.RunOutcomeTenantActionable, opsalert.OutcomeTenantActionable)
// are two independent literals with nothing but this test tying them
// together. A drift here would not error — it would decode to the empty
// string, which Streak reads as an ordinary SUCCESS, silently reinstating the
// false "recovered" verdict this bead exists to remove.
func TestRecordDecodesTheTenantActionableOutcome(t *testing.T) {
	want := lineupapi.RunDetail{
		Run: lineupapi.Run{
			ID:      "task-1",
			Command: "connect --user u1",
			Status:  "SUCCESS",
			Outcome: lineupapi.RunOutcomeTenantActionable,
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got opsalert.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Outcome != opsalert.OutcomeTenantActionable {
		t.Errorf("Outcome = %q, want %q", got.Outcome, opsalert.OutcomeTenantActionable)
	}
}

// A RUNNING record has no exit code and no log tail; decoding must leave them
// zero rather than erroring, because the streak logic sees these too.
//
// The same goes for user_id, which is absent from every record written before
// per-tenant fan-out: it must decode to the empty string, which both opsalert
// decisions treat as one ordinary tenant, so the existing ledger keeps grading
// as the single tenant it describes.
func TestRecordDecodesARunningLedgerRecord(t *testing.T) {
	data, err := json.Marshal(lineupapi.RunDetail{
		Run: lineupapi.Run{ID: "x", Command: "grade", Status: "RUNNING", StartedAt: "2026-07-01T17:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user_id") {
		t.Errorf("an untagged record serialized user_id: %s", data)
	}
	if strings.Contains(string(data), "outcome") {
		t.Errorf("a record with no outcome serialized the field anyway: %s", data)
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
	if got.UserID != "" {
		t.Errorf("UserID = %q, want empty", got.UserID)
	}
	if got.Outcome != "" {
		t.Errorf("Outcome = %q, want empty", got.Outcome)
	}
}

// TestRunKey_ShapeOpsnotifyKeysOn pins the WRITER half of a contract whose
// reader lives in another module.
//
// opsnotify/ledger.go decides what is a ledger record from the key's basename:
// a run of digits, a hyphen, then a non-empty id, then ".json". It has to use
// the shape rather than the key's depth, because the records moved under
// runledger/user=<uid>/ when layout.RunLedger became PerTenant and a
// depth-based filter then excluded every one of them — which froze the
// notifier for three days and produced 14 false "has not run" alerts
// (rosterbot-3vr).
//
// This cannot import opsnotify's predicate: opsnotify is package main in its
// own module, and the module boundary that keeps chromedp out of the Lambda is
// the same one that prevents a shared test. So the two sides are tied by a
// written-down shape rather than by the compiler, and this test is the half
// that fails if the writer moves. TestIsLedgerRecord in opsnotify is the other
// half. A genuinely mechanical link would need the predicate to live in a
// stdlib-only leaf both can import.
func TestRunKey_ShapeOpsnotifyKeysOn(t *testing.T) {
	for _, tc := range []struct{ name, started, id string }{
		{"an ECS task id", "2026-08-17T17:02:20Z", "837df59b0adb4bf6981f3c0243d4dcc4"},
		{"a local id, which itself contains hyphens", "2026-06-17T21:19:37Z", "local-20260617211937"},
		{"an unparseable start time still yields a well-formed key", "not-a-time", "abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := lineupapi.RunKey(tc.started, tc.id) + ".json"

			if !strings.HasSuffix(base, ".json") {
				t.Fatalf("key %q lost its .json suffix", base)
			}
			stamp, rest, ok := strings.Cut(strings.TrimSuffix(base, ".json"), "-")
			if !ok || stamp == "" || rest == "" {
				t.Fatalf("key %q is not <digits>-<id>.json; opsnotify would skip it", base)
			}
			if strings.TrimLeft(stamp, "0123456789") != "" {
				t.Errorf("timestamp segment %q of %q is not all digits; opsnotify would skip it", stamp, base)
			}
		})
	}
}

// TestRunKey_SortsReverseChronologically pins the property the whole "newest N"
// listing rests on: the inverted timestamp is ZERO-PADDED, so lexicographic
// order equals chronological order. Drop the padding and every ledger read
// silently returns the wrong records in the wrong order.
func TestRunKey_SortsReverseChronologically(t *testing.T) {
	older := lineupapi.RunKey("2026-08-14T17:26:37Z", "old")
	newer := lineupapi.RunKey("2026-08-17T17:02:20Z", "new")

	if newer >= older {
		t.Errorf("RunKey(newer)=%q should sort before RunKey(older)=%q", newer, older)
	}
	if len(strings.SplitN(newer, "-", 2)[0]) != len(strings.SplitN(older, "-", 2)[0]) {
		t.Errorf("timestamp segments differ in width (%q vs %q); lexicographic order "+
			"no longer equals chronological order", newer, older)
	}
}

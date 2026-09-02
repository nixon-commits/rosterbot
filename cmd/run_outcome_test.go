package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestConnectTenant_RouteTenantFailureRecordsTheRunOutcome is the writer-side
// half of rosterbot-cnn9, at the connectTenant call site — the same shape
// TestConnectTenant_StampsTheRunOntoTheTenantFaultItWrites uses for the
// connection-record stamp. A connect that demotes a tenant to
// needs_reconnect exits 0 so opsalert does not page the operator, which is
// exactly why the ledger row alone cannot say the run failed the tenant: this
// is the only other channel available, and it must be written on every
// routeTenant exit.
func TestConnectTenant_RouteTenantFailureRecordsTheRunOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(runOutcomeFileEnv, path)

	conns := pendingConn(t)
	conns.conn.TeamID = "" // no team assigned: an ordinary tenant fault
	d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant returned %v; a tenant-actionable failure must exit 0", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v — the routeTenant branch never called recordRunOutcome", path, err)
	}
	if string(got) != lineupapi.RunOutcomeTenantActionable {
		t.Errorf("outcome file = %q, want %q", got, lineupapi.RunOutcomeTenantActionable)
	}
}

// TestConnectTenant_OperatorActionableFailureDoesNotRecordAnOutcome is the
// mirror: routeOperator and routePostAuth already exit non-zero, so the
// ledger's own status is the true statement about the run and no second
// channel is needed. Writing one there would be harmless today, but the
// design is that the exit code is authoritative on those two routes.
func TestConnectTenant_OperatorActionableFailureDoesNotRecordAnOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(runOutcomeFileEnv, path)

	conns := &postAuthConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})
	d.login = cloudflareLogin
	d.push = func(string) {}

	if err := connectTenant(context.Background(), "alice", d); err == nil {
		t.Fatal("returned nil for an operator-actionable failure")
	}

	if _, err := os.ReadFile(path); err == nil {
		t.Error("an operator-actionable route wrote a run outcome; only routeTenant should")
	}
}

// TestRecordRunOutcome_WritesWhenSet is the write half of rosterbot-cnn9's
// second channel: with RUN_OUTCOME_FILE pointed at a real path,
// recordRunOutcome leaves the outcome there for entrypoint.sh's terminal
// run-ledger write to pick up.
func TestRecordRunOutcome_WritesWhenSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(runOutcomeFileEnv, path)

	recordRunOutcome(lineupapi.RunOutcomeTenantActionable)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != lineupapi.RunOutcomeTenantActionable {
		t.Errorf("wrote %q, want %q", got, lineupapi.RunOutcomeTenantActionable)
	}
}

// TestRecordRunOutcome_NoopWhenUnset is the ABSENCE assertion: local dev never
// sets RUN_OUTCOME_FILE (only entrypoint.sh does, inside the container), so
// `go run . connect` on a laptop must not fail or write anything anywhere.
func TestRecordRunOutcome_NoopWhenUnset(t *testing.T) {
	t.Setenv(runOutcomeFileEnv, "")
	// Must not panic, error, or touch the filesystem in any observable way.
	recordRunOutcome(lineupapi.RunOutcomeTenantActionable)
}

// runLedgerFixture sets every run-ledger flag var to a minimal valid run and
// returns a cleanup that restores them, so tests in this file don't leak
// package-level flag state into others.
func runLedgerFixture(t *testing.T) {
	t.Helper()
	ledgerID = "task-1"
	ledgerCommand = "connect --user u1"
	ledgerUser = ""
	ledgerStatus = "SUCCESS"
	ledgerExitCode = 0
	ledgerStarted = "2026-09-02T17:00:00Z"
	ledgerEnded = "2026-09-02T17:00:01Z"
	ledgerTrigger = "schedule"
	ledgerLogFile = ""
	ledgerOutcome = ""
	t.Cleanup(func() {
		ledgerID, ledgerCommand, ledgerUser, ledgerStatus = "", "", "", ""
		ledgerExitCode = -1
		ledgerStarted, ledgerEnded, ledgerTrigger, ledgerLogFile, ledgerOutcome = "", "", "", "", ""
	})
}

// TestRunLedger_UnknownOutcomeDegradesButStillWrites pins the degrade rule:
// an outcome value run-ledger doesn't recognize is warned about and dropped,
// but the terminal write must land regardless — a missing terminal record
// leaves the run RUNNING forever, which blinds both Streak and Overdue.
// Degrade to noise, never to silence.
func TestRunLedger_UnknownOutcomeDegradesButStillWrites(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	t.Chdir(t.TempDir())
	runLedgerFixture(t)
	ledgerOutcome = "some-typo-value"

	var stderr bytes.Buffer
	ledgerCmd.SetErr(&stderr)
	t.Cleanup(func() { ledgerCmd.SetErr(nil) })

	if err := runLedger(ledgerCmd, nil); err != nil {
		t.Fatalf("runLedger: %v", err)
	}
	if !strings.Contains(stderr.String(), "unknown --outcome") {
		t.Errorf("stderr = %q, want a warning naming the unrecognized outcome", stderr.String())
	}

	got, ok, err := lineupapi.NewFileRunStore(filepath.Join(".lineup", "runs")).Get(context.Background(), ledgerID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v — the terminal write must land even on a bad outcome", ok, err)
	}
	if got.Outcome != "" {
		t.Errorf("Outcome = %q, want empty: an unrecognized value must be dropped, not stored", got.Outcome)
	}
}

// TestRunLedger_KnownOutcomeIsRecorded is the mirror: a recognized outcome
// rides through onto the stored record.
func TestRunLedger_KnownOutcomeIsRecorded(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	t.Chdir(t.TempDir())
	runLedgerFixture(t)
	ledgerOutcome = lineupapi.RunOutcomeTenantActionable

	if err := runLedger(ledgerCmd, nil); err != nil {
		t.Fatalf("runLedger: %v", err)
	}

	got, ok, err := lineupapi.NewFileRunStore(filepath.Join(".lineup", "runs")).Get(context.Background(), ledgerID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Outcome != lineupapi.RunOutcomeTenantActionable {
		t.Errorf("Outcome = %q, want %q", got.Outcome, lineupapi.RunOutcomeTenantActionable)
	}
}

// TestEntrypointRunOutcomeFileFlowsIntoLedgerOutcome mirrors
// TestEntrypointRunIDIsTheLedgerID (connect_stamp_test.go): the join between
// recordRunOutcome's env var and run-ledger's --outcome flag lives entirely in
// shell text no Go test can otherwise see, so this reads entrypoint.sh and
// pins the shape directly.
func TestEntrypointRunOutcomeFileFlowsIntoLedgerOutcome(t *testing.T) {
	src, err := os.ReadFile("../entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint.sh: %v", err)
	}
	var live []string
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		live = append(live, line)
	}
	text := strings.Join(live, "\n")

	exportRe := regexp.MustCompile(`export ` + regexp.QuoteMeta(runOutcomeFileEnv) + `=\S+`)
	if !exportRe.MatchString(text) {
		t.Fatalf("entrypoint.sh does not export %s; cmd's recordRunOutcome reads "+
			"os.Getenv(%q) and has nothing to write to", runOutcomeFileEnv, runOutcomeFileEnv)
	}

	// Removed before the run, so a stale file from an earlier invocation of
	// the same binary cannot leak into this run's terminal write.
	rmRe := regexp.MustCompile(`rm -f "\$` + regexp.QuoteMeta(runOutcomeFileEnv) + `"`)
	if !rmRe.MatchString(text) {
		t.Errorf("entrypoint.sh does not clear %s before running the bot; a stale value "+
			"could leak into a later run's ledger record", runOutcomeFileEnv)
	}

	// Split on each run-ledger invocation. There must be exactly two: the
	// RUNNING write and the terminal one (identified by --exit-code, which
	// only the terminal write carries).
	calls := strings.Split(text, "./rosterbot run-ledger")
	if len(calls) < 3 { // calls[0] is everything before the first invocation
		t.Fatalf("found %d run-ledger invocations in entrypoint.sh, want 2", len(calls)-1)
	}

	outcomeRe := regexp.MustCompile(`--outcome "\$\(cat "\$` + regexp.QuoteMeta(runOutcomeFileEnv) + `"`)
	var terminalCalls, outcomeCalls int
	for _, call := range calls[1:] {
		isTerminal := strings.Contains(call, "--exit-code")
		hasOutcome := outcomeRe.MatchString(call)
		if isTerminal {
			terminalCalls++
			if !hasOutcome {
				t.Errorf("the terminal run-ledger invocation does not pass --outcome from "+
					"$%s: %s", runOutcomeFileEnv, call)
			}
		} else if hasOutcome {
			t.Errorf("the RUNNING run-ledger invocation passes --outcome; only the terminal "+
				"write should — a run in progress has no outcome to report yet: %s", call)
		}
		if hasOutcome {
			outcomeCalls++
		}
	}
	if terminalCalls != 1 {
		t.Fatalf("found %d terminal (--exit-code) run-ledger invocations, want 1", terminalCalls)
	}
	if outcomeCalls != 1 {
		t.Errorf("found %d run-ledger invocations passing --outcome, want exactly 1 (the terminal one)", outcomeCalls)
	}
}

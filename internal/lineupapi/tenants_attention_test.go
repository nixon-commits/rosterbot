package lineupapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTenantsAttentionFrom_PendingAttributesAFailingRunToTheOperator.
//
// rosterbot-v1ro: attentionFrom's run-failure signal was scoped INSIDE the
// conn_status === "verified" branch, so a tenant permanently stuck at
// ConnPending (a crashed connect writes zero connection records — see
// rosterbot-spb9) fell through every branch to the terminal `return "—"`,
// reading as healthy on the one screen built to find failures.
//
// This RUNS the real attentionFrom under node rather than pattern-matching
// its source, on the same reasoning as infra/dashboard_function_behavior_test.go:
// a source-shaped assertion (does the file mention "pending") would pass
// against a mutation that left the fallthrough in place while adding an
// unrelated "pending" string elsewhere.
func TestTenantsAttentionFrom_PendingAttributesAFailingRunToTheOperator(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the executable check of attentionFrom")
	}

	fn := extractAttentionFrom(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "check.js")
	source := attentionFromDriverPreamble + "\n" + fn + "\n" + attentionFromDriverBody
	if err := os.WriteFile(script, []byte(source), 0o644); err != nil {
		t.Fatalf("write driver: %v", err)
	}

	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed:\n%s", out)
	}

	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed driver output line: %q\nfull output:\n%s", line, out)
		}
		got[parts[0]] = parts[1]
	}

	const operatorRunFailing = "You (not the tenant) — a job is failing"
	const tenantMustReconnect = "The tenant — they must reconnect"
	const operatorCheckDidNotFinish = "You (not the tenant) — the check did not finish"
	const operatorCheckNeverReachedFantrax = "You (not the tenant) — the check never reached fantrax"

	cases := []struct {
		name string
		want string
	}{
		// (1) The defect this bead fixes: pending, with a failing run, must
		// attribute the failure to the operator rather than read "—".
		{"pending_with_failure", operatorRunFailing},
		// (2) ABSENCE: pending with no run data, or a run record with no
		// failure, must still read "—" — the fallback must not fire on
		// evidence that isn't there.
		{"pending_no_runs", "—"},
		{"pending_runs_no_failure", "—"},
		// (3) precedence: needs_reconnect outranks the run-failure fallback
		// even when a run is failing — the tenant, not the operator, must act.
		{"needs_reconnect_with_failure", tenantMustReconnect},
		// (3b) precedence: an operator-actionable conn_error on a still-pending
		// row keeps its own, more specific copy rather than the run-failure
		// fallback's — the fallback is the LAST branch, not a new first one.
		{"pending_operator_actionable_with_failure", "You (not the tenant)"},
		// (4) interrupted keeps its own copy, unaffected by the fallback.
		{"interrupted_with_failure", operatorCheckDidNotFinish},
		// (4b) check_failed (rosterbot-spb9) keeps its own copy too, one step
		// earlier than interrupted: the check never reached Fantrax at all,
		// so the tenant still has nothing to fix.
		{"check_failed_with_failure", operatorCheckNeverReachedFantrax},
		// (5) verified is unchanged (it already had this behavior).
		{"verified_with_failure", operatorRunFailing},
	}
	for _, tc := range cases {
		v, ok := got[tc.name]
		if !ok {
			t.Errorf("driver produced no output for case %q; full output:\n%s", tc.name, out)
			continue
		}
		if v != tc.want {
			t.Errorf("%s: attentionFrom returned %q, want %q", tc.name, v, tc.want)
		}
	}
}

// extractAttentionFrom pulls the source of tenants.js's attentionFrom out of
// the real file, from the "function attentionFrom(" marker to the closing
// brace that sits at column 0 (the function's own, as opposed to any nested
// brace inside its body, all of which are indented).
//
// It fails loudly rather than silently extracting nothing: a marker that goes
// missing (the function renamed) or an unbalanced extraction (the column-0
// scan stopped on the wrong line) both mean this test is no longer testing
// the function it claims to.
func extractAttentionFrom(t *testing.T) string {
	t.Helper()

	path := filepath.Join("..", "..", "web", "dashboard", "tenants.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)

	const marker = "function attentionFrom("
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatalf("no %q in %s — attentionFrom was renamed or removed, and this test "+
			"no longer covers it", marker, path)
	}
	rest := src[start:]

	lines := strings.Split(rest, "\n")
	var out []string
	closed := false
	for _, line := range lines {
		out = append(out, line)
		if strings.HasPrefix(line, "}") {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatalf("never found attentionFrom's closing brace at column 0 in %s", path)
	}

	fn := strings.Join(out, "\n")
	if open, close := strings.Count(fn, "{"), strings.Count(fn, "}"); open != close {
		t.Fatalf("extracted attentionFrom is not brace-balanced (%d open, %d close) — "+
			"the column-0 scan stopped at the wrong line:\n%s", open, close, fn)
	}
	return fn
}

// attentionFromDriverPreamble declares the one module-level value
// attentionFrom reads from outside its own scope (OPERATOR_ACTIONABLE, from
// tenants.js). The extracted function is otherwise self-contained; without
// this the driver throws a ReferenceError rather than testing anything —
// which would itself be a real finding rather than a harness bug to route
// around.
const attentionFromDriverPreamble = `
const OPERATOR_ACTIONABLE = new Set(["bot_challenge"]);
`

const attentionFromDriverBody = `
function report(name, t) {
    console.log(name + "\t" + attentionFrom(t));
}

report("pending_with_failure",
    { status: "active", conn_status: "pending", runs: { last_failure: { error: "connect failed" } } });
report("pending_no_runs",
    { status: "active", conn_status: "pending", runs: null });
report("pending_runs_no_failure",
    { status: "active", conn_status: "pending", runs: { last_failure: null } });
report("needs_reconnect_with_failure",
    { status: "active", conn_status: "needs_reconnect", runs: { last_failure: { error: "connect failed" } } });
report("pending_operator_actionable_with_failure",
    { status: "active", conn_status: "pending", conn_error: "bot_challenge", runs: { last_failure: { error: "connect failed" } } });
report("interrupted_with_failure",
    { status: "active", conn_status: "interrupted", runs: { last_failure: { error: "connect failed" } } });
report("check_failed_with_failure",
    { status: "active", conn_status: "check_failed", runs: { last_failure: { error: "connect failed" } } });
report("verified_with_failure",
    { status: "active", conn_status: "verified", runs: { last_failure: { error: "connect failed" } } });
`

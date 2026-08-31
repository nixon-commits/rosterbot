package cmd

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// The write half of rosterbot-jg92: every connect outcome is recorded together
// with the run that reached it, so GET /v1/runs can say what a specific row
// concluded instead of showing the ledger's exit status — which reads SUCCESS
// on every tenant-actionable failure by design.

func cloudflareLogin(lineupapi.FantraxCreds) loginEvidence {
	return loginEvidence{Matched: map[string]bool{"cloudflare": true}, Title: "Just a moment…"}
}

// TestConnectTenant_StampsTheRunOntoTheTenantFaultItWrites is the bead's own
// screen from the writing side: a connect that exits 0 because only the tenant
// can act still leaves a durable statement that THIS run failed.
func TestConnectTenant_StampsTheRunOntoTheTenantFaultItWrites(t *testing.T) {
	t.Setenv("RUN_ID", "task-1")
	conns := pendingConn(t)
	conns.conn.TeamID = "" // no team assigned: the failure Jon actually saw
	d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant returned %v; a tenant-actionable failure must exit 0", err)
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.LastConnectRun == nil {
		t.Fatal("no run stamp: the Runs tab is left showing SUCCESS with nothing beside it")
	}
	want := lineupapi.ConnectRun{
		RunID:     "task-1",
		Verdict:   lineupapi.ConnectVerdictFailed,
		LastError: lineupapi.ConnErrNoTeam,
	}
	if *got.LastConnectRun != want {
		t.Fatalf("stamp = %+v, want %+v", *got.LastConnectRun, want)
	}
}

// TestConnectTenant_AnOperatorActionableFailureIsStampedFailed is the mirror of
// the bead, and the reason the verdict is an explicit parameter.
//
// On the operator-actionable route (a Cloudflare block) fail() DELIBERATELY
// leaves the tenant's Status alone — so on a re-verify of an already-verified
// tenant the record still says "verified" while the task exits non-zero and the
// ledger records FAILED. A stamp that copied Status would put a green
// "connected" chip on that FAILED row.
func TestConnectTenant_AnOperatorActionableFailureIsStampedFailed(t *testing.T) {
	t.Setenv("RUN_ID", "task-1")
	conns := &postAuthConns{conn: connFor(t, "alice", lineupapi.ConnVerified)}
	d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})
	d.login = cloudflareLogin
	d.push = func(string) {} // operator-actionable failures do push; not what this asserts

	if err := connectTenant(context.Background(), "alice", d); err == nil {
		t.Error("returned nil; an operator-actionable failure must exit non-zero so the ledger pages")
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.Status != lineupapi.ConnVerified {
		t.Fatalf("Status = %q, want %q: the operator-actionable route must not demote the tenant",
			got.Status, lineupapi.ConnVerified)
	}
	if got.LastConnectRun == nil {
		t.Fatal("no run stamp on an operator-actionable failure")
	}
	if got.LastConnectRun.Verdict != lineupapi.ConnectVerdictFailed {
		t.Fatalf("stamped verdict %q on a run that failed and paged, want %q — the record's own "+
			"Status is still %q here, which is exactly why it must not be the source",
			got.LastConnectRun.Verdict, lineupapi.ConnectVerdictFailed, got.Status)
	}
	if got.LastConnectRun.LastError != lineupapi.ConnErrBotChallenge {
		t.Fatalf("stamped class %q, want %q", got.LastConnectRun.LastError, lineupapi.ConnErrBotChallenge)
	}
}

// TestConnectTenant_AVerifiedRunIsStampedVerified is the other half: the
// mechanism has to be able to say yes, or a green chip would only ever mean
// "nothing recorded".
func TestConnectTenant_AVerifiedRunIsStampedVerified(t *testing.T) {
	t.Setenv("RUN_ID", "task-1")
	conns := pendingConn(t)
	d := postAuthDeps(t, conns, &memFeed{}, &bytes.Buffer{})

	if err := connectTenant(context.Background(), "alice", d); err != nil {
		t.Fatalf("connectTenant: %v", err)
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d connection records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.Status != lineupapi.ConnVerified {
		t.Fatalf("Status = %q, want verified", got.Status)
	}
	want := lineupapi.ConnectRun{RunID: "task-1", Verdict: lineupapi.ConnectVerdictVerified}
	if got.LastConnectRun == nil || *got.LastConnectRun != want {
		t.Fatalf("stamp = %+v, want %+v", got.LastConnectRun, want)
	}
}

// TestConnectRecord_NoRunIDLeavesAnOlderStampAlone: a hand-run task has no
// RUN_ID. Clearing the stamp would discard a true statement about a real run;
// overwriting it with an empty id would make it unmatchable AND unreadable. The
// read side matches on id, so an older stamp can never be shown against this
// run anyway.
func TestConnectRecord_NoRunIDLeavesAnOlderStampAlone(t *testing.T) {
	t.Setenv("RUN_ID", "")
	old := &lineupapi.ConnectRun{
		RunID: "task-old", Verdict: lineupapi.ConnectVerdictVerified,
	}
	conn := connFor(t, "alice", lineupapi.ConnVerified)
	conn.LastConnectRun = old
	conns := &postAuthConns{}
	c := connectRun{connectDeps: connectDeps{conns: conns}, ctx: context.Background(), uid: "alice", conn: conn}

	conn.Status = lineupapi.ConnNeedsReconnect
	conn.LastError = lineupapi.ConnErrBadCredentials
	if err := c.record(lineupapi.ConnectVerdictFailed); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(conns.put) != 1 {
		t.Fatalf("wrote %d records, want 1", len(conns.put))
	}
	got := conns.put[0]
	if got.LastConnectRun == nil || *got.LastConnectRun != *old {
		t.Fatalf("stamp = %+v, want the untouched %+v", got.LastConnectRun, *old)
	}
	if got.Status != lineupapi.ConnNeedsReconnect || got.LastError != lineupapi.ConnErrBadCredentials {
		t.Fatalf("the new outcome did not land: status %q error %q", got.Status, got.LastError)
	}
}

// TestConnectTaskWritesConnectionsOnlyThroughTheRecorder is the test that
// matters most here.
//
// runConnect needs DynamoDB, KMS and a headless browser, so no unit test can
// drive its real call sites; the property that every connect write carries a
// stamp therefore has to be checked structurally. Modelled on
// internal/fantrax/retry_sites_test.go, and package-WIDE like it: a connection
// write added in connect_feed.go, connect_classify.go or a new connect_*.go
// would otherwise evade a guard that parsed connect.go alone.
//
// WHAT IT DOES NOT CLOSE: a path that writes NOTHING has zero call sites and
// passes cleanly. Two such paths exist in connectTenant today (a failed
// opener.Open, a malformed credential blob), and they leave the record at
// ConnPending — that is rosterbot-ch0s's territory, not this guard's.
func TestConnectTaskWritesConnectionsOnlyThroughTheRecorder(t *testing.T) {
	// The two session-ladder writers are legitimate and deliberately unstamped:
	// an ordinary scheduled run is not a connect and must not take credit or
	// blame on a connect row (see sessionLadder.stop's own comment).
	allowed := map[string]bool{
		"connectRun.record":     true,
		"sessionLadder.refresh": true,
		"sessionLadder.stop":    true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/: %v", err)
	}
	fset := token.NewFileSet()
	seen := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			owner := funcQualifiedName(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "PutConnection" {
					return true
				}
				seen[owner]++
				if !allowed[owner] {
					t.Errorf("%s writes a connection record directly (%s:%d). Every connect "+
						"write must go through connectRun.record so the run that reached the "+
						"outcome is stamped in the same write (rosterbot-jg92).",
						owner, name, fset.Position(sel.Pos()).Line)
				}
				return true
			})
		}
	}
	// The single writer must still write. A record() that stopped calling
	// PutConnection would satisfy the loop above vacuously.
	if seen["connectRun.record"] == 0 {
		t.Error("connectRun.record contains no PutConnection call; the connect task writes nothing")
	}
}

func funcQualifiedName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// TestEntrypointRunIDIsTheLedgerID pins the one join in this feature that no
// handler test can see.
//
// Every test above constructs BOTH sides of the equality itself — "task-1" in
// the fake ledger and in the stamp. What actually has to hold in production is
// that the id the bot reads from RUN_ID is the id entrypoint.sh files the
// ledger row under. Change either derivation and every other test in this
// change stays green while the chip silently never renders again.
//
// Reading the shell source is the established shape for a guard nothing else
// can express (internal/fantrax/retry_sites_test.go, internal/lineupapi/routedoc_test.go).
func TestEntrypointRunIDIsTheLedgerID(t *testing.T) {
	src, err := os.ReadFile("../entrypoint.sh")
	if err != nil {
		t.Fatalf("read entrypoint.sh: %v", err)
	}
	// Shell comments are stripped first. Without it, commenting the export line
	// out — which is how it would most plausibly disappear — still matches the
	// regex below and the guard passes on a broken entrypoint.
	var live []string
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		live = append(live, line)
	}
	text := strings.Join(live, "\n")

	exportRe := regexp.MustCompile(`export RUN_ID="\$([A-Za-z_][A-Za-z0-9_]*)"`)
	m := exportRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatal(`entrypoint.sh no longer exports RUN_ID from a shell variable; ` +
			`the connect task reads os.Getenv("RUN_ID") to stamp its run`)
	}
	runIDVar := m[1]

	ledgerRe := regexp.MustCompile(`run-ledger --id "\$([A-Za-z_][A-Za-z0-9_]*)"`)
	ids := ledgerRe.FindAllStringSubmatch(text, -1)
	if len(ids) < 2 {
		t.Fatalf("found %d `run-ledger --id` invocations, want at least 2 (the RUNNING write "+
			"and the terminal one)", len(ids))
	}
	for _, got := range ids {
		if got[1] != runIDVar {
			t.Errorf("run-ledger is filed under $%s while RUN_ID exports $%s; the run id the "+
				"bot stamps would no longer match the ledger row it is joined to",
				got[1], runIDVar)
		}
	}
}

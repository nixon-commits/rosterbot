package fantrax

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// retrySite is one withRetry call: the function that makes it and the label it
// passes.
type retrySite struct {
	fn    string
	label string
}

// wrappedCalls returns every withRetry call site in the package's non-test
// sources, keyed by enclosing function.
func wrappedCalls(t *testing.T) []retrySite {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	// Walked by hand rather than with parser.ParseDir, which is deprecated
	// precisely because it ignores build tags. Ignoring them is what this guard
	// wants — a wrap hidden behind a build tag is still a wrap — so the walk
	// states that choice instead of inheriting it from a deprecated helper.
	fset := token.NewFileSet()
	var sites []retrySite
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "withRetry" || len(call.Args) == 0 {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: withRetry label must be a string literal so the "+
						"ledger's log_tail names the call", fn.Name.Name)
					return true
				}
				label, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: unquote label: %v", fn.Name.Name, err)
					return true
				}
				sites = append(sites, retrySite{fn: fn.Name.Name, label: label})
				return true
			})
			return false
		})
	}
	return sites
}

// withRetry is for IDEMPOTENT READS ONLY, and nothing in the type system says
// so — it is generic over any fetch func, so wiring a mutation through it
// compiles and passes every other test in this package. The cost of getting it
// wrong is not a failed run: retrying a mutating POST after a 524 can apply the
// same roster edit twice, because a timed-out origin may already have committed
// it before it stopped answering.
//
// So the allowlist is the control. Adding a wrap is a deliberate act that
// updates this list; wrapping ApplyLineup's edit_roster call — or any other
// write — fails here rather than shipping.
func TestWithRetry_WrapsOnlyIdempotentReads(t *testing.T) {
	allowed := map[retrySite]string{
		{fn: "allMatchups", label: "getAllMatchups"}:                        "pure read, no disk cache to fall back on (rosterbot-eaa37bb)",
		{fn: "fetchPeriodSnapshot", label: "getTeamRosterInfoRaw"}:          "pure roster read; ~35 per windowed recency fetch (rosterbot-exaf)",
		{fn: "getPlayerGSSnapshotForPeriod", label: "getTeamRosterInfoRaw"}: "same pure roster read, reached by the GS walk (rosterbot-exaf)",
	}

	got := wrappedCalls(t)

	// A guard that finds nothing passes for the wrong reason — the same
	// vacuous-pass trap TestBacktestToWireResult_RoundsEveryFloat guards
	// against. If this fires, the AST walk broke, not the code.
	if len(got) == 0 {
		t.Fatal("found no withRetry call sites at all — the walk is broken, " +
			"not the package (allMatchups has been wrapped since eaa37bb)")
	}

	for _, s := range got {
		if _, ok := allowed[s]; !ok {
			t.Errorf("unapproved retry wrap: %s calls withRetry(%q).\n"+
				"withRetry is for idempotent READS only. If %s is a pure read, add it to "+
				"the allowlist with the reason. If it mutates roster state, do NOT wrap "+
				"it — a retried write can apply twice.", s.fn, s.label, s.fn)
		}
	}

	seen := map[retrySite]bool{}
	for _, s := range got {
		seen[s] = true
	}
	var missing []string
	for s := range allowed {
		if !seen[s] {
			missing = append(missing, s.fn+"/"+s.label)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("allowlisted retry wraps have disappeared: %v.\n"+
			"If a wrap was removed on purpose, drop it from the allowlist; otherwise "+
			"the call has silently lost its retry.", missing)
	}
}

// ApplyLineup is the one mutating path in this package, and the reason the
// wrap lives at the call site rather than in the fork's transport. Named
// explicitly so the constraint survives a future rename of the allowlist above.
func TestApplyLineupIsNotRetried(t *testing.T) {
	for _, s := range wrappedCalls(t) {
		if strings.Contains(strings.ToLower(s.fn), "applylineup") {
			t.Fatalf("%s is wrapped in withRetry — a retried roster edit can apply "+
				"twice after a 524", s.fn)
		}
	}
}

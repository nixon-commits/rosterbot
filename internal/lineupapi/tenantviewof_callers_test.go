package lineupapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The two guards in this file exist because the package's actual gate cannot
// see what they check. adminOnlyRoutes is a PATH allowlist consulted with
// r.URL.Path, so it says nothing about which values a handler feeds to a
// cross-tenant read. tenantViewOf and runSummary take an arbitrary uid; the
// bead's own NOTES name the failure they guard ("a guard that appears to cover
// admin surfaces but structurally cannot") and the naive version of it compiles
// clean and passes every existing authz test.
//
// Both walk the package's non-test sources. They must never be widened by
// deleting an assertion — a new caller is a decision to record here, with a
// sentence saying who authorized that uid.

// arbitraryTenantFuncs are the functions that read an ARBITRARY tenant's data.
// A caller of either must have already established that the requester may read
// that tenant.
var arbitraryTenantFuncs = []string{"tenantViewOf", "runSummary"}

// allowedCallers is the recorded, deliberate caller set for each.
//
//	tenantViewOf  tenantView (the caller's own uid), runSummary (admin listing),
//	              and handleTenantRuns / handleTenantRunOutput — the admin
//	              drill-down (rosterbot-f0th), authorized because both are
//	              registered EXCLUSIVELY at /v1/tenants/{id}/runs[...], inside
//	              the adminOnlyRoutes "/v1/tenants" prefix.
//	runSummary    fillRunSummaries, driven by handleTenants
var allowedCallers = map[string][]string{
	"tenantViewOf": {"tenantView", "runSummary", "handleTenantRuns", "handleTenantRunOutput"},
	"runSummary":   {"fillRunSummaries"},
}

// TestTenantViewOf_CallersAreAnAllowlist pins who may read another tenant's
// data.
//
// MUTATION: add `cfg.tenantViewOf(r.Context(), "someone")` (or a runSummary
// call) inside handleRuns or any other handler — the caller set grows and this
// goes red.
func TestTenantViewOf_CallersAreAnAllowlist(t *testing.T) {
	files := packageFiles(t)
	for _, name := range arbitraryTenantFuncs {
		got := referencingFuncs(t, files, name)
		// Vacuity guard. Both halves are derived from a name, so a rename or an
		// inline collapses the set to empty — and an empty set is a subset of
		// anything, which is exactly the shape of guard this repo has been
		// bitten by before. Fail loudly instead.
		if len(got) == 0 {
			t.Fatalf("found no reference to %q anywhere in the package's non-test "+
				"sources. If it was renamed or inlined, update arbitraryTenantFuncs "+
				"and allowedCallers — an empty set would otherwise pass forever "+
				"while guarding nothing", name)
		}
		want := allowedCallers[name]
		for _, caller := range got {
			if !slices.Contains(want, caller) {
				t.Errorf("%s is referenced from %s, which is not in the recorded "+
					"caller set %v. That function reads an ARBITRARY tenant's data and "+
					"the route allowlist cannot see it: say in a comment who authorized "+
					"that uid, then add the caller here.", name, caller, want)
			}
		}
		for _, caller := range want {
			if !slices.Contains(got, caller) {
				t.Errorf("%s is no longer referenced from %s, so the allowlist is "+
					"stale; a stale entry silently widens what this guard permits", name, caller)
			}
		}
	}
}

// TestNoRequestValueBecomesATenantID is the guard the bead's trap actually
// calls for.
//
// The caller allowlist above says WHICH functions may read another tenant, and
// says nothing about the value they pass. This one finds every function that
// feeds a request-derived string — r.URL.Query(), r.PathValue, r.FormValue, a
// header — into a UserID conversion or into an arbitrary-tenant function, and
// requires that function to be a handler REGISTERED ONLY AT ADMIN-ONLY PATHS.
//
// Taking a tenant id from the request is legitimate exactly once: on
// /v1/tenants/{id}/..., where isAdminOnlyPath's `p+"/"` rule covers the whole
// subtree. It is illegitimate as a query parameter on any other route, because
// isAdminOnlyPath matches on r.URL.Path — the param reaches no gate at all,
// while compiling clean and passing every existing authz test.
//
// The exemption is DERIVED, not listed: it reads the mux registrations and runs
// the real isAdminOnlyPath over them. A hand-kept allowlist would have to be
// updated when a handler moved routes, and a stale entry would silently widen
// what this permits — which is the failure mode this whole file exists to
// close, one level up.
//
// MUTATION: add
// `_ = cfg.runSummary(r.Context(), UserID(r.URL.Query().Get("tenant")), nil)`
// to handleRuns — red, because GET /v1/runs is not an admin-only path.
func TestNoRequestValueBecomesATenantID(t *testing.T) {
	files := packageFiles(t)
	routes := handlerRoutes(t, files)

	// Vacuity guard on the DETECTOR, not on the result: an empty finding is the
	// passing state here, so the only way to know the walk is alive is to
	// confirm the package still contains the shapes it looks for.
	var sawRequestRead, sawTenantSink int
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			if isRequestDerived(n) {
				sawRequestRead++
			}
			if call, ok := n.(*ast.CallExpr); ok && tenantSinkName(call) != "" {
				sawTenantSink++
			}
			return true
		})
	}
	if sawRequestRead == 0 {
		t.Fatal("found no request-derived reads (r.URL.Query, r.PathValue, r.FormValue, " +
			"Header.Get) in the package; the detector below can no longer recognise " +
			"what it is looking for, so it would pass on anything")
	}
	if sawTenantSink == 0 {
		t.Fatal("found no UserID conversion or arbitrary-tenant call in the package; " +
			"the detector below has nothing to watch and would pass on anything")
	}

	for _, f := range files {
		for _, d := range f.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			tainted := taintedLocals(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sink := tenantSinkName(call)
				if sink == "" {
					return true
				}
				for _, arg := range call.Args {
					if !containsRequestDerived(arg, tainted) {
						continue
					}
					if why, ok := gatedByAdminRoute(routes, fn.Name.Name); ok {
						t.Logf("%s: %s takes its tenant id from the request, which is "+
							"authorized here: %s", f.name, fn.Name.Name, why)
						continue
					}
					t.Errorf("%s: %s passes a request-derived value into %s, and is not "+
						"registered exclusively at admin-only paths (registered at %v). "+
						"A tenant id taken from the request reaches NO gate — "+
						"isAdminOnlyPath matches on r.URL.Path only, so a ?tenant= param "+
						"is unauthenticated cross-tenant access that compiles clean and "+
						"passes every existing authz test (rosterbot-nejq).",
						f.name, fn.Name.Name, sink, routes[fn.Name.Name])
				}
				return true
			})
		}
	}
}

// TestNoRequestValueBecomesATenantID_BodyAndStructFieldForms is the pin for
// rosterbot-l29p: isRequestDerived / taintedLocals / containsRequestDerived,
// run directly against synthetic fixtures (parser.ParseFile, no type
// checking, matching how packageFiles reads the real package), must flag a
// tenant id that arrives through a struct-field assignment or a decoded JSON
// body — the two shapes the live guard above could not see. Found by running
// these same helpers over these exact shapes and getting FLAGGED=false with
// an empty tainted set for all three (see the bead).
//
// D/E/F are the positive fixtures, I/J the two split forms the first review
// found unrecognised (a decoder held in a local; a decode straight into a
// field address); G/H/K are ABSENCE fixtures and are not
// decoration — a taint pass that starts tainting every struct-field
// assignment, or every Decode call, regardless of its source would pass D/E/F
// for the wrong reason (flagging everything downstream of any assignment), so
// G and H pin that a non-request source stays clean.
func TestNoRequestValueBecomesATenantID_BodyAndStructFieldForms(t *testing.T) {
	cases := []struct {
		name     string
		fnName   string
		src      string
		wantFlag bool
	}{
		{
			name:   "D_struct_field_assign",
			fnName: "D",
			src: `package fixture

import "net/http"

func D(w http.ResponseWriter, r *http.Request) {
	var req struct{ Tenant string }
	req.Tenant = r.URL.Query().Get("t")
	_ = cfg.tenantViewOf(r.Context(), UserID(req.Tenant))
}
`,
			wantFlag: true,
		},
		{
			name:   "E_json_body_decode",
			fnName: "E",
			src: `package fixture

import (
	"encoding/json"
	"net/http"
)

func E(w http.ResponseWriter, r *http.Request) {
	var body struct{ Tenant string }
	json.NewDecoder(r.Body).Decode(&body)
	_ = cfg.tenantViewOf(r.Context(), UserID(body.Tenant))
}
`,
			wantFlag: true,
		},
		{
			name:   "F_json_body_to_tenantViewOf",
			fnName: "F",
			src: `package fixture

import (
	"encoding/json"
	"net/http"
)

func F(w http.ResponseWriter, r *http.Request) {
	var tenant string
	json.NewDecoder(r.Body).Decode(&tenant)
	_ = cfg.tenantViewOf(r.Context(), UserID(tenant))
}
`,
			wantFlag: true,
		},
		{
			name:   "G_struct_field_from_non_request_value",
			fnName: "G",
			src: `package fixture

func G(other string) {
	var req struct{ Tenant string }
	req.Tenant = other
	_ = cfg.tenantViewOf(nil, UserID(req.Tenant))
}
`,
			wantFlag: false,
		},
		{
			name:   "H_decode_from_non_body_reader",
			fnName: "H",
			src: `package fixture

import (
	"encoding/json"
	"strings"
)

func H(s string) {
	var body struct{ Tenant string }
	json.NewDecoder(strings.NewReader(s)).Decode(&body)
	_ = cfg.tenantViewOf(nil, UserID(body.Tenant))
}
`,
			wantFlag: false,
		},
		{
			name:   "I_decoder_held_in_a_local",
			fnName: "I",
			src: `package fixture

import (
	"encoding/json"
	"net/http"
)

func I(w http.ResponseWriter, r *http.Request) {
	var body struct{ Tenant string }
	dec := json.NewDecoder(r.Body)
	dec.Decode(&body)
	_ = cfg.tenantViewOf(r.Context(), UserID(body.Tenant))
}
`,
			wantFlag: true,
		},
		{
			name:   "J_decode_into_a_field_address",
			fnName: "J",
			src: `package fixture

import (
	"encoding/json"
	"net/http"
)

func J(w http.ResponseWriter, r *http.Request) {
	var req struct{ Tenant string }
	json.NewDecoder(r.Body).Decode(&req.Tenant)
	_ = cfg.tenantViewOf(r.Context(), UserID(req.Tenant))
}
`,
			wantFlag: true,
		},
		{
			name:   "K_decoder_local_from_non_body_reader",
			fnName: "K",
			src: `package fixture

import (
	"encoding/json"
	"strings"
)

func K(s string) {
	var body struct{ Tenant string }
	dec := json.NewDecoder(strings.NewReader(s))
	dec.Decode(&body)
	_ = cfg.tenantViewOf(nil, UserID(body.Tenant))
}
`,
			wantFlag: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, tc.name+".go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			var fn *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == tc.fnName {
					fn = fd
				}
			}
			if fn == nil {
				t.Fatalf("fixture defines no func %s", tc.fnName)
			}

			tainted := taintedLocals(fn)
			flagged := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || tenantSinkName(call) == "" {
					return true
				}
				for _, arg := range call.Args {
					if containsRequestDerived(arg, tainted) {
						flagged = true
					}
				}
				return true
			})

			if flagged != tc.wantFlag {
				t.Errorf("%s: FLAGGED=%v taintedLocals=%v, want FLAGGED=%v",
					tc.name, flagged, sortedKeys(tainted), tc.wantFlag)
			}
			if tc.wantFlag && len(tainted) == 0 {
				t.Errorf("%s: flagged with an EMPTY tainted set — the detector "+
					"caught the sink through some path this fixture isn't "+
					"pinning, so a real regression here could still read green",
					tc.name)
			}
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type parsedFile struct {
	name string
	file *ast.File
}

// handlerRoutes maps a handler method name to every mux pattern PATH it is
// registered at, read out of the `mux.HandleFunc("METHOD /path", cfg.name)`
// calls in the package. These are the same string literals the mux is handed,
// so this reads the source of truth rather than a restatement of it — the same
// reasoning routedoc_test.go's registeredRoutes gives.
func handlerRoutes(t *testing.T, files []parsedFile) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			handler, ok := call.Args[1].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fields := strings.Fields(pattern)
			out[handler.Sel.Name] = append(out[handler.Sel.Name], fields[len(fields)-1])
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no mux.HandleFunc registrations; gatedByAdminRoute would then " +
			"deny everything, which is loud but for the wrong reason — teach " +
			"handlerRoutes about however registration now works")
	}
	return out
}

// gatedByAdminRoute reports whether fn is a handler registered ONLY at paths
// the real isAdminOnlyPath accepts.
//
// A function with no registration at all is NOT exempt: it is a helper, and
// nothing here can establish which route reaches it.
func gatedByAdminRoute(routes map[string][]string, fn string) (string, bool) {
	paths := routes[fn]
	if len(paths) == 0 {
		return "", false
	}
	for _, p := range paths {
		if !isAdminOnlyPath(p) {
			return "", false
		}
	}
	return "registered only at admin-only paths " + strings.Join(paths, ", ") +
		" (adminOnlyRoutes covers the subtree)", true
}

// packageFiles parses every non-test .go file in the package directory, which
// is the test's working directory.
//
// os.ReadDir + parser.ParseFile rather than parser.ParseDir: that function and
// *ast.Package are deprecated, and staticcheck's SA1019 is enabled through
// .golangci.yml's standard set, so make lint would fail on them.
func packageFiles(t *testing.T) []parsedFile {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var out []parsedFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out = append(out, parsedFile{name: name, file: f})
	}
	if len(out) == 0 {
		t.Fatal("parsed no non-test files in the package directory; both guards in " +
			"this file would then be vacuous")
	}
	return out
}

// referencingFuncs returns the names of the functions that mention sel as a
// selector — `cfg.tenantViewOf(...)` and the method VALUE `f := cfg.tenantViewOf`
// alike. Matching references rather than call sites is deliberate: a method
// value escapes a call-site walk while still handing the primitive to whoever
// holds it.
func referencingFuncs(t *testing.T, files []parsedFile, sel string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, f := range files {
		for _, d := range f.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == sel {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == sel {
					seen[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tenantSinkName reports which arbitrary-tenant sink this call is, or "".
// A `UserID(x)` conversion counts: it is how a raw request string becomes the
// type these functions take.
func tenantSinkName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if fun.Name == "UserID" {
			return "UserID(...)"
		}
	case *ast.SelectorExpr:
		if slices.Contains(arbitraryTenantFuncs, fun.Sel.Name) {
			return fun.Sel.Name
		}
	}
	return ""
}

// isRequestDerived reports whether n is a read of the incoming request:
// Query(), PathValue(...), FormValue(...), PostFormValue(...), a
// Header.Get(...), a bare <ident>.Body selector (e.g. r.Body), or a
// X.Decode(...) call whose receiver is (transitively) json.NewDecoder of an
// argument that mentions the request body. Matched on the method/field name
// alone, so renaming the *http.Request variable cannot hide it.
func isRequestDerived(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.SelectorExpr:
		// <ident>.Body: the request body reader itself, e.g. `r.Body`. This is
		// deliberately loose (any identifier, not just `r`) — same reasoning
		// as matching on method name alone above — and is what lets the
		// NewDecoder case below recurse into an arbitrarily wrapped argument
		// (http.MaxBytesReader(w, r.Body, n), every real handler in this
		// package) rather than only a bare top-level r.Body.
		if x.Sel.Name != "Body" {
			return false
		}
		_, ok := x.X.(*ast.Ident)
		return ok
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		switch sel.Sel.Name {
		case "Query", "PathValue", "FormValue", "PostFormValue":
			return true
		case "Get":
			// url.Values.Get / http.Header.Get. Narrowed to a chain whose
			// receiver is itself a call or a Header/URL selector, so an
			// unrelated store `Get(ctx, id)` is not swept in.
			switch recv := sel.X.(type) {
			case *ast.CallExpr:
				return isRequestDerived(recv)
			case *ast.SelectorExpr:
				return recv.Sel.Name == "Header" || recv.Sel.Name == "URL"
			}
		case "NewDecoder":
			// json.NewDecoder(<expr mentioning the body>): request-derived
			// when any argument's subtree contains a body selector — reusing
			// containsRequestDerived (nil taint map; reads from a nil map are
			// safe) is what lets this see through a wrapper call like
			// http.MaxBytesReader(w, r.Body, n) instead of only a bare arg.
			for _, arg := range x.Args {
				if containsRequestDerived(arg, nil) {
					return true
				}
			}
		case "Decode":
			// X.Decode(...) where X is (transitively) json.NewDecoder(...ofBody).
			// Recurses into the receiver the same way the Get case above
			// recurses into its own receiver.
			if recv, ok := sel.X.(*ast.CallExpr); ok {
				return isRequestDerived(recv)
			}
		}
	}
	return false
}

// taintedLocals names the local variables in fn that were assigned, directly or
// transitively, from a request-derived read.
//
// Without this the guard only sees the value written inline —
// `UserID(r.URL.Query().Get("tenant"))` — and misses the shape a real handler
// is far more likely to have, which is two statements:
//
//	t := r.URL.Query().Get("tenant")
//	view, ok := cfg.tenantViewOf(ctx, UserID(t))
//
// Confirmed by mutation: the inline form was caught and the two-statement form
// was not, until this pass existed. Iterated to a fixed point so a value laundered
// through a second local is still tainted. It is a syntactic approximation, not
// a type-checked taint analysis — shadowing or a closure can still hide a value
// from it — which is why the caller allowlist above is a separate, independent
// guard rather than a redundant one.
//
// Two more shapes were folded into the same fixed-point loop for rosterbot-l29p:
// an assignment whose LHS is a struct-field selector (`req.Tenant = <tainted>`)
// taints the selector's ROOT identifier — coarse on purpose, since the pass
// cannot know which other fields alias the same struct, and a struct written
// from a request value is exactly the shape the bead's fixtures exist to
// catch; and a call `<request-derived receiver>.Decode(&target)` taints
// target, because Decode's taint arrives through an out-parameter rather than
// a return value, so it cannot go through the AssignStmt branch at all.
func taintedLocals(fn *ast.FuncDecl) map[string]bool {
	tainted := map[string]bool{}
	for {
		grew := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range stmt.Rhs {
					if !containsRequestDerived(rhs, tainted) {
						continue
					}
					// One RHS feeding several LHS (a multi-value call) taints all
					// of them: which one carries the value is not knowable here,
					// and over-tainting fails loud rather than silent.
					lhs := stmt.Lhs
					if len(stmt.Rhs) == len(stmt.Lhs) {
						lhs = stmt.Lhs[i : i+1]
					}
					for _, l := range lhs {
						root := taintRoot(l)
						if root == "" || root == "_" || tainted[root] {
							continue
						}
						tainted[root] = true
						grew = true
					}
				}
			case *ast.CallExpr:
				sel, ok := stmt.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Decode" {
					return true
				}
				// The receiver is request-derived either inline
				// (json.NewDecoder(r.Body).Decode) or through a local the
				// AssignStmt branch already tainted (dec := json.NewDecoder(r.Body);
				// dec.Decode). isRequestDerived has no taint map, so the split
				// form is resolved here, where the map lives.
				if !isRequestDerived(stmt) && !tainted[taintRoot(sel.X)] {
					return true
				}
				for _, arg := range stmt.Args {
					un, ok := arg.(*ast.UnaryExpr)
					if !ok || un.Op != token.AND {
						continue
					}
					// taintRoot, not an Ident assertion: Decode(&cfg.Tenant)
					// taints cfg, the same generalisation the AssignStmt branch
					// applies to a selector LHS.
					root := taintRoot(un.X)
					if root == "" || root == "_" || tainted[root] {
						continue
					}
					tainted[root] = true
					grew = true
				}
			}
			return true
		})
		if !grew {
			return tainted
		}
	}
}

// taintRoot returns the identifier that owns l: l itself if it is a plain
// identifier, or the root of a selector chain (`req.Tenant` -> "req";
// `a.b.c` -> "a"). Anything else (an index expression, a dereference) is not
// resolvable here and returns "".
func taintRoot(l ast.Expr) string {
	for {
		switch x := l.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			l = x.X
		default:
			return ""
		}
	}
}

func containsRequestDerived(n ast.Node, tainted map[string]bool) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if found {
			return false
		}
		if isRequestDerived(x) {
			found = true
			return false
		}
		if id, ok := x.(*ast.Ident); ok && tainted[id.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

package lineupapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// docRouteLine matches one row of Handler's route table. The leading HTTP verb
// and the trailing "->" are both required, and that is what keeps ordinary
// prose out of the parse: the paragraph above the table says "except
// /v1/auth/*, where each handler gates itself", whose second word is a /v1/
// path. A looser rule would read that sentence as a route named "except" and
// report a drift that does not exist — a guard that cries wolf gets muted,
// which costs more than the drift it was watching for.
var docRouteLine = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE)\s+(/v1/\S*)\s+->`)

// TestHandlerDocListsEveryRoute pins Handler's doc-comment route table to the
// routes Handler actually registers (rosterbot-w200).
//
// The table drifted for months because nothing could tell it had: a doc comment
// has no failing mode of its own, so twelve routes went undocumented one merge
// at a time and the omission was only ever found by a human reading both halves
// side by side. This is the cheapest thing that can go red instead.
//
// It reads the registrations out of the AST rather than out of the running mux
// because http.ServeMux cannot enumerate its patterns — there is no runtime
// listing to ask. That is a real limitation and not a shortcut: the string
// literals this walks are the *same* literals the mux is handed, so this is
// reading the source of truth, not a restatement of it.
func TestHandlerDocListsEveryRoute(t *testing.T) {
	fset := token.NewFileSet()
	// The package directory is the test's working directory, so handler.go
	// resolves without any build-tag or module plumbing.
	f, err := parser.ParseFile(fset, "handler.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse handler.go: %v", err)
	}

	fn := findFunc(f, "Handler")
	if fn == nil {
		t.Fatal("no func Handler in handler.go; this guard reads its body and doc " +
			"comment, so a rename must be reflected here or the check silently stops running")
	}

	registered := registeredRoutes(fn)
	documented := documentedRoutes(fn)

	// Vacuity guards. Both halves are derived, so both can collapse to empty
	// without anything else in the tree noticing — a registration loop or a
	// helper (`register(mux, ...)`) would leave `registered` empty, and a
	// reformatted table would leave `documented` empty. Set equality on two
	// empty sets passes, which is exactly the shape of guard this repo has been
	// bitten by before (make check-pins treats a missing jq as a hard error for
	// the same reason). Fail loudly instead.
	if len(registered) == 0 {
		t.Fatal("found no route registrations in Handler's body. If registration " +
			"moved behind a helper or a loop, teach registeredRoutes about it — an " +
			"empty set would otherwise match an empty table and pass forever")
	}
	if len(documented) == 0 {
		t.Fatalf("found no route rows in Handler's doc comment. Rows must read "+
			"%q; see docRouteLine", "METHOD /v1/path -> description")
	}

	for _, r := range registered {
		if !slices.Contains(documented, r) {
			t.Errorf("route %q is registered but absent from Handler's doc table. "+
				"Add a row for it: the table is documented as EXHAUSTIVE, and a route "+
				"missing from it is invisible to everyone who reads the comment instead "+
				"of the code.", r)
		}
	}
	for _, d := range documented {
		if !slices.Contains(registered, d) {
			t.Errorf("route %q is documented but NOT registered. A row for a route "+
				"that does not exist is worse than a missing row — it sends a caller "+
				"after a 404 with the comment as evidence they are right.", d)
		}
	}
}

// findFunc returns the top-level function declaration named name.
func findFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// registeredRoutes collects the pattern literal of every HandleFunc call in
// fn's body.
//
// It matches on the method name alone, not on a receiver named "mux", so
// renaming the local variable cannot quietly empty the set. A non-literal
// pattern (a variable, a concatenation) is skipped and would show up as a
// missing route rather than as a pass, which is the right direction: this
// cannot verify what it cannot read.
func registeredRoutes(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
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
		out = append(out, normalizeRoute(pattern))
		return true
	})
	return out
}

// documentedRoutes collects the route rows from fn's doc comment.
func documentedRoutes(fn *ast.FuncDecl) []string {
	if fn.Doc == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(fn.Doc.Text(), "\n") {
		m := docRouteLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		out = append(out, normalizeRoute(m[1]+" "+m[2]))
	}
	return out
}

// normalizeRoute collapses the alignment padding the table uses so a row and
// its registration compare as the same string. Method and path are compared
// verbatim otherwise — including {wildcard} names, since a row naming
// {leagueID} where the mux says {id} is a small lie of exactly the kind this
// test exists to stop.
func normalizeRoute(s string) string { return strings.Join(strings.Fields(s), " ") }

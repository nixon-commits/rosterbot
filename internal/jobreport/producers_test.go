package jobreport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProducersReachTheSurfacesOnlyThroughEmit pins the seam this package
// exists for. A producer that calls notify.Send, jobwire.RecordOutput or
// reads GITHUB_STEP_SUMMARY itself has restated part of the sequence, and
// the restated copy is where the dry-run and failure rules drift (prospects
// had no dry-run guard for a season). The producers are the six job
// packages plus the two commands that record a wire result.
//
// notify.SendPushover is deliberately NOT forbidden: gs-check's league
// broadcast and the operator alerts bypass the dispatcher by design, and
// the broadcast reaches Emit as the Report's Before step.
func TestProducersReachTheSurfacesOnlyThroughEmit(t *testing.T) {
	root := filepath.Join("..", "..")
	producers := []string{
		"internal/waivers",
		"internal/claims",
		"internal/prospects",
		"internal/transactions",
		"internal/gscheck",
		"cmd/grade.go",
		"cmd/backtest.go",
	}
	forbiddenCalls := map[string]string{
		"notify.Send":          "the send is Emit's",
		"jobwire.RecordOutput": "the wire result is Report.Wire",
	}
	const forbiddenLiteral = "GITHUB_STEP_SUMMARY"

	for _, p := range producers {
		for _, file := range sourceFiles(t, filepath.Join(root, p)) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok {
							call := pkg.Name + "." + sel.Sel.Name
							if why, bad := forbiddenCalls[call]; bad {
								t.Errorf("%s: calls %s — %s", fset.Position(x.Pos()), call, why)
							}
						}
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING && strings.Contains(x.Value, forbiddenLiteral) {
						t.Errorf("%s: reads %s — the summary path is Options.SummaryPath's", fset.Position(x.Pos()), forbiddenLiteral)
					}
				}
				return true
			})
		}
	}
}

// sourceFiles lists the non-test Go files of a package directory, or the one
// file named.
func sourceFiles(t *testing.T, path string) []string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		return []string{path}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(path, name))
	}
	return files
}

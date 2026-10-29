package sleeperauth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyTheOffersCommandImportsThisPackage pins the token boundary the
// package exists for: the client that carries a full-access account token is
// reachable from exactly one command file and from no internal/* package, so
// the blast radius of the token is readable from the import graph rather
// than from a code search.
//
// DIRECT imports only, on jobwire's depcheck precedent: transitive reach is
// what the direct rule prevents.
func TestOnlyTheOffersCommandImportsThisPackage(t *testing.T) {
	const module = "github.com/nixon-commits/rosterbot"
	const self = module + "/internal/sleeperauth"

	rootOut, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m: %v\n%s", err, rootOut)
	}
	root := strings.TrimSpace(string(rootOut))

	// No internal/* package may import this one.
	cmd := exec.CommandContext(t.Context(), "go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./internal/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./internal/...: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, deps, _ := strings.Cut(line, " ")
		if pkg == self {
			continue
		}
		for _, dep := range strings.Fields(deps) {
			if dep == self {
				t.Errorf("%s imports %s — only cmd/football_offers.go may", pkg, self)
			}
		}
	}

	// Under cmd/, only the offers command (and its test) may import it.
	allowed := map[string]bool{"football_offers.go": true, "football_offers_test.go": true}
	files, err := filepath.Glob(filepath.Join(root, "cmd", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), `"`+self+`"`) && !allowed[filepath.Base(f)] {
			t.Errorf("%s imports %s — only football_offers.go may", filepath.Base(f), self)
		}
	}
}

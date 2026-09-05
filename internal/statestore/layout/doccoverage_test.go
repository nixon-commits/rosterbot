package layout

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// moduleLineRe extracts the module path declared in a go.mod's first line.
var moduleLineRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// docCoverageRepoRoot resolves the repository root from this package's own
// location (internal/statestore/layout sits three directories below it),
// mirroring TestEveryPerTenantArtifactHasAFanningOutProducer's "..", "..", ".."
// above rather than trusting the working directory `go test` happens to use.
func docCoverageRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected repo root at %s to contain go.mod: %v", root, err)
	}
	return root
}

// rootModulePath reads the module path out of the repo-root go.mod (the root
// module, not one of the nested lambda/opsnotify/infra modules).
func rootModulePath(t *testing.T, repoRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatalf("reading root go.mod: %v", err)
	}
	m := moduleLineRe.FindSubmatch(data)
	if m == nil {
		t.Fatal("root go.mod has no `module` line")
	}
	return string(m[1])
}

// internalPackageDirs returns every directory under internal/ that holds at
// least one .go file, skipping testdata directories entirely (fixture data,
// never a package).
func internalPackageDirs(t *testing.T, repoRoot string) []string {
	t.Helper()
	var dirs []string
	root := filepath.Join(repoRoot, "internal")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
				dirs = append(dirs, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}
	return dirs
}

// nonTestGoSource concatenates every *.go file in the repo EXCEPT _test.go
// files, across all four Go modules (root, lambda/, opsnotify/, infra/) plus
// cmd/ and main.go. It is used only to answer one question: is a given import
// path ever referenced from production code, as opposed to only from a
// _test.go file? That is what separates a genuine contract/test-double
// package (identitytest, cachetest, s3blobtest, ...) — never imported outside
// a test — from a package whose name coincidentally ends in "test" because
// its PARENT package's name does (s3backtest, the S3 shim for
// internal/backtest; internal/backtest itself), which ships in the real
// binary and must be documented like any other package.
func nonTestGoSource(t *testing.T, repoRoot string) string {
	t.Helper()
	skipDirNames := map[string]bool{
		".git": true, ".beads": true, ".claude": true, ".github": true, "web": true,
	}
	var sb strings.Builder
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repoRoot && (skipDirNames[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sb.Write(data)
		sb.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("scanning repo for non-test .go source: %v", err)
	}
	if sb.Len() == 0 {
		t.Fatal("scanned zero bytes of non-test Go source; the exclusion check would pass vacuously")
	}
	return sb.String()
}

// docCorpus concatenates CLAUDE.md, README.md, and every docs/**/*.md file —
// the three places this repo's own doc-updating convention (CLAUDE.md's
// README section, and "Domain docs" below it) says a feature gets described.
func docCorpus(t *testing.T, repoRoot string) string {
	t.Helper()
	files := []string{
		filepath.Join(repoRoot, "CLAUDE.md"),
		filepath.Join(repoRoot, "README.md"),
	}
	docsRoot := filepath.Join(repoRoot, "docs")
	err := filepath.WalkDir(docsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking docs/: %v", err)
	}
	var sb strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		t.Fatal("doc corpus is empty; the coverage check would pass vacuously")
	}
	return sb.String()
}

// TestEveryInternalPackageHasDocCoverage is the package-level doc-drift gate
// filed as rosterbot-jdb: internal/backtest/gs_gate_summary.go shipped with
// zero CLAUDE.md mention (rosterbot-hx5 backfilled it after the fact), and
// nothing before this test would have noticed a repeat.
//
// The check is deliberately PACKAGE-level, not symbol-level: CLAUDE.md is
// already near its size budget (see "Cutting an over-limit CLAUDE.md" in
// memory), and a symbol-level gate would demand a CLAUDE.md/README/docs
// mention of every new exported function, which is far noisier than the
// convention this repo actually follows (a package gets a paragraph; symbols
// inside it don't each need one). See docs/adr/0003-package-level-doc-coverage.md
// for the fuller decision record.
//
// A package is exempt only when its directory name ends in "test" AND it is
// never imported from a non-_test.go file anywhere in the repo — that
// combination is what distinguishes a genuine contract/test-double package
// (cachetest, identitytest, ddbusertest, s3blobtest, ...) from a package that
// merely happens to end in the letters "test" because its parent's name does
// (internal/backtest, internal/backtest/s3backtest). Naming alone is not
// enough: a plain suffix match would silently exempt s3backtest and
// internal/backtest forever, which is exactly the kind of gap this test
// exists to catch.
func TestEveryInternalPackageHasDocCoverage(t *testing.T) {
	repoRoot := docCoverageRepoRoot(t)
	modulePath := rootModulePath(t, repoRoot)
	dirs := internalPackageDirs(t, repoRoot)
	if len(dirs) == 0 {
		t.Fatal("found zero package directories under internal/; the walk is broken")
	}
	nonTestSrc := nonTestGoSource(t, repoRoot)
	docs := docCorpus(t, repoRoot)

	var missing []string
	for _, dir := range dirs {
		rel, err := filepath.Rel(repoRoot, dir)
		if err != nil {
			t.Fatalf("relativizing %s: %v", dir, err)
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(dir)

		if strings.HasSuffix(base, "test") {
			importPath := modulePath + "/" + rel
			if !strings.Contains(nonTestSrc, `"`+importPath+`"`) {
				// Never imported outside a _test.go file: a contract/test-double
				// package, exempt by convention (see doc comment above).
				continue
			}
		}

		if !strings.Contains(docs, rel) {
			missing = append(missing, rel)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("package(s) with no CLAUDE.md / README.md / docs/**/*.md mention of their "+
			"path:\n  - %s\n\n"+
			"Add a sentence naming the package path to CLAUDE.md (internals worth a "+
			"paragraph), README.md (user-facing commands/flags), or the relevant "+
			"docs/*.md file. See docs/adr/0003-package-level-doc-coverage.md.",
			strings.Join(missing, "\n  - "))
	}
}

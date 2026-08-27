package lineupapi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/infralistertest"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// TestFileInfraStore_Conformance holds the local lister to the shared
// key-grammar contract. The file side resolves an S3 prefix to its local
// directory through the layout table, so the seed writes under the Analysis
// artifact's LocalDir — the same mapping localDirFor applies on read.
func TestFileInfraStore_Conformance(t *testing.T) {
	infralistertest.Run(t, func(t *testing.T) (lineupapi.InfraLister, string, infralistertest.Seed) {
		root := t.TempDir()
		art := layout.Analysis
		seed := func(remainder string, data []byte) {
			t.Helper()
			path := filepath.Join(root, art.LocalDir, filepath.FromSlash(remainder))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("seed mkdir: %v", err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("seed write: %v", err)
			}
		}
		return lineupapi.NewFileInfraStore(root), art.S3Prefix, seed
	})
}

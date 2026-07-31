package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupgap"
)

func TestRenderGapSite_WritesModelFromStore(t *testing.T) {
	stateDir := t.TempDir()
	outDir := t.TempDir()

	w := lineupgap.NewFileWriter(stateDir)
	d := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGaps(d, []lineupgap.Row{{
		Dt: "2026-07-20", ActualPts: 90, OptimalPts: 100, Gap: -10, StartedN: 13, BenchedN: 2,
	}}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := writeGapModel(lineupgap.NewFileReader(stateDir), outDir); err != nil {
		t.Fatalf("writeGapModel: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(outDir, "gap.json"))
	if err != nil {
		t.Fatalf("read gap.json: %v", err)
	}
	var m lineupgap.Model
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("gap.json is not valid JSON: %v", err)
	}
	if m.LatestDate != "2026-07-20" {
		t.Errorf("LatestDate = %q, want 2026-07-20", m.LatestDate)
	}
	if len(m.Series) != 1 || m.Series[0].GapPts != -10 {
		t.Errorf("Series = %+v, want one point with gap -10", m.Series)
	}
}

// A fresh deploy has an empty store. That must still produce a valid file, so
// the dashboard's headline block renders "no data yet" instead of erroring.
func TestRenderGapSite_EmptyStoreStillWritesValidJSON(t *testing.T) {
	outDir := t.TempDir()
	if err := writeGapModel(lineupgap.NewFileReader(t.TempDir()), outDir); err != nil {
		t.Fatalf("writeGapModel on empty store: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(outDir, "gap.json"))
	if err != nil {
		t.Fatalf("read gap.json: %v", err)
	}
	var m lineupgap.Model
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("gap.json is not valid JSON: %v", err)
	}
	if len(m.Series) != 0 {
		t.Errorf("Series = %d points, want 0", len(m.Series))
	}
	if _, ok := m.Windows["30"]; !ok {
		t.Error(`Windows["30"] must be present even when empty`)
	}
}

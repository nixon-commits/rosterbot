package claims

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/waivers"
)

func TestBuildAndWriteLedger(t *testing.T) {
	day := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	moves := []Move{
		{TeamName: "Aces", TeamID: "t1", ClaimType: "WW", BidAmount: "12", Priority: "3",
			Added:   []SidePlayer{{Name: "Added Guy", Position: "OF", MLBAMID: 99, Value: 3000, Rank: 120, Signal: waivers.SignalHot, ProjectedFPG: 4.2}},
			Dropped: []SidePlayer{{Name: "Dropped Guy", Value: 1000}}},
	}
	led := BuildLedger(day, moves)
	if led.Date != "2026-06-12" || len(led.Entries) != 1 {
		t.Fatalf("unexpected ledger: %+v", led)
	}
	e := led.Entries[0]
	if e.Added.Signal != "HOT" || e.Added.ProjectedFPG != 4.2 || e.NetValue != 2000 {
		t.Errorf("unexpected entry: %+v", e)
	}

	dir := t.TempDir()
	if err := WriteLedger(dir, led); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "2026-06-12.json"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var round Ledger
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(round.Entries) != 1 || round.Entries[0].Team != "Aces" {
		t.Errorf("round-trip mismatch: %+v", round)
	}
}

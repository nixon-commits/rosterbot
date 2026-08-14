//go:build diag

package hkb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pmurley/go-fantrax/models"
)

// TestDiagMinorsVsLevel cross-tabs Fantrax's MinorsEligible flag against HKB's
// Level for every rostered player whose normalized name resolves to exactly one
// HKB row. It answers the question resolveCollision's level signal rests on:
// does minors-eligibility actually predict a non-MLB HKB level, or do the two
// mean different enough things that the implication only holds one way?
//
//	go test -tags diag -run TestDiagMinorsVsLevel -v ./internal/hkb/
func TestDiagMinorsVsLevel(t *testing.T) {
	var hkbEnv struct {
		Data []Player `json:"data"`
	}
	readCache(t, "../../.cache/hkb-players.json", &hkbEnv)

	pools, _ := filepath.Glob("../../.cache/fantrax-player-pool-*.json")
	if len(pools) == 0 {
		t.Skip("no cached player pool")
	}
	var poolEnv struct {
		Data []models.PoolPlayer `json:"data"`
	}
	readCache(t, pools[0], &poolEnv)

	l := BuildLookup(hkbEnv.Data)

	counts := map[string]int{}
	var minorsAtMLB, mlbAtMinors []string
	for _, pp := range poolEnv.Data {
		if pp.FantasyTeamID == "" {
			continue // rostered players only — that is what teamvalue joins
		}
		hp, ok := l.Find(pp.Name)
		if !ok {
			continue // absent or contested: not a clean observation
		}
		key := "minors=false"
		if pp.MinorsEligible {
			key = "minors=true "
		}
		counts[key+" level="+orNone(hp.Level)]++

		if pp.MinorsEligible && hp.Level == "MLB" {
			minorsAtMLB = append(minorsAtMLB, pp.Name)
		}
		if !pp.MinorsEligible && hp.Level != "MLB" && hp.Level != "" {
			mlbAtMinors = append(mlbAtMinors, pp.Name+" ("+hp.Level+")")
		}
	}

	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-28s %d", k, counts[k])
	}

	sort.Strings(minorsAtMLB)
	sort.Strings(mlbAtMinors)
	t.Logf("VIOLATES (minors=true  => level != MLB): %d  %v", len(minorsAtMLB), minorsAtMLB)
	t.Logf("VIOLATES (minors=false => level == MLB): %d  %v", len(mlbAtMinors), mlbAtMinors)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func readCache(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("cache unavailable: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

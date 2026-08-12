//go:build diag

package playername

import (
	"testing"
	"time"
)

// TestDiagAB runs the production resolver twice, back to back, in one process,
// to establish whether the 97-name drop is reproducible or transient.
func TestDiagAB(t *testing.T) {
	names := diagNames(t)
	for i := 1; i <= 3; i++ {
		start := time.Now()
		rp, err := ResolveMLBAMIDsNoCache(names)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var missing []string
		for _, n := range names {
			if id, ok := rp.ByName[Normalize(n)]; !ok || id == 0 {
				missing = append(missing, n)
			}
		}
		t.Logf("run %d: %d indexed, %d variants, %d/%d UNRESOLVED, took %s",
			i, len(rp.ByID), len(rp.ByName), len(missing), len(names),
			time.Since(start).Round(time.Millisecond))
		if len(missing) > 0 && len(missing) <= 12 {
			t.Logf("   misses: %v", missing)
		}
		time.Sleep(2 * time.Second)
	}
}

//go:build diag

package playername

import (
	"encoding/json"
	"os"
	"testing"
)

type sleeperPlayer struct {
	FullName       string `json:"full_name"`
	SearchFullName string `json:"search_full_name"`
	FirstName      string `json:"first_name"`
	LastName       string `json:"last_name"`
	Team           string `json:"team"`
	Status         string `json:"status"`
	Position       string `json:"position"`
	SwishID        *int   `json:"swish_id"`
}

// TestDiagSleeperParity compares Sleeper's MLB dump against the production
// statsapi resolver over a real Fantrax name set (rosterbot-txw).
func TestDiagSleeperParity(t *testing.T) {
	names := diagNames(t)
	path := os.Getenv("SLEEPER_JSON")
	if path == "" {
		t.Skip("SLEEPER_JSON unset")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sleeper dump: %v", err)
	}
	var dump map[string]sleeperPlayer
	if err := json.Unmarshal(raw, &dump); err != nil {
		t.Fatalf("decode sleeper dump: %v", err)
	}
	t.Logf("sleeper dump: %d players", len(dump))

	// Index Sleeper by our own Normalize() over its full name, plus its own
	// search_full_name (spaces already stripped) as a second key.
	byName := map[string]sleeperPlayer{}
	var withID int
	for _, p := range dump {
		if p.SwishID != nil {
			withID++
		}
		if k := Normalize(p.FullName); k != "" {
			byName[k] = p
		}
	}
	t.Logf("sleeper players carrying swish_id (MLBAM): %d/%d (%.1f%%)",
		withID, len(dump), 100*float64(withID)/float64(len(dump)))

	// Production resolver, for comparison.
	rp, err := ResolveMLBAMIDsNoCache(names)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}

	var bothAgree, bothDisagree, onlyStats, onlySleeper, neither int
	var sleeperNamedNoID []string
	var sleeperMissing []string
	for _, n := range names {
		statsID, statsOK := rp.ByName[Normalize(n)]
		statsOK = statsOK && statsID != 0

		sp, nameOK := byName[Normalize(n)]
		var sleepID int
		if nameOK && sp.SwishID != nil {
			sleepID = *sp.SwishID
		}
		sleepOK := sleepID != 0

		if nameOK && !sleepOK {
			sleeperNamedNoID = append(sleeperNamedNoID, n+" ["+sp.Status+"/"+sp.Team+"]")
		}
		if !nameOK {
			sleeperMissing = append(sleeperMissing, n)
		}

		switch {
		case statsOK && sleepOK && statsID == sleepID:
			bothAgree++
		case statsOK && sleepOK:
			bothDisagree++
			t.Logf("   DISAGREE %q statsapi=%d sleeper=%d", n, statsID, sleepID)
		case statsOK:
			onlyStats++
		case sleepOK:
			onlySleeper++
			t.Logf("   SLEEPER-ONLY WIN %q -> %d", n, sleepID)
		default:
			neither++
		}
	}

	t.Logf("=== PARITY over %d Fantrax names ===", len(names))
	t.Logf("  both resolve, IDs agree     : %d", bothAgree)
	t.Logf("  both resolve, IDs DISAGREE  : %d", bothDisagree)
	t.Logf("  statsapi only               : %d", onlyStats)
	t.Logf("  sleeper only                : %d", onlySleeper)
	t.Logf("  neither                     : %d", neither)
	t.Logf("--- sleeper name-table coverage ---")
	t.Logf("  name present but NO swish_id: %d", len(sleeperNamedNoID))
	for _, s := range sleeperNamedNoID {
		t.Logf("      %s", s)
	}
	t.Logf("  name absent from sleeper    : %d %v", len(sleeperMissing), sleeperMissing)
}

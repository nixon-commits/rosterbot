//go:build diag

package lineuprun

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

// TestDiagHKBJoin is a throwaway diagnostic (build tag `diag`, never runs in CI).
// It answers the question a coverage count cannot: does the normalized join key
// COLLIDE, so that a lineup player could silently receive another player's age
// and value? Run with:
//
//	go test -tags diag -run TestDiagHKBJoin -v ./internal/lineuprun/
func TestDiagHKBJoin(t *testing.T) {
	players, err := hkb.GetPlayers(t.Context(), ".cache")
	if err != nil {
		t.Fatalf("hkb: %v", err)
	}
	t.Logf("HKB players: %d", len(players))

	byKey := map[string][]hkb.Player{}
	for _, p := range players {
		k := playername.Normalize(p.Name)
		byKey[k] = append(byKey[k], p)
	}
	t.Logf("distinct normalized keys: %d", len(byKey))

	var colliding []string
	for k, ps := range byKey {
		if len(ps) > 1 {
			colliding = append(colliding, k)
		}
	}
	sort.Strings(colliding)
	t.Logf("COLLIDING KEYS: %d", len(colliding))
	for _, k := range colliding {
		for _, p := range byKey[k] {
			t.Logf("  [%s] %-28s age=%-5.1f value=%-6d team=%-4s level=%s prospect=%v",
				k, p.Name, p.Age, p.Value, p.Team, p.Level, p.Prospect)
		}
	}

	// Now: are any of MY published lineup's players sitting on a colliding key?
	raw, err := os.ReadFile("../../.lineup/lineup-today.json")
	if err != nil {
		t.Skipf("no published lineup: %v", err)
	}
	var doc struct {
		Slots []struct {
			Player *struct {
				Name  string  `json:"name"`
				Age   float64 `json:"age"`
				Value int     `json:"hkb_value"`
			} `json:"player"`
		} `json:"slots"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("lineup json: %v", err)
	}

	collides := map[string]bool{}
	for _, k := range colliding {
		collides[k] = true
	}

	var affected, checked int
	for _, s := range doc.Slots {
		if s.Player == nil {
			continue
		}
		checked++
		k := playername.Normalize(s.Player.Name)
		if collides[k] {
			affected++
			t.Errorf("LINEUP PLAYER ON COLLIDING KEY: %s (key=%s)", s.Player.Name, k)
		}
		// Verify the published value round-trips to a real HKB row.
		ps, ok := byKey[k]
		if !ok {
			t.Errorf("LINEUP PLAYER NOT IN HKB: %s (key=%s)", s.Player.Name, k)
			continue
		}
		if ps[0].Age != s.Player.Age || ps[0].Value != s.Player.Value {
			t.Errorf("MISMATCH %s: published age=%.1f value=%d, hkb age=%.1f value=%d",
				s.Player.Name, s.Player.Age, s.Player.Value, ps[0].Age, ps[0].Value)
		}
	}
	t.Logf("lineup players checked: %d, on colliding keys: %d", checked, affected)
}

package lineuprun

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// The map is keyed by playername.Normalize and looked up by
// projections.NormalizeName (a wrapper over it) inside lineupapi.Build. This
// test drives the real lookup side rather than comparing key strings, so the
// two cannot drift apart the way a hand-built key elsewhere in this repo did
// (see InvalidatePeriodRosterCache / rosterbot-sza).
//
// The names below are the spellings that actually differ between HKB and
// Fantrax — accents, a suffix comma, a hyphen — because those are the cases
// where a miss produces no error at all, just an empty cell.
func TestHKBMetaJoinsFantraxSpellings(t *testing.T) {
	meta := hkbMeta([]hkb.Player{
		{Name: "Vladimir Guerrero Jr.", Age: 27.3, Value: 6100},
		{Name: "José Ramírez", Age: 33.0, Value: 4200},
		{Name: "Ha-Seong Kim", Age: 30.4, Value: 900},
		{Name: "Julio Rodríguez", Age: 25.7, Value: 9240},
	})

	// The left column is how Fantrax spells the name in a roster row.
	cases := []struct {
		fantraxName string
		wantAge     float64
		wantValue   int
	}{
		{"Vladimir Guerrero, Jr.", 27.3, 6100},
		{"Jose Ramirez", 33.0, 4200},
		{"Ha Seong Kim", 30.4, 900},
		{"Julio Rodriguez", 25.7, 9240},
	}
	for _, tc := range cases {
		got, ok := meta[projections.NormalizeName(tc.fantraxName)]
		if !ok {
			t.Errorf("%q: no HKB match — the age and value columns would be blank for this player", tc.fantraxName)
			continue
		}
		if got.Age != tc.wantAge || got.Value != tc.wantValue {
			t.Errorf("%q = age %v value %d, want %v / %d", tc.fantraxName, got.Age, got.Value, tc.wantAge, tc.wantValue)
		}
	}

	if _, ok := meta[projections.NormalizeName("Nobody McNobody")]; ok {
		t.Error("unrostered name matched; a miss must stay a miss so the wire fields stay absent")
	}
}

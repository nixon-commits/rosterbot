package hkb

import "testing"

func testPicks() []Player {
	return []Player{
		{Name: "2027 Early 1st", AssetType: "PICK", Value: 1456},
		{Name: "2027 Mid 1st", AssetType: "PICK", Value: 821},
		{Name: "2027 Late 1st", AssetType: "PICK", Value: 778},
		{Name: "2027 Early 2nd", AssetType: "PICK", Value: 390},
		{Name: "2028 Early 1st", AssetType: "PICK", Value: 1226},
		// Not a pick -- a same-named player would be a data anomaly, but the
		// AssetType filter is what actually protects PickAverage from it.
		{Name: "2027 Early 1st", AssetType: "", Value: 1},
	}
}

func TestPickAverage_AveragesEveryTierFound(t *testing.T) {
	avg, tiers := PickAverage(testPicks(), 2027, 1)
	wantTiers := 3
	wantAvg := (1456 + 821 + 778) / 3
	if tiers != wantTiers {
		t.Errorf("tiers = %d, want %d", tiers, wantTiers)
	}
	if avg != wantAvg {
		t.Errorf("avg = %d, want %d", avg, wantAvg)
	}
}

func TestPickAverage_SingleTier(t *testing.T) {
	avg, tiers := PickAverage(testPicks(), 2027, 2)
	if tiers != 1 || avg != 390 {
		t.Errorf("got avg=%d tiers=%d, want avg=390 tiers=1", avg, tiers)
	}
}

// A year HKB no longer lists (its draft has already happened) must report
// zero tiers, not silently average nothing to zero -- callers rely on this
// to leave the asset unpriced rather than valuing it at 0.
func TestPickAverage_UnknownYearOrRoundReportsZeroTiers(t *testing.T) {
	if avg, tiers := PickAverage(testPicks(), 2026, 1); tiers != 0 || avg != 0 {
		t.Errorf("2026 1st: got avg=%d tiers=%d, want 0/0 -- HKB doesn't list a resolved draft", avg, tiers)
	}
	if avg, tiers := PickAverage(testPicks(), 2027, 4); tiers != 0 || avg != 0 {
		t.Errorf("2027 4th: got avg=%d tiers=%d, want 0/0 -- HKB only prices rounds 1-3", avg, tiers)
	}
}

func TestOrdinal(t *testing.T) {
	cases := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th"}
	for round, want := range cases {
		if got := Ordinal(round); got != want {
			t.Errorf("Ordinal(%d) = %q, want %q", round, got, want)
		}
	}
}

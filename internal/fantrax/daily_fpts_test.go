package fantrax

import (
	"testing"

	"github.com/pmurley/go-fantrax/models"
)

func TestPlayerStatsFromTables_BothGroups(t *testing.T) {
	tables := []models.RosterTable{
		{
			SCGroup: "10", // hitting
			Header: models.TableHeader{
				Cells: []models.Column{
					{ShortName: "GP"},
					{ShortName: "FPts", Key: "fpts"},
				},
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "Judge", ScorerID: "h1", TeamShortName: "NYY", PosShortNames: "OF"}, StatusID: "1", PosID: "012", Cells: []models.Cell{{Content: "10"}, {Content: "55.5"}}},
				{Scorer: models.Player{Name: "Bench Bat", ScorerID: "h2", TeamShortName: "BOS", PosShortNames: "1B"}, StatusID: "2", PosID: "", Cells: []models.Cell{{Content: "8"}, {Content: "30.0"}}},
				{IsEmptyRosterSlot: true}, // skip
			},
		},
		{
			SCGroup: float64(20), // pitching
			Header: models.TableHeader{
				Cells: []models.Column{
					{ShortName: "GS"},
					{ShortName: "GP"},
					{ShortName: "FPts", Key: "fpts"},
				},
			},
			Rows: []models.PlayerRow{
				{Scorer: models.Player{Name: "Ace", ScorerID: "p1", TeamShortName: "LAD", PosShortNames: "SP"}, StatusID: "1", PosID: "015", Cells: []models.Cell{{Content: "3"}, {Content: "3"}, {Content: "42.0"}}},
				{Scorer: models.Player{Name: "Closer", ScorerID: "p2", TeamShortName: "ATL", PosShortNames: "RP"}, StatusID: "1", PosID: "016", Cells: []models.Cell{{Content: "0"}, {Content: "6"}, {Content: "18.5"}}},
			},
		},
		{
			SCGroup: "5", // unknown group — skip
			Header:  models.TableHeader{Cells: []models.Column{{Key: "fpts"}}},
			Rows:    []models.PlayerRow{{Scorer: models.Player{Name: "Ghost", ScorerID: "g1"}, Cells: []models.Cell{{Content: "99"}}}},
		},
	}

	snap := playerStatsFromTables(tables)
	if len(snap.Hitters) != 2 {
		t.Fatalf("expected 2 hitters, got %d", len(snap.Hitters))
	}
	if len(snap.Pitchers) != 2 {
		t.Fatalf("expected 2 pitchers, got %d", len(snap.Pitchers))
	}
	if snap.Hitters["h1"].FPts != 55.5 {
		t.Errorf("h1 FPts = %v, want 55.5", snap.Hitters["h1"].FPts)
	}
	if snap.Hitters["h1"].GP != 10 {
		t.Errorf("h1 GP = %d, want 10", snap.Hitters["h1"].GP)
	}
	if snap.Hitters["h1"].StatusID != "1" {
		t.Errorf("h1 StatusID = %q, want 1", snap.Hitters["h1"].StatusID)
	}
	if snap.Hitters["h1"].SlotPosID != "012" {
		t.Errorf("h1 SlotPosID = %q, want 012", snap.Hitters["h1"].SlotPosID)
	}
	if snap.Pitchers["p1"].FPts != 42.0 {
		t.Errorf("p1 FPts = %v, want 42.0", snap.Pitchers["p1"].FPts)
	}
	if !snap.Pitchers["p1"].IsPitcher {
		t.Errorf("p1 IsPitcher should be true")
	}
	if snap.Hitters["h1"].IsPitcher {
		t.Errorf("h1 IsPitcher should be false")
	}
}

func TestDiffYTD_DeltaExtraction(t *testing.T) {
	prev := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Judge", FPts: 20.0, GP: 5, StatusID: "1"},
	}
	cur := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Judge", FPts: 32.5, GP: 6, StatusID: "1"},
	}

	got := diffYTD(cur, prev, nil, false)
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].FPts != 12.5 {
		t.Errorf("FPts delta = %v, want 12.5", got[0].FPts)
	}
	if !got[0].HadGame {
		t.Errorf("HadGame should be true (FPts advanced)")
	}
	if !got[0].Active {
		t.Errorf("Active should be true (StatusID=1)")
	}
}

func TestDiffYTD_FirstAppearanceZeroed(t *testing.T) {
	// Player appears mid-period with big pre-period YTD (e.g., waiver pickup).
	// First-appearance delta is zeroed so pre-team production isn't credited as
	// today's points, and NeedsBackfill is set so BackfillDailyFPts can compute
	// the real same-day FPts from the MLB statsapi game log.
	cur := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Pickup", FPts: 120.0, GP: 40, StatusID: "1"},
	}

	got := diffYTD(cur, map[string]playerYTD{}, nil, false)
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].FPts != 0 {
		t.Errorf("first-appearance FPts should be 0, got %v", got[0].FPts)
	}
	if got[0].HadGame {
		t.Errorf("HadGame should be false on day-of-acquisition (no signal)")
	}
	if !got[0].NeedsBackfill {
		t.Errorf("NeedsBackfill should be true so backfill can compute real FPts")
	}
}

func TestDiffYTD_TwoWayPlayerCrossesKinds(t *testing.T) {
	// Ohtani-style: yesterday Fantrax classified him as a hitter; today as a
	// pitcher. The hitter YTD and pitcher YTD are role-specific season totals
	// that can't be meaningfully subtracted from each other — doing so produces
	// a phantom delta. diffYTD detects the crossing (prevOther fallback fired)
	// and zeroes the delta + flags NeedsBackfill so MLB statsapi can compute
	// the real same-day points. End-to-end verification lives in the
	// BackfillDailyFPts tests in mlb_backfill_test.go.
	prevHitters := map[string]playerYTD{
		"ohtani": {PlayerID: "ohtani", Name: "Ohtani", FPts: 250.0, GP: 30, StatusID: "1"},
	}
	prevPitchers := map[string]playerYTD{}
	curPitchers := map[string]playerYTD{
		"ohtani": {PlayerID: "ohtani", Name: "Ohtani", FPts: 275.0, GP: 31, StatusID: "1"},
	}
	got := diffYTD(curPitchers, prevPitchers, prevHitters, true)
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if got[0].FPts != 0 {
		t.Errorf("cross-kind delta = %v, want 0 (subtraction is meaningless across roles)", got[0].FPts)
	}
	if got[0].HadGame {
		t.Errorf("HadGame should be false pre-backfill (delta is zeroed)")
	}
	if !got[0].NeedsBackfill {
		t.Errorf("NeedsBackfill should be true so backfill can compute real FPts")
	}
}

func TestDiffYTD_ZeroPointGameDetected(t *testing.T) {
	// Player played today but earned exactly 0 FPts (0-for-4 with no Ks = 0 pts
	// depending on scoring). GP advanced — HadGame should still be true.
	prev := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Slumper", FPts: 10.0, GP: 5, StatusID: "1"},
	}
	cur := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Slumper", FPts: 10.0, GP: 6, StatusID: "1"},
	}
	got := diffYTD(cur, prev, nil, false)
	if got[0].FPts != 0 {
		t.Errorf("FPts delta should be 0, got %v", got[0].FPts)
	}
	if !got[0].HadGame {
		t.Errorf("HadGame should be true when GP advances")
	}
}

func TestDiffYTD_NoGameDay(t *testing.T) {
	// Same YTD both days — no game.
	prev := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "OffDay", FPts: 10.0, GP: 5, StatusID: "1"},
	}
	cur := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "OffDay", FPts: 10.0, GP: 5, StatusID: "1"},
	}
	got := diffYTD(cur, prev, nil, false)
	if got[0].FPts != 0 {
		t.Errorf("FPts delta should be 0, got %v", got[0].FPts)
	}
	if got[0].HadGame {
		t.Errorf("HadGame should be false on off day")
	}
}

func TestDiffYTD_ReserveToActiveMidPeriod(t *testing.T) {
	// Player was on bench yesterday, moved to active today with positive delta.
	prev := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Mover", FPts: 15.0, GP: 5, StatusID: "2"},
	}
	cur := map[string]playerYTD{
		"h1": {PlayerID: "h1", Name: "Mover", FPts: 22.0, GP: 6, StatusID: "1"},
	}
	got := diffYTD(cur, prev, nil, false)
	if got[0].FPts != 7.0 {
		t.Errorf("FPts delta = %v, want 7.0", got[0].FPts)
	}
	if !got[0].Active {
		t.Errorf("Active should reflect today's StatusID (now 1)")
	}
}

func TestDiffYTD_TwoWayPlayerCycling(t *testing.T) {
	// Ohtani-style: hitter YTD retained across days he doesn't appear in hitter table.
	// diffYTD only handles one group at a time; the retention across days is done
	// by the caller (DailyFantasyPoints) by keeping prev maps alive. This test
	// verifies that when a player reappears with a larger YTD, the delta uses
	// the prior YTD (not zero), so there's no over-count.
	prev := map[string]playerYTD{
		"o1": {PlayerID: "o1", Name: "Ohtani", FPts: 50.0, GP: 8, StatusID: "1"},
	}
	cur := map[string]playerYTD{
		"o1": {PlayerID: "o1", Name: "Ohtani", FPts: 62.0, GP: 9, StatusID: "1"},
	}
	got := diffYTD(cur, prev, nil, false)
	if got[0].FPts != 12.0 {
		t.Errorf("two-way delta = %v, want 12.0 (not 62.0)", got[0].FPts)
	}
}

func TestIsScGroup(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int
		ok   bool
	}{
		{"10", 10, true},
		{"20", 20, true},
		{float64(20), 20, true},
		{int(10), 10, true},
		{"bogus", 10, false},
		{"20", 10, false},
		{nil, 10, false},
	}
	for _, c := range cases {
		got := isScGroup(c.in, c.want)
		if got != c.ok {
			t.Errorf("isScGroup(%v, %d) = %v, want %v", c.in, c.want, got, c.ok)
		}
	}
}

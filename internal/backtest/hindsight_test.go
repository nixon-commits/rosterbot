package backtest

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// A negative-scoring player is never worth seating: benching them yields
// exactly 0, so the hindsight optimum must ignore them rather than let the
// optimizer's fill-every-slot behaviour drag the total down. Without the
// clamp the optimizer pulls the -6 reserve into the free UT slot (the only
// other candidate with a game) and reports optimal=4 against actual=10 — a
// mathematically impossible positive gap.
func TestRunLineupAnalysis_NegativeReserveNotForcedIntoEmptySlot(t *testing.T) {
	day := fantrax.DayRoster{
		Date:   time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Period: 22,
		Players: []fantrax.DayPlayerFP{
			activeHitter("h_good", "Good OF", "NYY", 10.0, []string{"012"}),
			benchHitter("h_bad", "Bad Bench", "BOS", -6.0, []string{"012"}),
		},
	}

	r := RunLineupAnalysis([]fantrax.DayRoster{day}, hitterSlotsForTest(), pitcherSlotsForTest())[0]
	if r.ActualPts != 10.0 {
		t.Errorf("ActualPts = %v, want 10.0", r.ActualPts)
	}
	if r.OptimalPts != 10.0 {
		t.Errorf("OptimalPts = %v, want 10.0", r.OptimalPts)
	}
	if math.Abs(r.Gap) > 1e-9 {
		t.Errorf("Gap = %v, want 0", r.Gap)
	}
}

// The same defect via the other route: an *active* negative scorer the
// optimizer has no better candidate for. Benching them is always available,
// so optimal must exclude their loss even though actual includes it.
func TestRunLineupAnalysis_NegativeStarterExcludedFromOptimal(t *testing.T) {
	day := fantrax.DayRoster{
		Date:   time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
		Period: 22,
		Players: []fantrax.DayPlayerFP{
			activeHitter("h_good", "Good OF", "NYY", 7.0, []string{"012"}),
			{
				PlayerID: "h_bad", Name: "Bad UT", MLBTeam: "HOU",
				PosShortNames: "UT", Positions: []string{"014"},
				SlotPosID: "014", StatusID: "1", FPts: -4.0,
				Active: true, HadGame: true,
			},
		},
	}

	r := RunLineupAnalysis([]fantrax.DayRoster{day}, hitterSlotsForTest(), pitcherSlotsForTest())[0]
	if r.ActualPts != 3.0 {
		t.Errorf("ActualPts = %v, want 3.0", r.ActualPts)
	}
	if r.OptimalPts != 7.0 {
		t.Errorf("OptimalPts = %v, want 7.0", r.OptimalPts)
	}
	if math.Abs(r.Gap-(-4.0)) > 1e-9 {
		t.Errorf("Gap = %v, want -4.0", r.Gap)
	}
}

// guardOptimal is the last line of defence: a positive gap means the hindsight
// optimizer failed to reconstruct a lineup it provably could have set, so we
// report the tightest correct lower bound (actual) rather than a green day.
func TestGuardOptimal(t *testing.T) {
	date := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name            string
		actual, optimal float64
		want            float64
	}{
		{"optimal above actual is untouched", 48, 51, 51},
		{"equal is untouched", 48, 48, 48},
		{"float noise is not clamped as a violation", 48, 48 - 1e-12, 48 - 1e-12},
		{"optimal below actual clamps up to actual", 48, 41, 48},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := guardOptimal(date, tc.actual, tc.optimal); math.Abs(got-tc.want) > 1e-15 {
				t.Errorf("guardOptimal(%v, %v) = %v, want %v", tc.actual, tc.optimal, got, tc.want)
			}
		})
	}
}

// --- Production regression fixture ---

type dayFixture struct {
	Days         []fantrax.DayRoster `json:"days"`
	HitterSlots  []fantrax.Slot      `json:"hitter_slots"`
	PitcherSlots []fantrax.Slot      `json:"pitcher_slots"`
}

// The real 2026-07-02 roster that produced Gap=+7.0 in the Lineup Gap Store.
// Hitters: seven positive scorers total 26 (Jake Bauers, -1, must not be
// seated). Pitchers: Chase Burns 25, with Roki Sasaki (-9, on the bench in
// real life) pulled into one of the five free P slots by the old code.
func TestRunLineupAnalysis_July2ProductionDay(t *testing.T) {
	b, err := os.ReadFile("testdata/day-2026-07-02.json")
	if err != nil {
		t.Fatal(err)
	}
	var f dayFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}

	r := RunLineupAnalysis(f.Days, f.HitterSlots, f.PitcherSlots)[0]
	if r.ActualPts != 48.0 {
		t.Errorf("ActualPts = %v, want 48.0", r.ActualPts)
	}
	if r.OptimalPts != 51.0 {
		t.Errorf("OptimalPts = %v, want 51.0 (26 hitters + 25 pitchers)", r.OptimalPts)
	}
	if r.Gap > 0 {
		t.Errorf("Gap = %v, want <= 0 (a positive gap is impossible by construction)", r.Gap)
	}
}

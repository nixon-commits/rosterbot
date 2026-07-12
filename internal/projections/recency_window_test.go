package projections

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func TestWindowedRecent_GroupsByPlayerAppliesWindowAndFiltersRole(t *testing.T) {
	days := []fantrax.DayRoster{
		{Date: day("2026-04-01"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 0, HadGame: true, IsPitcher: false}, // old, out of 7d window
		}},
		{Date: day("2026-05-29"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 4, HadGame: true, IsPitcher: false},
			{PlayerID: "p1", FPts: 9, HadGame: true, IsPitcher: true},
		}},
		{Date: day("2026-05-30"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 6, HadGame: true, IsPitcher: false},
		}},
	}

	// Hitters, 7-day window as of 2026-05-31: h1's Apr 1 game is out of window,
	// May 29 + 30 in → (4+6)/2 = 5 over 2 games. Pitcher p1 excluded (wantPitchers=false).
	got := WindowedRecent(days, day("2026-05-31"), WindowWeight(7), false)
	if _, ok := got["p1"]; ok {
		t.Fatalf("pitcher leaked into hitter windowed recent")
	}
	h := got["h1"]
	if math.Abs(h.FPtsPerGame-5.0) > 1e-9 {
		t.Fatalf("h1 FPtsPerGame = %v, want 5.0", h.FPtsPerGame)
	}
	if h.GamesPlayed != 2 {
		t.Fatalf("h1 GamesPlayed = %d, want 2 (Apr 1 out of window)", h.GamesPlayed)
	}
}

func TestWindowedRecent_PitchersOnlyWhenRequested(t *testing.T) {
	days := []fantrax.DayRoster{
		{Date: day("2026-05-29"), Players: []fantrax.DayPlayerFP{
			{PlayerID: "h1", FPts: 4, HadGame: true, IsPitcher: false},
			{PlayerID: "p1", FPts: 8, HadGame: true, IsPitcher: true},
			{PlayerID: "p1", FPts: 12, HadGame: true, IsPitcher: true}, // second outing same day
		}},
	}
	got := WindowedRecent(days, day("2026-05-31"), YTDWeight, true)
	if _, ok := got["h1"]; ok {
		t.Fatalf("hitter leaked into pitcher windowed recent")
	}
	if got["p1"].GamesPlayed != 2 {
		t.Fatalf("p1 GamesPlayed = %d, want 2", got["p1"].GamesPlayed)
	}
}

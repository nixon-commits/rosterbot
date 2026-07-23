package lineuprun

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeRecentClient serves a fixed daily series. seasonStart is passed non-zero
// by the test, so GetSeasonDateRange is never called; it errors if it is, to
// catch accidental use.
type fakeRecentClient struct {
	days []fantrax.DayRoster
}

func (f *fakeRecentClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, errors.New("GetSeasonDateRange should not be called when seasonStart is set")
}
func (f *fakeRecentClient) DailyFantasyPoints(_ string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	return f.days, nil
}
func (f *fakeRecentClient) BackfillDailyFPts([]fantrax.DayRoster) error { return nil }

func hitterDay(d time.Time, playerID string, fp float64) fantrax.DayRoster {
	return fantrax.DayRoster{Date: d, Players: []fantrax.DayPlayerFP{
		{PlayerID: playerID, IsPitcher: false, HadGame: true, FPts: fp},
	}}
}

// windowedHitterRecent collapses the daily series into per-player FP/game +
// games-in-window (trailing 30d, WindowWeight uniform 1 within the window,
// leakage guard excludes the as-of day).
func TestWindowedHitterRecent_WindowMean(t *testing.T) {
	today := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	days := []fantrax.DayRoster{
		hitterDay(today.AddDate(0, 0, -2), "p1", 10), // in window, played
		hitterDay(today.AddDate(0, 0, -1), "p1", 20), // in window, played
		hitterDay(today, "p1", 999),                  // as-of day → excluded by leakage guard
	}
	f := &fakeRecentClient{days: days}
	got, err := windowedHitterRecent(f, "t1", today, seasonStart, false)
	if err != nil {
		t.Fatalf("windowedHitterRecent: %v", err)
	}
	rs, ok := got["p1"]
	if !ok {
		t.Fatalf("p1 missing from recency map")
	}
	if rs.GamesPlayed != 2 {
		t.Errorf("GamesPlayed = %d, want 2 (as-of day excluded)", rs.GamesPlayed)
	}
	if math.Abs(rs.FPtsPerGame-15.0) > 1e-9 { // (10 + 20)/2
		t.Errorf("FPtsPerGame = %v, want 15", rs.FPtsPerGame)
	}
}

// A pitcher row in the series must not leak into the hitter recency map.
func TestWindowedHitterRecent_PitchersExcluded(t *testing.T) {
	today := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	days := []fantrax.DayRoster{{
		Date: today.AddDate(0, 0, -1),
		Players: []fantrax.DayPlayerFP{
			{PlayerID: "hit", IsPitcher: false, HadGame: true, FPts: 12},
			{PlayerID: "pit", IsPitcher: true, HadGame: true, FPts: 40},
		},
	}}
	got, err := windowedHitterRecent(&fakeRecentClient{days: days}, "t1", today, seasonStart, false)
	if err != nil {
		t.Fatalf("windowedHitterRecent: %v", err)
	}
	if _, ok := got["pit"]; ok {
		t.Errorf("pitcher leaked into hitter recency map")
	}
	if _, ok := got["hit"]; !ok {
		t.Errorf("hitter missing from recency map")
	}
}

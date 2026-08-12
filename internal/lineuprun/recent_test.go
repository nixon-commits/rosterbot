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
	days    []fantrax.DayRoster
	mlbDays []fantrax.DayRoster
	mlbErr  error

	// mlbAsked records which players the MLB path was consulted for, so tests
	// can assert the lookup is confined to short-coverage players.
	mlbAsked []fantrax.MLBPlayerRef
}

func (f *fakeRecentClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, errors.New("GetSeasonDateRange should not be called when seasonStart is set")
}
func (f *fakeRecentClient) DailyFantasyPoints(_ string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	return f.days, nil
}
func (f *fakeRecentClient) MLBDailyFPts(players []fantrax.MLBPlayerRef, _, _ time.Time) ([]fantrax.DayRoster, error) {
	f.mlbAsked = append(f.mlbAsked, players...)
	if f.mlbErr != nil {
		return nil, f.mlbErr
	}
	return f.mlbDays, nil
}

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

// A player acquired mid-window appears in our roster snapshots only from the
// acquisition day forward, so the roster-derived series reports a GamesPlayed
// truncated to their tenure rather than to their actual games. That truncated
// count drives the blend weight, pinning a newly acquired everyday hitter near
// 100% base projection (rosterbot-6tw: Sam Antonacci, 3 days on roster, 96%).
// The window must instead reflect the games the player actually played.
func TestWindowedHitterRecent_UsesMLBLogWhenRosterCoverageIsShort(t *testing.T) {
	today := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Our snapshots hold "new" for only the last 3 days before today.
	var days []fantrax.DayRoster
	for _, ago := range []int{3, 2, 1} {
		days = append(days, fantrax.DayRoster{
			Date: today.AddDate(0, 0, -ago),
			Players: []fantrax.DayPlayerFP{
				{PlayerID: "new", Name: "New Guy", IsPitcher: false, HadGame: true, FPts: 10},
			},
		})
	}

	// MLB says he actually played 20 games inside the trailing 30-day window,
	// at 20 FPts each — the production that happened on someone else's roster.
	var mlbDays []fantrax.DayRoster
	for ago := 1; ago <= 20; ago++ {
		mlbDays = append(mlbDays, fantrax.DayRoster{
			Date: today.AddDate(0, 0, -ago),
			Players: []fantrax.DayPlayerFP{
				{PlayerID: "new", Name: "New Guy", IsPitcher: false, HadGame: true, FPts: 20},
			},
		})
	}

	f := &fakeRecentClient{days: days, mlbDays: mlbDays}
	got, err := windowedHitterRecent(f, "t1", today, seasonStart, false)
	if err != nil {
		t.Fatalf("windowedHitterRecent: %v", err)
	}
	rs, ok := got["new"]
	if !ok {
		t.Fatalf("new missing from recency map")
	}
	if rs.GamesPlayed != 20 {
		t.Errorf("GamesPlayed = %d, want 20 (actual games in window, not 3 days of roster tenure)", rs.GamesPlayed)
	}
	if math.Abs(rs.FPtsPerGame-20.0) > 1e-9 {
		t.Errorf("FPtsPerGame = %v, want 20", rs.FPtsPerGame)
	}
	if len(f.mlbAsked) != 1 || f.mlbAsked[0].PlayerID != "new" {
		t.Errorf("expected exactly one MLB lookup for %q, got %+v", "new", f.mlbAsked)
	}
}

// A player rostered for the whole window already has complete coverage, so the
// MLB path must not be consulted for them — it costs an upstream request per
// player and the roster series is authoritative.
func TestWindowedHitterRecent_NoMLBLookupWhenCoverageIsComplete(t *testing.T) {
	today := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Present on every day the fetch covers: today-35 .. today-1.
	var days []fantrax.DayRoster
	for ago := 35; ago >= 1; ago-- {
		days = append(days, fantrax.DayRoster{
			Date: today.AddDate(0, 0, -ago),
			Players: []fantrax.DayPlayerFP{
				{PlayerID: "vet", Name: "Vet Guy", IsPitcher: false, HadGame: true, FPts: 8},
			},
		})
	}

	f := &fakeRecentClient{days: days}
	got, err := windowedHitterRecent(f, "t1", today, seasonStart, false)
	if err != nil {
		t.Fatalf("windowedHitterRecent: %v", err)
	}
	if len(f.mlbAsked) != 0 {
		t.Errorf("expected no MLB lookup for a fully-covered player, got %+v", f.mlbAsked)
	}
	if got["vet"].GamesPlayed != 30 {
		t.Errorf("GamesPlayed = %d, want 30 (the trailing window)", got["vet"].GamesPlayed)
	}
}

// The MLB path is best-effort: an upstream failure must leave the roster-derived
// signal standing rather than failing the whole run, matching the soft-fail
// contract DailyFantasyPoints already documents for its own backfill.
func TestWindowedHitterRecent_MLBFailureLeavesRosterSignalIntact(t *testing.T) {
	today := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	days := []fantrax.DayRoster{
		hitterDay(today.AddDate(0, 0, -1), "new", 10),
	}
	f := &fakeRecentClient{days: days, mlbErr: errors.New("statsapi down")}
	got, err := windowedHitterRecent(f, "t1", today, seasonStart, false)
	if err != nil {
		t.Fatalf("windowedHitterRecent should soft-fail the MLB path, got %v", err)
	}
	if got["new"].GamesPlayed != 1 {
		t.Errorf("GamesPlayed = %d, want 1 (roster-derived signal preserved)", got["new"].GamesPlayed)
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

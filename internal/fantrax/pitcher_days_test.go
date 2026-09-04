package fantrax

import (
	"testing"
	"time"
)

// The day walk has to report ROSTER PRESENCE, not just starts, because the
// only honest denominator for a per-pitcher start rate is the days the pitcher
// was actually ours. A denominator of "every day in the window" reads a
// mid-window acquisition as a pitcher who declined to start on days he was not
// on the roster — the same class of error rosterbot-6tw fixed for the hitter
// recency window, where a player's absent days contributed no row at all and
// were indistinguishable from zeroes.
func TestGetTeamPitcherDays_EmitsARowForEveryRosteredDay(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonStart := today.AddDate(0, 0, -9) // today is daily period 10

	// period 6 is the baseline day (start-1); 7..9 are the walked days.
	snaps := map[DailyPeriod]map[string]playerGSSnapshot{
		6: {"a": {GS: 0, Name: "Ace", MLBTeam: "LAD"}},
		7: {"a": {GS: 0, Name: "Ace", MLBTeam: "LAD", Active: true}},
		8: {"a": {GS: 1, FPts: 30, Name: "Ace", MLBTeam: "LAD", Active: true}},
		9: {
			"a": {GS: 1, FPts: 30, Name: "Ace", MLBTeam: "LAD"},
			"b": {GS: 0, Name: "Newcomer", MLBTeam: "NYM", Active: true},
		},
	}
	restore := stubPlayerGSSnapshot(func(_ *Client, _ string, period DailyPeriod) (map[string]playerGSSnapshot, error) {
		return snaps[period], nil
	})
	defer restore()

	c := &Client{teamID: "4", periodMapMemo: map[string]DailyPeriod{}}
	start, end := today.AddDate(0, 0, -3), today.AddDate(0, 0, -1)

	days, err := c.GetTeamPitcherDays("4", start, end, seasonStart, "", 0)
	if err != nil {
		t.Fatalf("GetTeamPitcherDays: %v", err)
	}

	type row struct {
		name    string
		date    string
		started bool
	}
	got := map[row]bool{}
	for _, d := range days {
		got[row{d.PitcherName, d.Date.Format("2006-01-02"), d.Started}] = true
	}
	want := []row{
		{"Ace", start.Format("2006-01-02"), false},
		{"Ace", start.AddDate(0, 0, 1).Format("2006-01-02"), true},
		{"Ace", end.Format("2006-01-02"), false},
		// The newcomer's single rostered day is a row, so his denominator is
		// 1 and not the whole window.
		{"Newcomer", end.Format("2006-01-02"), false},
	}
	if len(days) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(days), len(want), days)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing row %+v; got %+v", w, days)
		}
	}
}

// GetTeamPitcherStarts must remain exactly the active-slot-start subset of the
// day walk. The two used to be one function; if they ever disagree, the recap's
// start list and the forecast's rate denominator are reading different worlds.
func TestGetTeamPitcherStarts_IsTheStartedSubsetOfTheDayWalk(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonStart := today.AddDate(0, 0, -9)

	snaps := map[DailyPeriod]map[string]playerGSSnapshot{
		6: {"a": {GS: 0, Name: "Ace", MLBTeam: "LAD"}},
		7: {"a": {GS: 0, Name: "Ace", MLBTeam: "LAD", Active: true}},
		8: {"a": {GS: 1, FPts: 30, Name: "Ace", MLBTeam: "LAD", Active: true}},
		9: {
			"a": {GS: 1, FPts: 30, Name: "Ace", MLBTeam: "LAD"},
			// A real-world start taken from the BENCH: the GS advances but the
			// pitcher was not active, so it spends no weekly budget and must
			// not appear as a start.
			"b": {GS: 4, FPts: 12, Name: "Benched", MLBTeam: "NYM"},
		},
	}
	restore := stubPlayerGSSnapshot(func(_ *Client, _ string, period DailyPeriod) (map[string]playerGSSnapshot, error) {
		return snaps[period], nil
	})
	defer restore()

	c := &Client{teamID: "4", periodMapMemo: map[string]DailyPeriod{}}
	start, end := today.AddDate(0, 0, -3), today.AddDate(0, 0, -1)

	days, err := c.GetTeamPitcherDays("4", start, end, seasonStart, "", 0)
	if err != nil {
		t.Fatalf("GetTeamPitcherDays: %v", err)
	}
	starts, err := c.GetTeamPitcherStarts("4", start, end, seasonStart, "", 0)
	if err != nil {
		t.Fatalf("GetTeamPitcherStarts: %v", err)
	}

	var started int
	for _, d := range days {
		if d.Started {
			started++
		}
	}
	if started != len(starts) {
		t.Fatalf("day walk reported %d started rows, GetTeamPitcherStarts reported %d", started, len(starts))
	}
	if len(starts) != 1 || starts[0].PitcherName != "Ace" {
		t.Fatalf("want exactly Ace's active-slot start, got %+v", starts)
	}
}

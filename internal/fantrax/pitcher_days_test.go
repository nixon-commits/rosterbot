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

// The roster-meta join. Fantrax's per-day pitcher-GS snapshot carries no status
// and no positions, so a rate denominator built from it alone counts days a
// pitcher was on the IL or in the minors — days on which he could not have
// taken an active-slot start whatever his club did. Measured live, that read
// Gerrit Cole at 0.086 over 26 "opportunities" of which 24 were unavailable
// days, against 0.600 over the two that were real.
//
// The join reads the SAME roster-stats snapshot DailyFantasyPoints already
// walks, through the same cached helper, so it introduces no upstream call.
func TestGetTeamPitcherDaysWithStatus_JoinsTheDaysRosterFacts(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonStart := today.AddDate(0, 0, -9) // today is daily period 10

	snaps := map[DailyPeriod]map[string]playerGSSnapshot{
		6: {"a": {GS: 0, Name: "Ace", MLBTeam: "LAD"}},
		7: {"a": {GS: 0, Name: "Ace", MLBTeam: "LAD", Active: true}},
		8: {"a": {GS: 1, FPts: 30, Name: "Ace", MLBTeam: "LAD", Active: true}},
		9: {"a": {GS: 1, FPts: 30, Name: "Ace", MLBTeam: "LAD"}},
	}
	rosters := map[DailyPeriod]periodSnapshot{
		7: {Pitchers: map[string]playerYTD{"a": {StatusID: StatusActive, PosShortNames: "SP"}}},
		8: {Pitchers: map[string]playerYTD{"a": {StatusID: StatusActive, PosShortNames: "SP"}}},
		9: {Pitchers: map[string]playerYTD{"a": {StatusID: StatusIL, PosShortNames: "SP"}}},
	}
	restore := stubPlayerGSSnapshot(func(_ *Client, _ string, period DailyPeriod) (map[string]playerGSSnapshot, error) {
		return snaps[period], nil
	})
	defer restore()
	prevFetch := fetchPeriodSnapshotFn
	fetchPeriodSnapshotFn = func(_ *Client, _ string, period DailyPeriod) (periodSnapshot, error) {
		return rosters[period], nil
	}
	defer func() { fetchPeriodSnapshotFn = prevFetch }()

	c := &Client{teamID: "4", periodMapMemo: map[string]DailyPeriod{}}
	start, end := today.AddDate(0, 0, -3), today.AddDate(0, 0, -1)

	days, err := c.GetTeamPitcherDaysWithStatus("4", start, end, seasonStart, "", 0)
	if err != nil {
		t.Fatalf("GetTeamPitcherDaysWithStatus: %v", err)
	}
	if len(days) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(days), days)
	}
	wantStatus := []string{StatusActive, StatusActive, StatusIL}
	for i, d := range days {
		if d.StatusID != wantStatus[i] {
			t.Errorf("day %d StatusID = %q, want %q", i, d.StatusID, wantStatus[i])
		}
		if d.PosShortNames != "SP" {
			t.Errorf("day %d PosShortNames = %q, want %q", i, d.PosShortNames, "SP")
		}
	}

	// And the plain walk must NOT pay for the join: its callers do not read
	// these fields, and giving it a second upstream to fail on would let a
	// recap lose a week's pitching highlights over data it never looks at.
	plain, err := c.GetTeamPitcherDays("4", start, end, seasonStart, "", 0)
	if err != nil {
		t.Fatalf("GetTeamPitcherDays: %v", err)
	}
	for i, d := range plain {
		if d.StatusID != "" || d.PosShortNames != "" {
			t.Errorf("plain walk row %d carries roster meta %q/%q", i, d.StatusID, d.PosShortNames)
		}
	}
}

// A row the walk had no previous YTD to diff against is marked, because it is
// silently asymmetric in both directions: the first-appearance delta is capped
// at 1 so it CAN report a start belonging to another team's season, while a
// first-appearance day on which the pitcher did not start is indistinguishable
// from one on which he did but sat. A rate denominator has to drop it whole.
func TestGetTeamPitcherDays_MarksFirstAppearances(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonStart := today.AddDate(0, 0, -9)

	snaps := map[DailyPeriod]map[string]playerGSSnapshot{
		6: {"a": {GS: 3, Name: "Ace", MLBTeam: "LAD"}},
		7: {"a": {GS: 3, Name: "Ace", MLBTeam: "LAD", Active: true}},
		8: {
			"a": {GS: 4, FPts: 30, Name: "Ace", MLBTeam: "LAD", Active: true},
			// Acquired mid-window carrying a whole season of GS. The delta cap
			// turns 12 into 1 and the row reports a start that was not ours.
			"b": {GS: 12, FPts: 400, Name: "Newcomer", MLBTeam: "NYM", Active: true},
		},
		9: {
			"a": {GS: 4, FPts: 30, Name: "Ace", MLBTeam: "LAD"},
			"b": {GS: 12, FPts: 400, Name: "Newcomer", MLBTeam: "NYM", Active: true},
		},
	}
	restore := stubPlayerGSSnapshot(func(_ *Client, _ string, period DailyPeriod) (map[string]playerGSSnapshot, error) {
		return snaps[period], nil
	})
	defer restore()

	c := &Client{teamID: "4", periodMapMemo: map[string]DailyPeriod{}}
	days, err := c.GetTeamPitcherDays("4", today.AddDate(0, 0, -3), today.AddDate(0, 0, -1), seasonStart, "", 0)
	if err != nil {
		t.Fatalf("GetTeamPitcherDays: %v", err)
	}

	for _, d := range days {
		first := d.PitcherName == "Newcomer" && d.Date.Equal(today.AddDate(0, 0, -2))
		if d.FirstAppearance != first {
			t.Errorf("%s on %s: FirstAppearance = %v, want %v",
				d.PitcherName, d.Date.Format("2006-01-02"), d.FirstAppearance, first)
		}
		if first && !d.Started {
			t.Error("fixture must reproduce the capped first-appearance start, otherwise the flag pins nothing")
		}
	}
	// The baseline day seeds Ace, so his first walked day is NOT a first
	// appearance — which is what makes the flag mean "no diffable baseline"
	// rather than "first row in the output".
	if days[0].PitcherName != "Ace" || days[0].FirstAppearance {
		t.Errorf("baselined pitcher marked as a first appearance: %+v", days[0])
	}
}

package recap

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

func pday(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// A bye is a fact about a playoff week the recap must show: the team was in
// the bracket and played nobody. Only byes inside the window count, and only
// the flagged entries — a regular-season row never has one.
func TestByesForWeek(t *testing.T) {
	entries := []fantrax.MatchupEntry{
		{ScoringPeriod: 22, Date: "Mon Aug 31, 2026", HomeID: "a", AwayID: "b"},
		{ScoringPeriod: 23, Date: "Mon Sep 7, 2026", HomeID: "pfaadt", Playoff: true, Bye: true},
		{ScoringPeriod: 23, Date: "Mon Sep 7, 2026", HomeID: "jimmy", AwayID: "balk", Playoff: true},
		{ScoringPeriod: 23, Date: "Mon Sep 7, 2026", HomeID: "yordan", Playoff: true, Bye: true},
		{ScoringPeriod: 24, Date: "Mon Sep 14, 2026", HomeID: "later", Playoff: true, Bye: true},
	}
	got := byesForWeek(entries, pday(2026, 9, 7), pday(2026, 9, 13), map[string]string{"pfaadt": "Pfaadt Wood Kings", "yordan": "Yordan's"})
	if len(got) != 2 || got[0].TeamID != "pfaadt" || got[0].TeamName != "Pfaadt Wood Kings" || got[1].TeamID != "yordan" {
		t.Errorf("byes = %+v, want pfaadt and yordan in bracket order", got)
	}
}

// The week label and number come from the league's own period list: a
// regular-season week is "Week N" with N its scoring period, a playoff round
// carries Fantrax's caption, and a window outside every period keeps the
// calendar fallback the recap always had.
func TestWeekLabelFor(t *testing.T) {
	periods := []fantrax.ScoringPeriod{
		{Number: 22, Caption: "Scoring Period 22", StartDate: pday(2026, 8, 31), EndDate: pday(2026, 9, 6)},
		{Number: 23, Caption: "Playoffs - Round 1", Playoff: true, StartDate: pday(2026, 9, 7), EndDate: pday(2026, 9, 13)},
	}
	cases := []struct {
		start     time.Time
		wantNum   int
		wantLabel string
	}{
		{pday(2026, 8, 31), 22, "Week 22"},
		{pday(2026, 9, 7), 23, "Playoffs - Round 1"},
		{pday(2026, 10, 5), matchupWeekNumber(pday(2026, 3, 25), pday(2026, 10, 5)), "Week 28"}, // calendar fallback
	}
	for _, tc := range cases {
		num, label := weekLabelFor(periods, pday(2026, 3, 25), tc.start)
		if num != tc.wantNum || label != tc.wantLabel {
			t.Errorf("weekLabelFor(%s) = (%d, %q), want (%d, %q)", tc.start.Format("01-02"), num, label, tc.wantNum, tc.wantLabel)
		}
	}
}

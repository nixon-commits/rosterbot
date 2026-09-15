package cmd

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// The recap is league-scoped: its window comes from the league's weekly
// period list, never from the operator's own matchup weeks, so Round 2
// renders whether or not the operator's team is still in the bracket.
func TestRecapWindow_IsLeagueScoped(t *testing.T) {
	ymd := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	periods := []fantrax.ScoringPeriod{
		{Number: 22, Caption: "Scoring Period 22", StartDate: ymd(2026, 8, 31), EndDate: ymd(2026, 9, 6)},
		{Number: 23, Caption: "Playoffs - Round 1", Playoff: true, StartDate: ymd(2026, 9, 7), EndDate: ymd(2026, 9, 13)},
		{Number: 24, Caption: "Playoffs - Round 2", Playoff: true, StartDate: ymd(2026, 9, 14), EndDate: ymd(2026, 9, 20)},
	}
	done := func(bool) func(time.Time) bool { return func(time.Time) bool { return true } }
	notDone := func(time.Time) bool { return false }

	cases := []struct {
		name       string
		today      time.Time
		week       int
		todayDone  func(time.Time) bool
		wantStart  time.Time
		wantErrSub string
	}{
		{"--week 23 is Round 1", ymd(2026, 9, 21), 23, notDone, ymd(2026, 9, 7), ""},
		{"Monday after Round 2 defaults to Round 2", ymd(2026, 9, 21), 0, notDone, ymd(2026, 9, 14), ""},
		{"Round 1's Sunday with games final renders Round 1", ymd(2026, 9, 13), 0, done(true), ymd(2026, 9, 7), ""},
		{"Round 1's Sunday with games live falls back to week 22", ymd(2026, 9, 13), 0, notDone, ymd(2026, 8, 31), ""},
		{"--week past the bracket is an error", ymd(2026, 9, 21), 26, notDone, time.Time{}, "26"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, _, err := recapWindow(periods, tc.today, tc.week, tc.todayDone)
			if tc.wantErrSub != "" {
				if err == nil || !contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("recapWindow: %v", err)
			}
			if !start.Equal(tc.wantStart) {
				t.Errorf("start = %s, want %s", start.Format("2006-01-02"), tc.wantStart.Format("2006-01-02"))
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

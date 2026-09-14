package fantrax

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

func TestMatchupWeekBounds(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)

	// Weekly matchup entries: each entry is one week-long scoring period.
	// Week 1 (period 1): Mar 25 – Mar 31, opponent "opp1"
	// Week 2 (period 2): Apr 1 – Apr 7, opponent "opp2"
	matchups := []auth_client.Matchup{
		{
			ScoringPeriod: 1,
			Date:          "Wed Mar 25, 2026",
			AwayTeam:      auth_client.MatchTeam{TeamID: "myteam"},
			HomeTeam:      auth_client.MatchTeam{TeamID: "opp1"},
		},
		{
			ScoringPeriod: 2,
			Date:          "Wed Apr 1, 2026",
			AwayTeam:      auth_client.MatchTeam{TeamID: "opp2"},
			HomeTeam:      auth_client.MatchTeam{TeamID: "myteam"},
		},
	}

	tests := []struct {
		name      string
		date      time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			"first day of week 1",
			seasonStart,
			seasonStart,
			seasonStart.AddDate(0, 0, 6), // Mar 31
		},
		{
			"mid week 1",
			seasonStart.AddDate(0, 0, 3), // Mar 28
			seasonStart,
			seasonStart.AddDate(0, 0, 6),
		},
		{
			"last day of week 1",
			seasonStart.AddDate(0, 0, 6), // Mar 31
			seasonStart,
			seasonStart.AddDate(0, 0, 6),
		},
		{
			"first day of week 2",
			time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC), // last run uses +6 days
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd := MatchupWeekBounds(matchups, "myteam", seasonStart, tt.date)
			if !gotStart.Equal(tt.wantStart) {
				t.Errorf("weekStart = %s, want %s", gotStart.Format("2006-01-02"), tt.wantStart.Format("2006-01-02"))
			}
			if !gotEnd.Equal(tt.wantEnd) {
				t.Errorf("weekEnd = %s, want %s", gotEnd.Format("2006-01-02"), tt.wantEnd.Format("2006-01-02"))
			}
		})
	}
}

func TestMatchupWeekBounds_NoMatch(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	matchups := []auth_client.Matchup{
		{
			ScoringPeriod: 1,
			Date:          "Wed Mar 25, 2026",
			AwayTeam:      auth_client.MatchTeam{TeamID: "other1"},
			HomeTeam:      auth_client.MatchTeam{TeamID: "other2"},
		},
	}
	start, end := MatchupWeekBounds(matchups, "myteam", seasonStart, seasonStart)
	if !start.IsZero() || !end.IsZero() {
		t.Error("expected zero times when team not in matchups")
	}
}

func TestMatchupWeekBounds_SameOpponentInConsecutivePeriodsAreTwoWeeks(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	// Two consecutive scoring periods against the same opponent are two
	// matchups with two results, not one fortnight. CONTEXT.md's measured
	// invariant is that matchup weeks and the weekly period list agree 1:1;
	// merging across periods would break it, and the playoff bracket makes
	// the case real (a week-22 opponent can be the Round 1 opponent).
	matchups := []auth_client.Matchup{
		{ScoringPeriod: 1, Date: "Wed Mar 25, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		{ScoringPeriod: 2, Date: "Wed Apr 1, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		{ScoringPeriod: 3, Date: "Wed Apr 8, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "opp2"}, HomeTeam: auth_client.MatchTeam{TeamID: "myteam"}},
	}

	start, end := MatchupWeekBounds(matchups, "myteam", seasonStart, seasonStart)
	if !start.Equal(seasonStart) || !end.Equal(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("week 1 = %s to %s, want 2026-03-25 to 2026-03-31",
			start.Format("2006-01-02"), end.Format("2006-01-02"))
	}
	start2, end2 := MatchupWeekBounds(matchups, "myteam", seasonStart, time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC))
	if !start2.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) || !end2.Equal(time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("week 2 = %s to %s, want 2026-04-01 to 2026-04-07",
			start2.Format("2006-01-02"), end2.Format("2006-01-02"))
	}
	if n := MatchupWeekNumberForDate(matchups, "myteam", time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)); n != 2 {
		t.Errorf("week number for a day in the second period = %d, want 2", n)
	}
}

func TestMatchupWeekByNumber(t *testing.T) {
	matchups := []auth_client.Matchup{
		// Week 1: Mar 25–31 vs opp1
		{ScoringPeriod: 1, Date: "Wed Mar 25, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		// Week 2: Apr 1–7 vs opp2
		{ScoringPeriod: 2, Date: "Wed Apr 1, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "opp2"}, HomeTeam: auth_client.MatchTeam{TeamID: "myteam"}},
		// Week 3: Apr 8–14 vs opp3 (last run, +6 days end)
		{ScoringPeriod: 3, Date: "Wed Apr 8, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp3"}},
	}

	tests := []struct {
		n         int
		wantStart string
		wantEnd   string
	}{
		{1, "2026-03-25", "2026-03-31"},
		{2, "2026-04-01", "2026-04-07"},
		{3, "2026-04-08", "2026-04-14"},
	}
	for _, tt := range tests {
		ws, we := MatchupWeekByNumber(matchups, "myteam", tt.n)
		if ws.Format("2006-01-02") != tt.wantStart || we.Format("2006-01-02") != tt.wantEnd {
			t.Errorf("week %d: got %s..%s, want %s..%s",
				tt.n, ws.Format("2006-01-02"), we.Format("2006-01-02"), tt.wantStart, tt.wantEnd)
		}
	}

	// Out-of-range cases return zero times.
	if ws, we := MatchupWeekByNumber(matchups, "myteam", 0); !ws.IsZero() || !we.IsZero() {
		t.Error("week 0 should be zero")
	}
	if ws, we := MatchupWeekByNumber(matchups, "myteam", 99); !ws.IsZero() || !we.IsZero() {
		t.Error("week 99 should be zero (out of range)")
	}
}

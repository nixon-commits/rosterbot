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

func TestMatchupWeekBounds_MultiWeekSameOpponent(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	// Two consecutive weeks against the same opponent should group together.
	matchups := []auth_client.Matchup{
		{ScoringPeriod: 1, Date: "Wed Mar 25, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		{ScoringPeriod: 2, Date: "Wed Apr 1, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		{ScoringPeriod: 3, Date: "Wed Apr 8, 2026",
			AwayTeam: auth_client.MatchTeam{TeamID: "opp2"}, HomeTeam: auth_client.MatchTeam{TeamID: "myteam"}},
	}

	// Day in week 1 should return the full 2-week span vs opp1.
	start, end := MatchupWeekBounds(matchups, "myteam", seasonStart, seasonStart)
	wantStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 4, 7, 0, 0, 0, 0, time.UTC) // day before Apr 8
	if !start.Equal(wantStart) {
		t.Errorf("weekStart = %s, want %s", start.Format("2006-01-02"), wantStart.Format("2006-01-02"))
	}
	if !end.Equal(wantEnd) {
		t.Errorf("weekEnd = %s, want %s", end.Format("2006-01-02"), wantEnd.Format("2006-01-02"))
	}

	// Day in week 2 (still vs opp1) should return same span.
	start2, end2 := MatchupWeekBounds(matchups, "myteam", seasonStart, time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC))
	if !start2.Equal(wantStart) || !end2.Equal(wantEnd) {
		t.Errorf("mid-run lookup: got %s to %s, want %s to %s",
			start2.Format("2006-01-02"), end2.Format("2006-01-02"),
			wantStart.Format("2006-01-02"), wantEnd.Format("2006-01-02"))
	}

	// Week 3 (vs opp2) should be its own span.
	start3, end3 := MatchupWeekBounds(matchups, "myteam", seasonStart, time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC))
	wantStart3 := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	wantEnd3 := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC) // last run, +6 days
	if !start3.Equal(wantStart3) || !end3.Equal(wantEnd3) {
		t.Errorf("week 3: got %s to %s, want %s to %s",
			start3.Format("2006-01-02"), end3.Format("2006-01-02"),
			wantStart3.Format("2006-01-02"), wantEnd3.Format("2006-01-02"))
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

package fantrax

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

func TestMatchupWeekBounds(t *testing.T) {
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)

	// Build matchups: 7 daily periods per week, alternating opponents.
	// Week 1: periods 1-7, opponent "opp1"
	// Week 2: periods 8-14, opponent "opp2"
	var matchups []auth_client.Matchup
	for i := 0; i < 7; i++ {
		d := seasonStart.AddDate(0, 0, i)
		matchups = append(matchups, auth_client.Matchup{
			ScoringPeriod: i + 1,
			Date:          d.Format("Mon Jan 2, 2006"),
			AwayTeam:      auth_client.MatchTeam{TeamID: "myteam"},
			HomeTeam:      auth_client.MatchTeam{TeamID: "opp1"},
		})
	}
	for i := 0; i < 7; i++ {
		d := seasonStart.AddDate(0, 0, 7+i)
		matchups = append(matchups, auth_client.Matchup{
			ScoringPeriod: 8 + i,
			Date:          d.Format("Mon Jan 2, 2006"),
			AwayTeam:      auth_client.MatchTeam{TeamID: "opp2"},
			HomeTeam:      auth_client.MatchTeam{TeamID: "myteam"},
		})
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
			seasonStart.AddDate(0, 0, 6),
		},
		{
			"mid week 1",
			seasonStart.AddDate(0, 0, 3),
			seasonStart,
			seasonStart.AddDate(0, 0, 6),
		},
		{
			"last day of week 1",
			seasonStart.AddDate(0, 0, 6),
			seasonStart,
			seasonStart.AddDate(0, 0, 6),
		},
		{
			"first day of week 2",
			seasonStart.AddDate(0, 0, 7),
			seasonStart.AddDate(0, 0, 7),
			seasonStart.AddDate(0, 0, 13),
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
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	matchups := []auth_client.Matchup{
		{
			ScoringPeriod: 1,
			Date:          seasonStart.Format("Mon Jan 2, 2006"),
			AwayTeam:      auth_client.MatchTeam{TeamID: "other1"},
			HomeTeam:      auth_client.MatchTeam{TeamID: "other2"},
		},
	}
	start, end := MatchupWeekBounds(matchups, "myteam", seasonStart, seasonStart)
	if !start.IsZero() || !end.IsZero() {
		t.Error("expected zero times when team not in matchups")
	}
}

func TestMatchupWeekBounds_HomeAndAway(t *testing.T) {
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	// Team is away in week 1, home in week 2.
	matchups := []auth_client.Matchup{
		{ScoringPeriod: 1, Date: seasonStart.Format("Mon Jan 2, 2006"),
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		{ScoringPeriod: 2, Date: seasonStart.AddDate(0, 0, 1).Format("Mon Jan 2, 2006"),
			AwayTeam: auth_client.MatchTeam{TeamID: "myteam"}, HomeTeam: auth_client.MatchTeam{TeamID: "opp1"}},
		{ScoringPeriod: 3, Date: seasonStart.AddDate(0, 0, 2).Format("Mon Jan 2, 2006"),
			AwayTeam: auth_client.MatchTeam{TeamID: "opp2"}, HomeTeam: auth_client.MatchTeam{TeamID: "myteam"}},
	}

	start, end := MatchupWeekBounds(matchups, "myteam", seasonStart, seasonStart)
	if !start.Equal(seasonStart) || !end.Equal(seasonStart.AddDate(0, 0, 1)) {
		t.Errorf("week 1 should be periods 1-2 (same opponent opp1), got %s to %s",
			start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	start2, end2 := MatchupWeekBounds(matchups, "myteam", seasonStart, seasonStart.AddDate(0, 0, 2))
	if !start2.Equal(seasonStart.AddDate(0, 0, 2)) || !end2.Equal(seasonStart.AddDate(0, 0, 2)) {
		t.Errorf("week 2 should be period 3 only (opponent opp2), got %s to %s",
			start2.Format("2006-01-02"), end2.Format("2006-01-02"))
	}
}

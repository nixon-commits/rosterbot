package optimizer

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/pmurley/go-fantrax/auth_client"
)

func date(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestGSBudget_Remaining(t *testing.T) {
	tests := []struct {
		name   string
		budget *GSBudget
		want   int
	}{
		{"nil budget", nil, 2147483647},
		{"zero limit", &GSBudget{Limit: 0}, 2147483647},
		{"no usage", &GSBudget{Limit: 12, Used: 0}, 12},
		{"some usage", &GSBudget{Limit: 12, Used: 5}, 7},
		{"fully used", &GSBudget{Limit: 12, Used: 12}, 0},
		{"over used", &GSBudget{Limit: 12, Used: 14}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.budget.Remaining()
			if got != tt.want {
				t.Errorf("Remaining() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGSBudget_FutureDemand(t *testing.T) {
	today := date("2026-04-06")
	budget := &GSBudget{
		Limit:   12,
		Used:    3,
		Today:   today,
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-07"), Confirmed: 2},              // confirmed
			{Date: date("2026-04-08"), Confirmed: 0, Estimated: 1.4}, // estimated
			{Date: date("2026-04-09"), Confirmed: 1},              // confirmed
		},
	}
	got := budget.FutureDemand()
	want := 4.4 // 2 + 1.4 + 1
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("FutureDemand() = %.2f, want %.2f", got, want)
	}
}

func TestGSBudget_FutureDemand_SkipsTodayAndBefore(t *testing.T) {
	today := date("2026-04-08")
	budget := &GSBudget{
		Limit:   12,
		Today:   today,
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-07"), Confirmed: 5}, // before today — should be skipped
			{Date: date("2026-04-08"), Confirmed: 3}, // today — should be skipped
			{Date: date("2026-04-09"), Confirmed: 1}, // after today — counted
		},
	}
	got := budget.FutureDemand()
	if got != 1.0 {
		t.Errorf("FutureDemand() = %.2f, want 1.0", got)
	}
}

func TestApplyGSGate_NilBudget(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1"}, ExpectedPts: 10, IsStarter: true},
	}
	result := applyGSGate(scored, nil)
	if !result[0].IsStarter {
		t.Error("nil budget should not suppress starters")
	}
}

func TestApplyGSGate_AmpleBudget(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2"}, ExpectedPts: 8, IsStarter: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    2,
		Today:   date("2026-04-06"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-07"), Estimated: 1.4},
			{Date: date("2026-04-08"), Estimated: 1.4},
		},
	}
	// remaining=10, futureDemand=2.8, slack=7.2, todayStarters=2 — ample
	result := applyGSGate(scored, budget)
	for _, sp := range result {
		if !sp.IsStarter {
			t.Errorf("ample budget should not suppress starter %s", sp.Player.ID)
		}
	}
}

func TestApplyGSGate_TightBudget_SuppressesWeakest(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2"}, ExpectedPts: 5, IsStarter: true},
		{Player: fantrax.Player{ID: "r1"}, ExpectedPts: 7, IsStarter: false, HasGame: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    10,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-11"), Confirmed: 1},
		},
	}
	// remaining=2, futureDemand=1, slack=1, todayStarters=2 → allow 1, suppress 1
	result := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("best starter (p1, 10pts) should NOT be suppressed")
	}
	if result[1].IsStarter {
		t.Error("weaker starter (p2, 5pts) should be suppressed")
	}
	if result[2].IsStarter {
		t.Error("RP (r1) should remain non-starter")
	}
}

func TestApplyGSGate_ZeroRemaining_SuppressesAll(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2"}, ExpectedPts: 8, IsStarter: true},
	}
	budget := &GSBudget{Limit: 12, Used: 12, Today: date("2026-04-10"), WeekEnd: date("2026-04-12")}
	result := applyGSGate(scored, budget)
	for _, sp := range result {
		if sp.IsStarter {
			t.Errorf("zero remaining should suppress all starters, but %s still starting", sp.Player.ID)
		}
	}
}

func TestOptimizePitcherLineup_GSBudgetCapsStarter(t *testing.T) {
	roster := []fantrax.Player{
		{ID: "p1", Name: "Ace SP", MLBTeam: "NYY", Positions: []string{auth_client.PosSP}, PosShortNames: "SP", Status: "Reserve"},
		{ID: "p2", Name: "Back SP", MLBTeam: "BOS", Positions: []string{auth_client.PosSP}, PosShortNames: "SP", Status: "Reserve"},
		{ID: "r1", Name: "Closer", MLBTeam: "LAD", Positions: []string{auth_client.PosRP}, PosShortNames: "RP", Status: "Reserve"},
	}
	playing := map[string]bool{"NYY": true, "BOS": true, "LAD": true}
	probables := map[string]string{"ace sp": "NYY", "back sp": "BOS"}
	src := &stubPitcherSource{data: map[string]*projections.PitcherProjection{
		"ace sp":  {G: 30, GS: 30, IP: 180, K: 200, W: 15},
		"back sp": {G: 30, GS: 30, IP: 170, K: 150, W: 10},
		"closer":  {G: 60, IP: 65, K: 70, SV: 30},
	}}
	scoring := fantrax.ScoringWeights{"K": 1.0, "W": 5.0, "IP": 1.0, "SV": 5.0}
	slots := makeSlots("SP", "P") // 2 slots

	// Budget: only 1 GS remaining, 1 projected future start, 2 today starters.
	// Proportional allocation: round(1 * 2/3) = 1 allowed today.
	// Ace SP (highest value) keeps IsStarter; Back SP is suppressed.
	budget := &GSBudget{
		Limit:   12,
		Used:    11,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-11"), Confirmed: 1},
		},
	}

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, budget)

	// Ace SP should keep IsStarter (highest value), Back SP should be suppressed.
	for _, sp := range result.Scored {
		switch sp.Player.Name {
		case "Ace SP":
			if !sp.IsStarter {
				t.Error("Ace SP should still be IsStarter (highest value gets the GS)")
			}
		case "Back SP":
			if sp.IsStarter {
				t.Error("Back SP should be suppressed (lower value)")
			}
		}
	}
}

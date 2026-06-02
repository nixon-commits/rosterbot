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
			{Date: date("2026-04-07"), ConfirmedStarters: []float64{15.0, 12.0}}, // confirmed
			{Date: date("2026-04-08"), Estimated: 1.4},                           // estimated
			{Date: date("2026-04-09"), ConfirmedStarters: []float64{10.0}},       // confirmed
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
			{Date: date("2026-04-07"), ConfirmedStarters: []float64{10, 12, 14, 15, 16}}, // before today
			{Date: date("2026-04-08"), ConfirmedStarters: []float64{11, 12, 13}},         // today
			{Date: date("2026-04-09"), ConfirmedStarters: []float64{10}},                 // after today
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
	// remaining=10, planned=2+2.8=4.8 — ample budget.
	result := applyGSGate(scored, budget)
	for _, sp := range result {
		if !sp.IsStarter {
			t.Errorf("ample budget should not suppress starter %s", sp.Player.ID)
		}
	}
}

func TestApplyGSGate_TightBudget_SuppressesWeakestToday(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
		{Player: fantrax.Player{ID: "r1", PosShortNames: "RP"}, ExpectedPts: 7, IsStarter: false, HasGame: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    10,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			// One future confirmed start worth more than today's weakest SP.
			{Date: date("2026-04-11"), ConfirmedStarters: []float64{8}},
		},
	}
	// remaining=2, planned=3 (2 today + 1 future). Top 2 values: p1(10), future(8). p2(5) cut.
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

// TestApplyGSGate_ValueAwareKeepsTodayOverMediocreFuture is the key regression
// test for today's bug: when today's starter is clearly higher-value than
// future expected starts, the count-based proportional gate would have cut
// them to conserve budget. The value-aware gate keeps them.
func TestApplyGSGate_ValueAwareKeepsTodayOverMediocreFuture(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "ace", PosShortNames: "SP"}, ExpectedPts: 15, IsStarter: true},
		{Player: fantrax.Player{ID: "mid", PosShortNames: "SP"}, ExpectedPts: 14, IsStarter: true},
		{Player: fantrax.Player{ID: "burn", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		// Other rostered SPs (unused today) for placeholder-value computation.
		{Player: fantrax.Player{ID: "wood", PosShortNames: "SP"}, ExpectedPts: 6},
		{Player: fantrax.Player{ID: "cavy", PosShortNames: "SP"}, ExpectedPts: 5},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    7,
		Today:   date("2026-04-16"),
		WeekEnd: date("2026-04-19"),
		Forecast: []DayForecast{
			// Future demand = 5 estimated, unknown values. Roster mean ≈ 10.
			{Date: date("2026-04-17"), Estimated: 1.6},
			{Date: date("2026-04-18"), Estimated: 1.6},
			{Date: date("2026-04-19"), Estimated: 1.6},
		},
	}
	// remaining=5, today=3, future-est≈4.8 → totalPlanned ≈ 7.8.
	// Under old count-based gate: allowToday = round(5 * 3/7.8) = round(1.92) = 2 — would cut Burns.
	// Under value-aware gate: today's 10pt SP should beat ~10pt placeholder by
	// stability tiebreaker (today wins ties), so all 3 today SPs kept.
	result := applyGSGate(scored, budget)
	for _, sp := range result {
		if sp.Player.ID == "ace" || sp.Player.ID == "mid" || sp.Player.ID == "burn" {
			if !sp.IsStarter {
				t.Errorf("today's starter %s (%.1fpts) should not be suppressed over mediocre future starts", sp.Player.ID, sp.ExpectedPts)
			}
		}
	}
}

func TestApplyGSGate_HighValueFutureCutsTodayWeakest(t *testing.T) {
	// Inverse scenario: future confirmed probables are aces, today's SPs are
	// mediocre. Gate should suppress today's weakest SP to save GS for future.
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "today1", PosShortNames: "SP"}, ExpectedPts: 6, IsStarter: true},
		{Player: fantrax.Player{ID: "today2", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    9,
		Today:   date("2026-04-16"),
		WeekEnd: date("2026-04-19"),
		Forecast: []DayForecast{
			{Date: date("2026-04-17"), ConfirmedStarters: []float64{16, 15}},
		},
	}
	// remaining=3, total known=4 (2 today + 2 future). Top 3: 16, 15, 6.
	// today2 (5pts) suppressed; today1 (6pts) kept.
	result := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("today1 (6pts) should survive — higher than future cut")
	}
	if result[1].IsStarter {
		t.Error("today2 (5pts) should be suppressed — below future aces")
	}
}

func TestApplyGSGate_SkipsLockedPlayers(t *testing.T) {
	// Locked today SPs must keep their IsStarter status regardless of budget
	// pressure. Locked active starters already consumed their GS (counted in
	// Used); locked bench starters won't consume any. Flipping IsStarter on
	// a locked player only corrupts the displayed pts without affecting the
	// lineup (can't move locked players).
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "locked_active", PosShortNames: "SP", Locked: true}, ExpectedPts: 8, IsStarter: true},
		{Player: fantrax.Player{ID: "locked_bench", PosShortNames: "SP", Locked: true}, ExpectedPts: 7, IsStarter: true},
		{Player: fantrax.Player{ID: "unlocked_low", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    10,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-11"), ConfirmedStarters: []float64{15, 14, 13}},
		},
	}
	// remaining=2, 6 planned (3 today + 3 future). Locked SPs excluded from
	// the ranking entirely — only unlocked_low (5pts) competes against
	// future (15/14/13). It loses and is suppressed. Locked pair stays put.
	result := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("locked active SP should keep IsStarter=true (can't be suppressed)")
	}
	if !result[1].IsStarter {
		t.Error("locked bench SP should keep IsStarter=true (can't be suppressed)")
	}
	if result[2].IsStarter {
		t.Error("unlocked SP below future cut should be suppressed")
	}
}

func TestApplyGSGate_ZeroRemaining_PreservesLocked(t *testing.T) {
	// Even when budget is fully spent, locked players keep IsStarter so the
	// display reflects their actual (already-decided) role.
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "locked", Locked: true}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "unlocked"}, ExpectedPts: 8, IsStarter: true},
	}
	budget := &GSBudget{Limit: 12, Used: 12, Today: date("2026-04-10"), WeekEnd: date("2026-04-12")}
	result := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("locked player should retain IsStarter even at zero remaining")
	}
	if result[1].IsStarter {
		t.Error("unlocked player should be suppressed at zero remaining")
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

	// Budget: 1 GS remaining, 1 confirmed future start of higher value than
	// today's weaker SP. Ace SP (higher value) keeps IsStarter; Back SP cut.
	budget := &GSBudget{
		Limit:   12,
		Used:    11,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			// Future value (13.0) sits between Ace (~15.17) and Back (~12.33)
			// so with remaining=1, only Ace beats the future cutoff.
			{Date: date("2026-04-11"), ConfirmedStarters: []float64{13.0}},
		},
	}

	result := OptimizePitcherLineup(roster, playing, probables, src, scoring, slots, budget)

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

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
	result, _ := applyGSGate(scored, nil)
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
	result, _ := applyGSGate(scored, budget)
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
	result, _ := applyGSGate(scored, budget)
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
	result, _ := applyGSGate(scored, budget)
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
	result, _ := applyGSGate(scored, budget)
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
	result, _ := applyGSGate(scored, budget)
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
	result, _ := applyGSGate(scored, budget)
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
	result, _ := applyGSGate(scored, budget)
	for _, sp := range result {
		if sp.IsStarter {
			t.Errorf("zero remaining should suppress all starters, but %s still starting", sp.Player.ID)
		}
	}
}

// TestApplyGSGate_FractionalEstimateDoesNotOverSuppressToday is the
// regression test for the ceil-rounding bug: a small fractional future
// estimate (e.g. 0.2 expected starts) used to become one FULL-value
// placeholder entry (ceil(0.2)=1), competing at full roster-mean value
// against today's certain start. It should instead compete at only its
// fractional share of that value.
//
// It doubles as the counterexample that closed rosterbot-idn: 8 points certain
// beats 20×0.2 = 4.0 expected, and because weekly GS is use-it-or-lose-it, the
// 80% of the time the estimated start never materializes the held slot is
// burned rather than banked. Any rewrite that prices this tail at full value
// fails here, which is the point.
func TestApplyGSGate_FractionalEstimateDoesNotOverSuppressToday(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "today", PosShortNames: "SP"}, ExpectedPts: 8, IsStarter: true},
		// Bench SP feeding the roster-mean placeholder value (20).
		{Player: fantrax.Player{ID: "bench", PosShortNames: "SP"}, ExpectedPts: 20},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    11,
		Today:   date("2026-04-16"),
		WeekEnd: date("2026-04-19"),
		Forecast: []DayForecast{
			// 0.2 expected starts, no probables announced yet.
			{Date: date("2026-04-17"), Estimated: 0.2},
		},
	}
	// remaining=1, totalPlanned=1(today)+0.2(estimated)=1.2 > remaining → gate engages.
	// Old (ceil-rounded) code: 1 placeholder entry at full mean (20) beats today (8) → today cut.
	// Fixed code: placeholder entry discounted to 20*0.2=4.0 → today (8) wins.
	result, _ := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("today's 8pt starter should survive a 0.2-expected-start future claim, not lose to a full-value phantom placeholder")
	}
}

// TestApplyGSGate_EstimatedLadderPricesWholeUnitsFullAndTailMarginally pins
// BOTH rungs of the estimated-demand ladder in one case, which matters more
// since rosterbot-abd made Estimated routinely exceed 1.0 (it was pinned at
// numPSlots/rotationSize before).
//
// The k-th slot held for estimated demand is worth
// placeholder × min(max(E-(k-1), 0), 1) — full value while whole units of
// demand remain, the leftover fraction after that. With E=1.4 and a
// placeholder of 12 that is one entry at 12.0 and one at 4.8. The first
// outranks today's 9 (real demand, real value); the second loses to today's 8
// (a 40%-likely claim is not worth a certain start).
//
// This is the discriminating test for rosterbot-idn's proposed rewrite, which
// would price both rungs at a full 12 against a fractional budget: that form
// fits neither of today's starters into the remaining 2 slots and benches both.
func TestApplyGSGate_EstimatedLadderPricesWholeUnitsFullAndTailMarginally(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "today-strong", PosShortNames: "SP"}, ExpectedPts: 9, IsStarter: true},
		{Player: fantrax.Player{ID: "today-weak", PosShortNames: "SP"}, ExpectedPts: 8, IsStarter: true},
		// Bench SP pulls the roster-mean placeholder to (9+8+19)/3 = 12.
		{Player: fantrax.Player{ID: "bench", PosShortNames: "SP"}, ExpectedPts: 19},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    10,
		Today:   date("2026-04-16"),
		WeekEnd: date("2026-04-19"),
		Forecast: []DayForecast{
			{Date: date("2026-04-17"), Estimated: 1.4},
		},
	}
	// remaining=2, totalPlanned=2(today)+1.4(estimated)=3.4 > remaining → engages.
	// Ranked: est 12.0 | today 9 | today 8 | est 4.8. Top 2 keeps one today start.
	result, _ := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("today's 9pt start should outrank the 0.4 tail (4.8) and survive")
	}
	if result[1].IsStarter {
		t.Error("today's 8pt start should lose to the full 12.0 whole-unit estimated entry")
	}
}

// TestApplyGSGate_NearTiePrefersTodayOverEstimated extends the existing
// exact-tie "prefer today" rule to near-ties: an estimated (uncertain)
// future start only slightly ahead of today's value on paper (within the
// certainty margin) should not outrank a start that's already happening.
func TestApplyGSGate_NearTiePrefersTodayOverEstimated(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "today", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		// Bench SP sets roster-mean placeholder to 10.5 — 5% above today's
		// value, inside the certainty margin but NOT an exact tie.
		{Player: fantrax.Player{ID: "bench", PosShortNames: "SP"}, ExpectedPts: 10.5},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    11,
		Today:   date("2026-04-16"),
		WeekEnd: date("2026-04-19"),
		Forecast: []DayForecast{
			{Date: date("2026-04-17"), Estimated: 1.0},
		},
	}
	// remaining=1, totalPlanned=1(today)+1.0(estimated)=2.0 > remaining → gate engages.
	// Estimated entry (10.5) is only ~5% above today (10) — inside the margin,
	// so today should still win despite having the nominally lower value.
	result, _ := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("today's start should beat an estimated future start only marginally ahead in value (near-tie)")
	}
}

// TestApplyGSGate_ConfirmedNearTieUnaffected locks in that the near-tie
// leniency added for estimated future starts does NOT extend to confirmed
// future starts — those remain on the original exact-tie-only rule.
func TestApplyGSGate_ConfirmedNearTieUnaffected(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "today", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    11,
		Today:   date("2026-04-16"),
		WeekEnd: date("2026-04-19"),
		Forecast: []DayForecast{
			// Confirmed (not estimated) future start, 5% above today — inside
			// what would be the estimated-only near-tie margin.
			{Date: date("2026-04-17"), ConfirmedStarters: []float64{10.5}},
		},
	}
	// remaining=1, totalPlanned=2 (1 today + 1 confirmed future) > remaining.
	// Confirmed future (10.5) is strictly higher than today (10) with no
	// near-tie leniency for confirmed starts, so it wins the slot outright.
	result, _ := applyGSGate(scored, budget)
	if result[0].IsStarter {
		t.Error("confirmed future start narrowly ahead of today should still win — no near-tie leniency for confirmed starts")
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

// The gate already knew which starters it flipped; it just threw the answer
// away, leaving pitcherPipelinesFor to re-derive it by inference. These pin the
// report as the authoritative account.
func TestApplyGSGate_ReportNamesTheSuppressedStarters(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Ace", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2", Name: "Filler", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
		{Player: fantrax.Player{ID: "r1", Name: "Reliever", PosShortNames: "RP"}, ExpectedPts: 7, IsStarter: false, HasGame: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    10,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-11"), ConfirmedStarters: []float64{8}},
		},
	}
	// remaining=2, planned=3. Top 2: p1(10), future(8). p2(5) is cut.
	_, report := applyGSGate(scored, budget)

	if len(report.Suppressed) != 1 {
		t.Fatalf("Suppressed = %d entries, want 1: %+v", len(report.Suppressed), report.Suppressed)
	}
	got := report.Suppressed[0]
	if got.PlayerID != "p2" {
		t.Errorf("suppressed PlayerID = %q, want p2", got.PlayerID)
	}
	if got.Name != "Filler" {
		t.Errorf("suppressed Name = %q, want Filler", got.Name)
	}
	if got.ProjectedPts != 5 {
		t.Errorf("suppressed ProjectedPts = %v, want 5", got.ProjectedPts)
	}
	if report.SuppressedPts() != 5 {
		t.Errorf("SuppressedPts() = %v, want 5", report.SuppressedPts())
	}
	if report.Limit != 12 || report.Used != 10 || report.Remaining != 2 {
		t.Errorf("budget echo = %d/%d rem %d, want 12/10 rem 2", report.Used, report.Limit, report.Remaining)
	}
}

// Locked players are exempt from suppression, so they must not appear in the
// report either — otherwise the reported cost includes starts the gate never
// actually declined.
func TestApplyGSGate_ReportExcludesLockedPlayers(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "locked", Name: "Locked SP", PosShortNames: "SP", Locked: true}, ExpectedPts: 9, IsStarter: true},
		{Player: fantrax.Player{ID: "open", Name: "Open SP", PosShortNames: "SP"}, ExpectedPts: 4, IsStarter: true},
	}
	budget := &GSBudget{Limit: 10, Used: 10, Today: date("2026-04-10"), WeekEnd: date("2026-04-12")}

	_, report := applyGSGate(scored, budget)

	for _, s := range report.Suppressed {
		if s.PlayerID == "locked" {
			t.Error("locked player must not appear in the gate report")
		}
	}
	if len(report.Suppressed) != 1 || report.Suppressed[0].PlayerID != "open" {
		t.Errorf("Suppressed = %+v, want exactly the unlocked SP", report.Suppressed)
	}
}

// A budget that covers everything planned suppresses nothing, and the report
// must say so rather than being left zero-valued by accident.
func TestApplyGSGate_AmpleBudgetReportsNoSuppressions(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Ace", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
	}
	budget := &GSBudget{Limit: 12, Used: 0, Today: date("2026-04-10"), WeekEnd: date("2026-04-12")}

	_, report := applyGSGate(scored, budget)

	if len(report.Suppressed) != 0 {
		t.Errorf("Suppressed = %+v, want none", report.Suppressed)
	}
	if report.SuppressedPts() != 0 {
		t.Errorf("SuppressedPts() = %v, want 0", report.SuppressedPts())
	}
	if report.Limit != 12 {
		t.Errorf("Limit = %d, want 12 even with no suppressions", report.Limit)
	}
}

// A nil budget means no GS limit is configured at all — the report must be
// inert, not a zero-limit report that reads as "budget 0/0".
func TestApplyGSGate_NilBudgetReportIsEmpty(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Ace", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
	}
	_, report := applyGSGate(scored, nil)
	if len(report.Suppressed) != 0 || report.Limit != 0 {
		t.Errorf("nil-budget report = %+v, want zero value", report)
	}
}

// rosterbot-1jvj: a forecast day now carries BOTH regimes at once — the clubs
// that have named a starter and the clubs that have not. Reading them as
// either/or discards the estimate on every partially-announced day, which is
// most of them.
func TestGSBudget_FutureDemand_SumsConfirmedAndEstimatedOnTheSameDay(t *testing.T) {
	budget := &GSBudget{
		Limit:   12,
		Today:   date("2026-04-06"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-07"), ConfirmedStarters: []float64{15.0}, Estimated: 1.4},
		},
	}
	got := budget.FutureDemand()
	want := 2.4 // 1 confirmed + 1.4 estimated
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("FutureDemand() = %.2f, want %.2f", got, want)
	}
}

// The gate's own accumulation must agree with FutureDemand(): a mixed day's
// estimate has to reach the ranking as a placeholder entry, or the gate
// under-counts planned starts and declines to suppress when it should.
//
// The numbers sit exactly on the boundary the estimate moves. Two starters
// today, one confirmed start tomorrow, three GS remaining: without the day's
// 1.0 estimate the plan is 3 against 3 and the gate correctly stands down.
// With it the plan is 4, and the cheapest start has to give way.
func TestApplyGSGate_MixedDayEstimateStillCompetesForBudget(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Low", PosShortNames: "SP"}, ExpectedPts: 4, IsStarter: true},
		{Player: fantrax.Player{ID: "p2", Name: "High", PosShortNames: "SP"}, ExpectedPts: 30, IsStarter: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    9,
		Today:   date("2026-04-06"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-07"), ConfirmedStarters: []float64{40}, Estimated: 1.0},
		},
	}

	got, report := applyGSGate(scored, budget)

	if len(report.Suppressed) != 1 || report.Suppressed[0].Name != "Low" {
		t.Fatalf("suppressed = %+v, want exactly the low-value starter", report.Suppressed)
	}
	if got[0].IsStarter || !got[1].IsStarter {
		t.Errorf("IsStarter = [%v %v], want the low start cut and the high one kept",
			got[0].IsStarter, got[1].IsStarter)
	}
}

func TestGSBudget_NeedForFloor(t *testing.T) {
	tests := []struct {
		name   string
		budget *GSBudget
		want   int
	}{
		{"nil budget", nil, 0},
		{"floor unset", &GSBudget{Limit: 12, Used: 3}, 0},
		{"short of floor", &GSBudget{Limit: 12, Floor: 10, Used: 3}, 7},
		{"exactly on floor", &GSBudget{Limit: 12, Floor: 10, Used: 10}, 0},
		{"past floor", &GSBudget{Limit: 12, Floor: 10, Used: 11}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.budget.NeedForFloor(); got != tt.want {
				t.Errorf("NeedForFloor() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestApplyGSGate_FloorProtectsAStartTheCeilingWouldHaveCut is the core
// rosterbot-dpm regression. Same inputs as
// TestApplyGSGate_TightBudget_SuppressesWeakestToday, which suppresses p2 —
// but with a floor the week cannot otherwise reach, p2 must survive.
//
// The scenario is the one the season actually produced: Intentional Balk
// finished on the league floor in three of four periods while five other teams
// hit the cap, and since rosterbot-1jvj revived the forecast the gate can now
// decline starts on exactly such a week.
func TestApplyGSGate_FloorProtectsAStartTheCeilingWouldHaveCut(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
	}
	budget := &GSBudget{
		Limit: 12, Floor: 12, Used: 10,
		Today: date("2026-04-10"), WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-11"), ConfirmedStarters: []float64{8}},
		},
	}
	// remaining=2, need=2, futureConfirmed=1 -> floorReserve=1... but the
	// ceiling cut already keeps p1, so keepToday=1 >= floorReserve and nothing
	// is protected. Raise the need by one to force protection.
	budget.Floor = 12
	budget.Used = 9
	budget.Limit = 12
	// remaining=3, need=3, futureConfirmed=1 -> floorReserve=2: both of today's
	// starters must survive.
	result, report := applyGSGate(scored, budget)
	if !result[0].IsStarter {
		t.Error("p1 should not be suppressed")
	}
	if !result[1].IsStarter {
		t.Error("p2 should be PROTECTED by the floor, not suppressed")
	}
	if len(report.Suppressed) != 0 {
		t.Errorf("Suppressed = %+v, want none", report.Suppressed)
	}
	if report.Floor != 12 || report.FloorNeed != 3 {
		t.Errorf("Floor/FloorNeed = %d/%d, want 12/3", report.Floor, report.FloorNeed)
	}
}

// TestApplyGSGate_FloorNeverProtectsPastLimit pins the one constraint that must
// hold unconditionally: the floor may keep starts alive, but it can never let
// the week spend more than the ceiling allows. Floor above Limit is a
// misconfiguration Fantrax should never emit, and the gate must fail safe on it
// rather than trusting the invariant.
func TestApplyGSGate_FloorNeverProtectsPastLimit(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2", PosShortNames: "SP"}, ExpectedPts: 9, IsStarter: true},
		{Player: fantrax.Player{ID: "p3", PosShortNames: "SP"}, ExpectedPts: 8, IsStarter: true},
		{Player: fantrax.Player{ID: "p4", PosShortNames: "SP"}, ExpectedPts: 7, IsStarter: true},
	}
	budget := &GSBudget{
		// Only ONE start may still be spent, but the floor "needs" nine.
		Limit: 12, Floor: 20, Used: 11,
		Today: date("2026-04-10"), WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{{Date: date("2026-04-11"), Estimated: 2}},
	}
	result, report := applyGSGate(scored, budget)

	kept := 0
	for _, sp := range result {
		if sp.IsStarter {
			kept++
		}
	}
	if kept > budget.Remaining() {
		t.Fatalf("kept %d starters with only %d remaining — floor protection breached the ceiling",
			kept, budget.Remaining())
	}
	// A start kept by the ceiling cut is neither protected nor suppressed, so
	// the three sets partition today's starters as kept+suppressed, with
	// protected a subset of kept. What must never happen is a player in both
	// Protected and Suppressed.
	if kept+len(report.Suppressed) != len(scored) {
		t.Errorf("kept %d + suppressed %d != %d starters", kept, len(report.Suppressed), len(scored))
	}
	sup := make(map[string]bool, len(report.Suppressed))
	for _, s := range report.Suppressed {
		sup[s.PlayerID] = true
	}
	for _, p := range report.Protected {
		if sup[p.PlayerID] {
			t.Errorf("%s is reported as both protected and suppressed", p.PlayerID)
		}
	}
	if report.FloorNeed != 9 {
		t.Errorf("FloorNeed = %d, want 9 — the need is reported honestly even when unreachable", report.FloorNeed)
	}
}

// TestApplyGSGate_FloorDoesNotStakeItselfOnEstimatedStarts pins the deliberate
// asymmetry: floorReserve subtracts CONFIRMED future starts only.
//
// Estimated is rotation math over clubs that have not named a starter — a
// roughly 1-in-5 claim on a pitcher who may never take an active-slot turn
// (rosterbot-tmb9). Counting it toward the floor would decline a certain start
// today to protect a speculative one later, on a week already short of the
// minimum, and weekly GS is use-it-or-lose-it so the unspent probability is
// burned rather than banked. The two budgets below are identical except that
// one's future demand is confirmed and the other's is estimated.
func TestApplyGSGate_FloorDoesNotStakeItselfOnEstimatedStarts(t *testing.T) {
	mk := func() []ScoredPitcher {
		return []ScoredPitcher{
			{Player: fantrax.Player{ID: "p1", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
			{Player: fantrax.Player{ID: "p2", PosShortNames: "SP"}, ExpectedPts: 1, IsStarter: true},
		}
	}
	base := func(f []DayForecast) *GSBudget {
		return &GSBudget{
			Limit: 12, Floor: 12, Used: 10,
			Today: date("2026-04-10"), WeekEnd: date("2026-04-12"), Forecast: f,
		}
	}
	// need=2, remaining=2.
	// Confirmed: floorReserve = 2-2 = 0, so the weak p2 is free to be cut.
	confirmed := base([]DayForecast{{Date: date("2026-04-11"), ConfirmedStarters: []float64{9, 9}}})
	// Estimated: nothing is subtracted, floorReserve = 2, so p2 is protected.
	estimated := base([]DayForecast{{Date: date("2026-04-11"), Estimated: 2}})

	cResult, _ := applyGSGate(mk(), confirmed)
	eResult, eReport := applyGSGate(mk(), estimated)

	if cResult[1].IsStarter {
		t.Error("with TWO confirmed future starts the floor is already covered — the weak start should be cut")
	}
	if !eResult[1].IsStarter {
		t.Error("with only ESTIMATED future demand the floor must not rely on it — the weak start should be protected")
	}
	if len(eReport.Protected) != 1 || eReport.Protected[0].PlayerID != "p2" {
		t.Errorf("Protected = %+v, want exactly p2", eReport.Protected)
	}
}

// TestApplyGSGate_MetFloorLeavesCeilingBehaviourUnchanged pins that the floor
// is inert once satisfied, so every week comfortably clear of the minimum
// behaves exactly as it did before rosterbot-dpm.
func TestApplyGSGate_MetFloorLeavesCeilingBehaviourUnchanged(t *testing.T) {
	mk := func() []ScoredPitcher {
		return []ScoredPitcher{
			{Player: fantrax.Player{ID: "p1", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
			{Player: fantrax.Player{ID: "p2", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
		}
	}
	forecast := []DayForecast{{Date: date("2026-04-11"), ConfirmedStarters: []float64{8}}}
	noFloor := &GSBudget{Limit: 12, Used: 10, Today: date("2026-04-10"), WeekEnd: date("2026-04-12"), Forecast: forecast}
	metFloor := &GSBudget{Limit: 12, Floor: 8, Used: 10, Today: date("2026-04-10"), WeekEnd: date("2026-04-12"), Forecast: forecast}

	a, ra := applyGSGate(mk(), noFloor)
	b, rb := applyGSGate(mk(), metFloor)

	for i := range a {
		if a[i].IsStarter != b[i].IsStarter {
			t.Fatalf("player %d: floor=0 gave IsStarter=%v, met floor gave %v — a met floor must change nothing",
				i, a[i].IsStarter, b[i].IsStarter)
		}
	}
	if len(ra.Suppressed) != len(rb.Suppressed) || len(rb.Protected) != 0 {
		t.Errorf("met floor altered the report: suppressed %d vs %d, protected %d",
			len(ra.Suppressed), len(rb.Suppressed), len(rb.Protected))
	}
	if rb.Floor != 8 || rb.FloorNeed != 0 {
		t.Errorf("Floor/FloorNeed = %d/%d, want 8/0", rb.Floor, rb.FloorNeed)
	}
}

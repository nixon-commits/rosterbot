package fantrax

import (
	"reflect"
	"testing"
)

// TestGSPeriodWalk_MergedAllStarBreakPeriod is the regression test for the
// GetTeamGS bug (rosterbot-uv6): a merged multi-day scoring period (period
// 16, 2026-07-13 to 2026-07-26) walked day-by-day via bare anchor arithmetic
// resolves every day except the one nearest the anchor to a WRONG, unrelated
// period number. gsPeriodWalk must resolve every day in the span to 16.
func TestGSPeriodWalk_MergedAllStarBreakPeriod(t *testing.T) {
	periods := []ScoringPeriod{
		{Number: 14, Caption: "Scoring Period 14", StartDate: date("2026-06-29"), EndDate: date("2026-07-05")},
		{Number: 15, Caption: "Scoring Period 15", StartDate: date("2026-07-06"), EndDate: date("2026-07-12")},
		{Number: 16, Caption: "Scoring Period 16", StartDate: date("2026-07-13"), EndDate: date("2026-07-26")},
		{Number: 17, Caption: "Scoring Period 17", StartDate: date("2026-07-27"), EndDate: date("2026-08-02")},
	}
	sp := periods[2] // period 16, the merged span
	seasonStart := date("2026-03-25")
	today := date("2026-07-27") // day after period 16 ends
	currentPeriod := 17

	got := gsPeriodWalk(sp, periods, currentPeriod, seasonStart, today)

	want := make([]int, 14) // 07-13 through 07-26 inclusive = 14 days
	for i := range want {
		want[i] = 16
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gsPeriodWalk should resolve all 14 merged days to period 16, got %v", got)
	}
}

// TestGSPeriodWalk_CapsAtPeriodEndDate verifies the walk never reads past
// sp.EndDate even if "yesterday" (today-1) would otherwise extend further —
// mirrors GetTeamGS's existing yesterday-capping behavior.
func TestGSPeriodWalk_CapsAtPeriodEndDate(t *testing.T) {
	periods := []ScoringPeriod{
		{Number: 5, Caption: "Scoring Period 5", StartDate: date("2026-04-20"), EndDate: date("2026-04-20")},
	}
	sp := periods[0]
	seasonStart := date("2026-03-25")
	today := date("2026-04-25") // yesterday would be 04-24, well past sp.EndDate
	currentPeriod := 7

	got := gsPeriodWalk(sp, periods, currentPeriod, seasonStart, today)

	want := []int{5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk should cap at sp.EndDate (one day, period 5), got %v", got)
	}
}

// TestGSPeriodWalk_NilWhenPeriodNotStarted verifies the existing
// hasn't-started-yet short-circuit is preserved.
func TestGSPeriodWalk_NilWhenPeriodNotStarted(t *testing.T) {
	sp := ScoringPeriod{Number: 5, StartDate: date("2026-04-20"), EndDate: date("2026-04-20")}
	seasonStart := date("2026-03-25")
	today := date("2026-04-20") // yesterday (04-19) is before sp.StartDate

	got := gsPeriodWalk(sp, nil, 4, seasonStart, today)

	if got != nil {
		t.Fatalf("expected nil for not-yet-started period, got %v", got)
	}
}

// TestGSPeriodWalk_FallsBackWithoutPeriodsList verifies the pre-existing
// anchor/day-math behavior is preserved when no authoritative periods list
// is available (matches the old periodFor closure's fallback).
func TestGSPeriodWalk_FallsBackWithoutPeriodsList(t *testing.T) {
	sp := ScoringPeriod{Number: 91, StartDate: date("2026-06-23"), EndDate: date("2026-06-23")}
	seasonStart := date("2026-03-25")
	today := date("2026-06-24")
	currentPeriod := 92 // anchor: today (06-24) = period 92

	got := gsPeriodWalk(sp, nil, currentPeriod, seasonStart, today)

	want := []int{91} // AnchorPeriodForDate(06-24, 92, 06-23) = 91
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected anchor fallback [91], got %v", got)
	}
}

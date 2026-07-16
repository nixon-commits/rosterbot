package fantrax

import (
	"reflect"
	"testing"
)

// TestGSPeriodWalk_NormalWeekDailyNumbering is the regression test for the
// rosterbot-uv6 mis-fix: GetTeamGS diffs consecutive *daily* YTD snapshots, so
// the walk must return the daily period number for each calendar day (distinct
// per day), NOT the weekly matchup "Scoring Period" number. A normal week
// (period 15, 2026-07-06..07-12) anchored on today=07-13 / currentPeriod=111
// resolves to 104..110 — one number per day. Returning [15,15,…] made every day
// fetch the same snapshot and collapsed the tally to ~one day's worth.
func TestGSPeriodWalk_NormalWeekDailyNumbering(t *testing.T) {
	sp := ScoringPeriod{Number: 15, StartDate: date("2026-07-06"), EndDate: date("2026-07-12")}
	seasonStart := date("2026-03-25")
	today := date("2026-07-13")
	currentPeriod := DailyPeriod(111) // Fantrax's authoritative current daily period on 07-13

	got := gsPeriodWalk(nil, sp, currentPeriod, seasonStart, today)

	want := []DailyPeriod{104, 105, 106, 107, 108, 109, 110}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk must yield distinct daily periods per day, got %v want %v", got, want)
	}
}

// TestGSPeriodWalk_MergedAllStarBreakDailyNumbering: even a merged multi-day
// weekly period (period 16, 2026-07-13..07-26 under one weekly number) is walked
// by daily period — one number per calendar day — because the snapshot API is
// daily-keyed regardless of how the weekly matchup groups those days.
func TestGSPeriodWalk_MergedAllStarBreakDailyNumbering(t *testing.T) {
	sp := ScoringPeriod{Number: 16, StartDate: date("2026-07-13"), EndDate: date("2026-07-26")}
	seasonStart := date("2026-03-25")
	today := date("2026-07-27")       // day after the merged span ends
	currentPeriod := DailyPeriod(125) // daily period on 07-27

	got := gsPeriodWalk(nil, sp, currentPeriod, seasonStart, today)

	// 07-13..07-26 = 14 days, anchored back from 07-27=125 → 111..124.
	want := make([]DailyPeriod, 14)
	for i := range want {
		want[i] = DailyPeriod(111 + i)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged weekly period must still walk by daily period, got %v want %v", got, want)
	}
}

// TestGSPeriodWalk_CapsAtPeriodEndDate verifies the walk never reads past
// sp.EndDate even if "yesterday" (today-1) would otherwise extend further.
func TestGSPeriodWalk_CapsAtPeriodEndDate(t *testing.T) {
	sp := ScoringPeriod{Number: 5, StartDate: date("2026-04-20"), EndDate: date("2026-04-20")}
	seasonStart := date("2026-03-25")
	today := date("2026-04-25")      // yesterday would be 04-24, well past sp.EndDate
	currentPeriod := DailyPeriod(31) // daily period on 04-25

	got := gsPeriodWalk(nil, sp, currentPeriod, seasonStart, today)

	// Single-day period; anchored back from 04-25=31 → 04-20=26.
	want := []DailyPeriod{26}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk should cap at sp.EndDate (one day), got %v want %v", got, want)
	}
}

// TestGSPeriodWalk_NilWhenPeriodNotStarted verifies the hasn't-started-yet
// short-circuit is preserved.
func TestGSPeriodWalk_NilWhenPeriodNotStarted(t *testing.T) {
	sp := ScoringPeriod{Number: 5, StartDate: date("2026-04-20"), EndDate: date("2026-04-20")}
	seasonStart := date("2026-03-25")
	today := date("2026-04-20") // yesterday (04-19) is before sp.StartDate

	got := gsPeriodWalk(nil, sp, 27, seasonStart, today)

	if got != nil {
		t.Fatalf("expected nil for not-yet-started period, got %v", got)
	}
}

// TestGSPeriodWalk_DayMathFallback verifies the season-start day-math fallback
// when Fantrax's current period isn't available (currentPeriod == 0).
func TestGSPeriodWalk_DayMathFallback(t *testing.T) {
	sp := ScoringPeriod{Number: 91, StartDate: date("2026-06-23"), EndDate: date("2026-06-23")}
	seasonStart := date("2026-03-25")
	today := date("2026-06-24")

	got := gsPeriodWalk(nil, sp, 0, seasonStart, today) // currentPeriod unknown

	want := []DailyPeriod{PeriodForDate(seasonStart, date("2026-06-23"))}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected day-math fallback %v, got %v", want, got)
	}
}

package cmd

import (
	"errors"
	"testing"
	"time"
)

// TestResolveDatePeriod_TodayTrustsFantraxCurrentPeriod is the regression test
// for the period-mismatch bug: Fantrax inserted an extra daily scoring period
// mid-season, so its authoritative current period (92) ran one ahead of naive
// season-start day math (PeriodForDate => 91). The optimizer reads today's
// roster under Fantrax's current period, so the apply must target that same
// period — not the date-math value — or the write lands on the wrong period and
// silently no-ops on today's lineup.
func TestResolveDatePeriod_TodayTrustsFantraxCurrentPeriod(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)

	got := resolveDatePeriod(true, 92, nil, seasonStart, today)

	if got != 92 {
		t.Fatalf("today should use Fantrax current period 92, got %d", got)
	}
}

// TestResolveDatePeriod_TodayFallsBackWhenCurrentPeriodUnknown verifies that if
// the current-period lookup failed (periodErr) or returned 0, today falls back
// to date math rather than applying to period 0.
func TestResolveDatePeriod_TodayFallsBackWhenCurrentPeriodUnknown(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)

	if got := resolveDatePeriod(true, 0, errors.New("boom"), seasonStart, today); got != 91 {
		t.Fatalf("periodErr should fall back to PeriodForDate 91, got %d", got)
	}
	if got := resolveDatePeriod(true, 0, nil, seasonStart, today); got != 91 {
		t.Fatalf("zero current period should fall back to PeriodForDate 91, got %d", got)
	}
}

// TestResolveDatePeriod_FutureDateUsesDateMath verifies non-today dates keep
// using PeriodForDate (the only available signal for future days), ignoring the
// fetched current period.
func TestResolveDatePeriod_FutureDateUsesDateMath(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	future := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)

	got := resolveDatePeriod(false, 92, nil, seasonStart, future)

	if got != 93 { // (06-25 - 03-25) = 92 days, +1
		t.Fatalf("future date should use PeriodForDate 93, got %d", got)
	}
}

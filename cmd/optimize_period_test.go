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
// period — not the date-math value.
func TestResolveDatePeriod_TodayTrustsFantraxCurrentPeriod(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)

	got := resolveDatePeriod(92, nil, seasonStart, today, today)

	if got != 92 {
		t.Fatalf("today should use Fantrax current period 92, got %d", got)
	}
}

// TestResolveDatePeriod_FutureAnchorsOnCurrentPeriod verifies the future
// --matchup window also rides the authoritative anchor (current period + whole
// days), not season-start day math. Naive PeriodForDate(03-25, 06-25) = 93, but
// Fantrax's count is one ahead, so the correct period is 94.
func TestResolveDatePeriod_FutureAnchorsOnCurrentPeriod(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	future := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)

	got := resolveDatePeriod(92, nil, seasonStart, today, future)

	if got != 94 { // anchor 92 @ 06-23, +2 days
		t.Fatalf("future date should anchor on current period (94), got %d", got)
	}
}

// TestResolveDatePeriod_FallsBackWhenCurrentPeriodUnknown verifies that if the
// current-period lookup failed (periodErr) or returned 0, resolution falls back
// to season-start day math rather than anchoring on a bogus period.
func TestResolveDatePeriod_FallsBackWhenCurrentPeriodUnknown(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)

	if got := resolveDatePeriod(0, errors.New("boom"), seasonStart, today, today); got != 91 {
		t.Fatalf("periodErr should fall back to PeriodForDate 91, got %d", got)
	}
	if got := resolveDatePeriod(0, nil, seasonStart, today, today); got != 91 {
		t.Fatalf("zero current period should fall back to PeriodForDate 91, got %d", got)
	}
}

package fantrax

import "testing"

// TestResolvePeriod_PrefersAuthoritativePeriodsList verifies that when the
// live periods list (from GetScoringPeriodsAndTeams) contains an entry whose
// date range covers the target date, its Number wins — even when it
// disagrees with both day-math and anchor arithmetic. This is the case an
// inserted mid-season period (doubleheader/postponed-game makeup) produces:
// the periods list is exact, the arithmetic tiers are not.
func TestResolvePeriod_PrefersAuthoritativePeriodsList(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 23)
	periods := []ScoringPeriod{
		{Number: 92, StartDate: today, EndDate: today},
	}

	// Day-math for 06-23 would say 91; anchor arithmetic (currentPeriod=90,
	// deliberately wrong) would say 90. Both disagree with the periods list.
	got := ResolvePeriod(periods, 90, seasonStart, today, today)

	if got != 92 {
		t.Fatalf("ResolvePeriod should trust the periods list (92), got %d", got)
	}
}

// TestResolvePeriod_FallsBackToAnchorWhenDateNotInPeriodsList verifies that
// when the periods list doesn't cover the target date (e.g. a future date
// beyond Fantrax's currently-published range), resolution falls back to
// anchoring on today's authoritative current period.
func TestResolvePeriod_FallsBackToAnchorWhenDateNotInPeriodsList(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 23)
	future := d(2026, 6, 25)
	periods := []ScoringPeriod{
		{Number: 92, StartDate: today, EndDate: today}, // doesn't cover future
	}

	got := ResolvePeriod(periods, 92, seasonStart, today, future)

	want := AnchorPeriodForDate(today, 92, future) // 94
	if got != want {
		t.Fatalf("ResolvePeriod should anchor on current period (%d), got %d", want, got)
	}
}

// TestResolvePeriod_FallsBackToDayMathWhenNothingElseAvailable verifies the
// last-resort tier: no periods list coverage and no known current period.
func TestResolvePeriod_FallsBackToDayMathWhenNothingElseAvailable(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 23)

	got := ResolvePeriod(nil, 0, seasonStart, today, today)

	want := PeriodForDate(seasonStart, today) // 91
	if got != want {
		t.Fatalf("ResolvePeriod should fall back to day math (%d), got %d", want, got)
	}
}

// TestResolvePeriod_EmptyPeriodsListDoesNotPanic guards the nil/empty-slice
// input shape callers pass when GetScoringPeriodsAndTeams fails.
func TestResolvePeriod_EmptyPeriodsListDoesNotPanic(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 23)

	got := ResolvePeriod([]ScoringPeriod{}, 92, seasonStart, today, today)

	if got != 92 {
		t.Fatalf("empty periods list should anchor on current period (92), got %d", got)
	}
}

package fantrax

import (
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// TestAnchorPeriodForDate verifies the date→period mapping is anchored on an
// authoritative (anchorPeriod @ anchorDate) pair rather than season-start day
// counting — the fix for Fantrax inserting extra daily periods mid-season.
func TestAnchorPeriodForDate(t *testing.T) {
	// Fantrax says today (2026-06-23) is period 92 (one ahead of naive
	// season-start day math, which gives 91).
	today := d(2026, 6, 23)
	const cur = 92

	cases := []struct {
		name string
		date time.Time
		want int
	}{
		{"anchor day maps to anchor period", today, 92},
		{"next day is +1", d(2026, 6, 24), 93},
		{"yesterday is -1", d(2026, 6, 22), 91},
		{"a week ahead", d(2026, 6, 30), 99},
	}
	for _, tc := range cases {
		if got := AnchorPeriodForDate(today, cur, tc.date); got != tc.want {
			t.Errorf("%s: AnchorPeriodForDate(%s, %d, %s) = %d, want %d",
				tc.name, today.Format("01-02"), cur, tc.date.Format("01-02"), got, tc.want)
		}
	}
}

// TestPeriodForDate_IsSeasonStartAnchoredSpecialCase pins that the legacy
// season-start helper still behaves as before (period 1 = seasonStart) so its
// fallback callers are unchanged.
func TestPeriodForDate_IsSeasonStartAnchoredSpecialCase(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	if got := PeriodForDate(seasonStart, seasonStart); got != 1 {
		t.Errorf("season opener should be period 1, got %d", got)
	}
	if got := PeriodForDate(seasonStart, d(2026, 6, 23)); got != 91 {
		t.Errorf("naive day-math for 2026-06-23 should be 91, got %d", got)
	}
}

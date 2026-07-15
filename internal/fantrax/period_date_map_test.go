package fantrax

import (
	"testing"
	"time"
)

func TestParsePeriodList(t *testing.T) {
	entries := []interface{}{
		"1 (Wed Mar 25)",
		"104 (Mon Jul 6)",
		"187 (Sun Sep 27)",
		"not a period entry",
		42, // non-string, must be skipped
	}
	m := parsePeriodList(entries, 2026, time.March)

	want := map[string]DailyPeriod{
		"2026-03-25": 1,
		"2026-07-06": 104,
		"2026-09-27": 187,
	}
	if len(m) != len(want) {
		t.Fatalf("len=%d, want %d (map=%v)", len(m), len(want), m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%q]=%d, want %d", k, m[k], v)
		}
	}
}

func TestDailyPeriodForDate(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	// Pre-seed the in-memory memo so no auth/network is touched.
	c := &Client{periodMapMemo: map[string]DailyPeriod{
		"2026-07-06": 104,
	}}
	d := func(y int, m time.Month, day int) time.Time {
		return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	}
	// hit → authoritative period from the map
	if got := c.dailyPeriodForDate(seasonStart, d(2026, 7, 6)); got != 104 {
		t.Errorf("hit: got %d, want 104", got)
	}
	// miss → soft-fallback to naive PeriodForDate
	miss := d(2026, 4, 1)
	if got := c.dailyPeriodForDate(seasonStart, miss); got != PeriodForDate(seasonStart, miss) {
		t.Errorf("miss: got %d, want naive %d", got, PeriodForDate(seasonStart, miss))
	}
}

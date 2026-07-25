package fantrax

import (
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time {
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// TestPeriodForDate_CountsOnePeriodPerCalendarDay pins the fallback tier of
// DailyPeriodFor: period 1 = seasonStart, +1 per calendar day.
//
// The values below are the authoritative ones — verified 2026-07-25 against the
// live periodList map, which agrees with this arithmetic on all 187 dates of the
// 2026 season (2026-03-25..2026-09-27). The daily axis carries no merged or
// inserted periods; Fantrax's merging (e.g. the All-Star break) happens on the
// weekly axis only.
//
// (rosterbot-xyk deleted TestAnchorPeriodForDate along with AnchorPeriodForDate.
// Its fixture asserted Fantrax reported 2026-06-23 as period 92, "one ahead of
// naive season-start day math" — the live map says 91, same as the day math, so
// the drift that motivated the anchor was never observed in production.)
func TestPeriodForDate_CountsOnePeriodPerCalendarDay(t *testing.T) {
	seasonStart := d(2026, 3, 25)

	cases := []struct {
		name string
		date time.Time
		want DailyPeriod
	}{
		{"season opener is period 1", seasonStart, 1},
		{"next day is +1", d(2026, 3, 26), 2},
		{"2026-04-20", d(2026, 4, 20), 27},
		{"2026-06-23", d(2026, 6, 23), 91},
		{"2026-07-06", d(2026, 7, 6), 104},
		{"first day of the merged All-Star weekly period", d(2026, 7, 13), 111},
		{"last day of the merged All-Star weekly period", d(2026, 7, 26), 124},
		{"season finale", d(2026, 9, 27), 187},
	}
	for _, tc := range cases {
		if got := PeriodForDate(seasonStart, tc.date); got != tc.want {
			t.Errorf("%s: PeriodForDate(%s) = %d, want %d",
				tc.name, tc.date.Format("2006-01-02"), got, tc.want)
		}
	}
}

// TestPeriodForDate_MergedWeeklyPeriodStillAdvancesDaily is the axis-confusion
// guard: weekly period 16 spans 2026-07-13..07-26 as ONE weekly number, but the
// daily axis must still advance one per calendar day across that span. A daily
// resolver that ever returned the same number twice in a row here would
// reproduce the rosterbot-z3b duplicate-notification flood.
func TestPeriodForDate_MergedWeeklyPeriodStillAdvancesDaily(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	seen := map[DailyPeriod]bool{}
	prev := DailyPeriod(0)
	for day := 13; day <= 26; day++ {
		got := PeriodForDate(seasonStart, d(2026, 7, day))
		if seen[got] {
			t.Fatalf("2026-07-%02d reused daily period %d — the merged weekly period must not collapse days", day, got)
		}
		if prev != 0 && got != prev+1 {
			t.Errorf("2026-07-%02d: got %d, want %d (must advance exactly one per day)", day, got, prev+1)
		}
		seen[got] = true
		prev = got
	}
}

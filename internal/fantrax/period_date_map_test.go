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

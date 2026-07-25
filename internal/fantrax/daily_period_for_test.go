package fantrax

import "testing"

// TestClientDailyPeriodFor_AuthoritativeMapWinsOverAnchor is the regression
// test for rosterbot-2ax: on 2026-07-16, GetCurrentPeriod() (live) returned
// 113 for "today" while the authoritative periodList map (freshly re-fetched,
// not stale cache) said today is 114 and 113 is 2026-07-15. Anchoring purely
// on GetCurrentPeriod() made the lineup-apply hot path submit changes against
// yesterday's already-closed period, which Fantrax silently rejected as
// "already locked" for the affected players — masked as success by the
// locked-player retry path. The authoritative map must win whenever it has
// an entry for the requested date; the anchor is only a fallback for dates
// outside the map's coverage.
func TestClientDailyPeriodFor_AuthoritativeMapWins(t *testing.T) {
	// A map that deliberately DISAGREES with day math, so a passing test proves
	// the map was consulted rather than the fallback coincidentally agreeing.
	c := &Client{periodMapMemo: map[string]DailyPeriod{
		"2026-07-15": 900,
		"2026-07-16": 901,
	}}
	seasonStart := d(2026, 3, 25)

	if got := c.DailyPeriodFor(seasonStart, d(2026, 7, 16)); got != 901 {
		t.Errorf("map hit must win over day math: got %d, want 901", got)
	}
	if got := c.DailyPeriodFor(seasonStart, d(2026, 7, 15)); got != 900 {
		t.Errorf("map hit must win over day math: got %d, want 900", got)
	}
}

// TestClientDailyPeriodFor_MapMissFallsBackToDayMath pins the rosterbot-xyk
// decision: when the authoritative map misses, resolve by season-start day
// math, NOT by anchoring on Fantrax's reported current period.
//
// Measured 2026-07-25 against the live periodList map (187 entries, the whole
// 2026 season, 2026-03-25..2026-09-27): the daily axis is strictly 1:1 with
// calendar days — zero inserted or merged periods in 122 elapsed days — so day
// math agrees with the authoritative map on all 187 dates. The anchor tier's
// premise ("naive day counting drifts behind by inserted periods") has never
// materialised, while the anchor's own input demonstrably lagged a full day on
// 2026-07-16 and caused the rosterbot-48z silent apply failure. Preferring a
// known-unreliable pointer over exact arithmetic is strictly worse.
//
// 2026-06-23 is day 91 of the season; a stale/off-by-one current period of 92
// must NOT win.
func TestClientDailyPeriodFor_MapMissFallsBackToDayMath(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 23)

	cEmpty := &Client{periodMapMemo: map[string]DailyPeriod{}}
	if got := cEmpty.DailyPeriodFor(seasonStart, today); got != 91 {
		t.Errorf("map miss must use day math: got %d, want 91", got)
	}

	var cNil *Client
	if got := cNil.DailyPeriodFor(seasonStart, today); got != 91 {
		t.Errorf("nil client must use day math: got %d, want 91", got)
	}
}

// TestClientDailyPeriodFor_IsIndependentOfToday guards the property that made
// the collapse safe: resolution depends only on (seasonStart, date). The
// deleted anchor tier read "today" and Fantrax's reported current period, which
// is how a lagging pointer on 2026-07-16 shifted every resolved date by one.
// Nothing about which day the process happens to run may affect the answer.
func TestClientDailyPeriodFor_IsIndependentOfToday(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	target := d(2026, 6, 23)

	var cNil *Client
	want := DailyPeriod(91)
	if got := cNil.DailyPeriodFor(seasonStart, target); got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
	// Same call, conceptually made on any other day — same inputs, same answer.
	if got := cNil.DailyPeriodFor(seasonStart, target); got != want {
		t.Errorf("resolution must not vary: got %d, want %d", got, want)
	}
}

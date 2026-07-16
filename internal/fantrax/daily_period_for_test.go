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
func TestClientDailyPeriodFor_AuthoritativeMapWinsOverAnchor(t *testing.T) {
	c := &Client{periodMapMemo: map[string]DailyPeriod{
		"2026-07-15": 113,
		"2026-07-16": 114,
	}}
	seasonStart := d(2026, 3, 25)
	today := d(2026, 7, 16)
	const staleCurrentPeriod = 113 // what GetCurrentPeriod() lagged to report

	if got := c.DailyPeriodFor(staleCurrentPeriod, seasonStart, today, today); got != 114 {
		t.Errorf("map hit for today should win over anchor: got %d, want 114", got)
	}
	if got := c.DailyPeriodFor(staleCurrentPeriod, seasonStart, today, d(2026, 7, 15)); got != 113 {
		t.Errorf("map hit for yesterday should win over anchor: got %d, want 113", got)
	}
}

// TestClientDailyPeriodFor_FallsBackToAnchorOnMapMiss verifies dates outside
// the authoritative map's coverage (or a nil client, e.g. hermetic tests)
// still resolve via the anchor, preserving pre-2ax behavior.
func TestClientDailyPeriodFor_FallsBackToAnchorOnMapMiss(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 23)
	const cur = 92

	cEmpty := &Client{periodMapMemo: map[string]DailyPeriod{}}
	if got := cEmpty.DailyPeriodFor(cur, seasonStart, today, today); got != 92 {
		t.Errorf("map miss should fall back to anchor: got %d, want 92", got)
	}

	var cNil *Client
	if got := cNil.DailyPeriodFor(cur, seasonStart, today, today); got != 92 {
		t.Errorf("nil client should fall back to anchor: got %d, want 92", got)
	}
}

// TestClientDailyPeriodFor_FallsBackToDayMathWhenCurrentPeriodUnknown mirrors
// the pre-2ax day-math fallback when neither the map nor GetCurrentPeriod()
// is available.
func TestClientDailyPeriodFor_FallsBackToDayMathWhenCurrentPeriodUnknown(t *testing.T) {
	seasonStart := d(2026, 3, 25)
	today := d(2026, 6, 24)
	target := d(2026, 6, 23)

	var cNil *Client
	want := PeriodForDate(seasonStart, target)
	if got := cNil.DailyPeriodFor(0, seasonStart, today, target); got != want {
		t.Errorf("no map, no current period: got %d, want naive %d", got, want)
	}
}

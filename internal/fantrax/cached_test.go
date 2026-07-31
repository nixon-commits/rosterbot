package fantrax

import (
	"errors"
	"testing"
	"time"
)

// Tier durations are the on-disk TTL contract every cached method inherits.
// Pinning them here is the code-level assertion for acceptance criterion #2
// ("same TTL as before") and #3 ("the three-tier scheme is visible in code").
func TestCacheTierDurations(t *testing.T) {
	cases := []struct {
		tier cacheTier
		want time.Duration
	}{
		{tierToday, 15 * time.Minute},
		{tierStable, 7 * 24 * time.Hour},
		// tierPast is asserted by behaviour rather than by number in
		// TestPastPeriodTTL_OutlivesAFullSeason — what matters is that it
		// outlives a season, not the exact figure.
		{tierPast, PastPeriodTTL},
	}
	for _, tc := range cases {
		if got := tc.tier.duration(); got != tc.want {
			t.Errorf("tier %d duration = %v, want %v", tc.tier, got, tc.want)
		}
	}
}

// cached reads through the FileCache: the first call misses and invokes the
// fetch; the second call for the same key hits and does not.
func TestCached_MissThenHit(t *testing.T) {
	c := &Client{cacheDir: t.TempDir()}
	calls := 0
	fetch := func() (int, error) {
		calls++
		return 42, nil
	}

	v1, err := cached(c, "k", tierStable, fetch)
	if err != nil || v1 != 42 {
		t.Fatalf("first call = (%d, %v), want (42, nil)", v1, err)
	}
	v2, err := cached(c, "k", tierStable, fetch)
	if err != nil || v2 != 42 {
		t.Fatalf("second call = (%d, %v), want (42, nil)", v2, err)
	}
	if calls != 1 {
		t.Errorf("fetch invoked %d times, want 1 (second call should hit cache)", calls)
	}
}

// An empty cacheDir is the --no-cache / hermetic-test path: every call must
// short-circuit to a live fetch and persist nothing (criterion #4).
func TestCached_EmptyCacheDirAlwaysFetches(t *testing.T) {
	c := &Client{cacheDir: ""}
	calls := 0
	fetch := func() (int, error) {
		calls++
		return calls, nil
	}

	if _, err := cached(c, "k", tierToday, fetch); err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if _, err := cached(c, "k", tierToday, fetch); err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if calls != 2 {
		t.Errorf("fetch invoked %d times, want 2 (no-cache must not memoize)", calls)
	}
}

// A fetch error propagates and is not cached, so the next call re-fetches.
func TestCached_FetchErrorPropagates(t *testing.T) {
	c := &Client{cacheDir: t.TempDir()}
	sentinel := errors.New("boom")
	calls := 0
	fetch := func() (int, error) {
		calls++
		if calls == 1 {
			return 0, sentinel
		}
		return 7, nil
	}

	if _, err := cached(c, "k", tierToday, fetch); !errors.Is(err, sentinel) {
		t.Fatalf("first call error = %v, want %v", err, sentinel)
	}
	v, err := cached(c, "k", tierToday, fetch)
	if err != nil || v != 7 {
		t.Fatalf("second call = (%d, %v), want (7, nil) — error must not be cached", v, err)
	}
}

// cachedForPeriod must not resolve the per-period tier on the no-cache path,
// since tierForPeriod performs a season-range fetch. Here the client has a nil
// auth client, so any tier resolution would panic; the test passing proves the
// short-circuit runs first (the laziness invariant the two-helper split exists
// to preserve).
func TestCachedForPeriod_EmptyCacheDirSkipsTierResolution(t *testing.T) {
	c := &Client{cacheDir: ""}
	calls := 0
	fetch := func() (string, error) {
		calls++
		return "ok", nil
	}

	v, err := cachedForPeriod(c, "k", "4", DailyPeriod(105), fetch)
	if err != nil || v != "ok" {
		t.Fatalf("call = (%q, %v), want (\"ok\", nil)", v, err)
	}
	if calls != 1 {
		t.Errorf("fetch invoked %d times, want 1", calls)
	}
}

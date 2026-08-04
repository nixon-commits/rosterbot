package fantrax

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// seedSeasonRange populates the cached season range so periodCachePolicy can
// resolve a tier and a season year without reaching Fantrax. Without it the
// policy falls back to (tierToday, current calendar year) on the fetch error —
// which both sides would still agree on, so it would hide a genuine season-scope
// mismatch behind a shared fallback.
func seedSeasonRange(t *testing.T, c *Client, start time.Time) {
	t.Helper()
	_, err := cache.New[seasonDateRange](c.cacheDir, stableTTL).Get(
		cache.Key(keySeasonRange, c.leagueID),
		func() (seasonDateRange, error) {
			return seasonDateRange{First: start, Last: start.AddDate(0, 6, 0)}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

// InvalidatePeriodRosterCache exists so a second optimize run after ApplyLineup
// re-fetches the roster instead of reading the pre-apply snapshot. Between
// rosterbot-qoa (2026-07-31, which moved every per-period key to
// <prefix>-<teamID>-<season>-<period>) and this fix it built the old, unscoped
// grammar and therefore deleted nothing at all — silently, because deleting a
// key that does not exist is not an error.
//
// The test drives the real reader (cachedForPeriod, exactly what
// GetHitterRosterForPeriod calls) rather than comparing two key strings, so it
// fails on any future divergence between the two sides regardless of how the
// key comes to be built.
func TestInvalidatePeriodRosterCache_DropsTheEntryTheReaderWrote(t *testing.T) {
	c := &Client{teamID: "team1", leagueID: "lg1", cacheDir: t.TempDir()}
	seedSeasonRange(t, c, time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))

	// Well behind today, so periodCachePolicy settles it to tierPast — the tier
	// whose keys carry the season and the one an unbounded TTL makes permanent.
	const period = DailyPeriod(50)

	fetches := 0
	read := func() ([]Player, error) {
		fetches++
		return []Player{{ID: "p1"}}, nil
	}
	prefixes := []string{keyHitterRoster, keyPitcherRoster}

	// Populate both entries the way the roster readers do, then confirm they
	// are genuinely cached — otherwise a later "miss" would prove nothing.
	for _, p := range prefixes {
		if _, err := cachedForPeriod(c, p, c.teamID, period, read); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 2 {
		t.Fatalf("populating: got %d fetches, want 2", fetches)
	}
	for _, p := range prefixes {
		if _, err := cachedForPeriod(c, p, c.teamID, period, read); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 2 {
		t.Fatalf("re-reading: got %d fetches, want 2 — the entries were never cached", fetches)
	}

	c.InvalidatePeriodRosterCache(period)

	for i, p := range prefixes {
		if _, err := cachedForPeriod(c, p, c.teamID, period, read); err != nil {
			t.Fatal(err)
		}
		if want := 3 + i; fetches != want {
			t.Errorf("%s: got %d fetches, want %d — InvalidatePeriodRosterCache did not "+
				"drop the entry the reader wrote, so a post-apply run still sees the "+
				"pre-apply snapshot", p, fetches, want)
		}
	}
}

// The unscoped "today" keys carry no season by design (they are todayTTL, not
// tierPast), so they must keep using the plain grammar — fixing the period keys
// must not sweep these into season scoping and break them the other way.
func TestInvalidatePeriodRosterCache_AlsoDropsTheUnscopedTodayKeys(t *testing.T) {
	c := &Client{teamID: "team1", leagueID: "lg1", cacheDir: t.TempDir()}
	seedSeasonRange(t, c, time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC))

	fetches := 0
	read := func() ([]Player, error) {
		fetches++
		return []Player{{ID: "p1"}}, nil
	}
	keys := []string{
		cache.Key(keyHitterRoster, c.teamID),
		cache.Key(keyPitcherRoster, c.teamID),
	}
	for _, k := range keys {
		if _, err := cached(c, k, tierToday, read); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 2 {
		t.Fatalf("populating: got %d fetches, want 2", fetches)
	}

	c.InvalidatePeriodRosterCache(DailyPeriod(50))

	for i, k := range keys {
		if _, err := cached(c, k, tierToday, read); err != nil {
			t.Fatal(err)
		}
		if want := 3 + i; fetches != want {
			t.Errorf("%s: got %d fetches, want %d — the unscoped today key survived",
				k, fetches, want)
		}
	}
}

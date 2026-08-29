package fantrax

import (
	"sort"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache/cachetest"
)

// fullSeasonSpan is the widest an MLB season gets — 2026 runs 2026-03-25 through
// 2026-09-27, 187 calendar days. Used as the floor a past-period TTL has to
// clear.
const fullSeasonSpan = 187 * 24 * time.Hour

// A past-period snapshot is immutable by definition, so its cache entry must
// outlive the season that produced it. At the old 30-day TTL every weekly
// recap-site rebuild re-fetched every week older than a month, making the build
// O(season length) and tripling per-week cost by July (rosterbot-qoa).
func TestPastPeriodTTL_OutlivesAFullSeason(t *testing.T) {
	if PastPeriodTTL <= fullSeasonSpan {
		t.Errorf("PastPeriodTTL = %v, want > %v (a full season) so an opening-day "+
			"snapshot is still a cache hit when the season-finale recap renders",
			PastPeriodTTL, fullSeasonSpan)
	}
	if got := tierPast.duration(); got != PastPeriodTTL {
		t.Errorf("tierPast.duration() = %v, want PastPeriodTTL (%v)", got, PastPeriodTTL)
	}
}

// An effectively-infinite TTL is only safe if "past" excludes periods whose
// snapshot can still move. Fantrax's StatsType=1 roster YTD keeps settling for
// up to ~a day after a period closes, and the current-period math runs in UTC
// while the games run in ET — so the last few closed periods must stay on the
// short tier. Without this a snapshot captured mid-settle is pinned wrong
// forever instead of self-healing at the old 30-day expiry.
func TestTierFor_RecentlyClosedPeriodsStayVolatile(t *testing.T) {
	const cur = DailyPeriod(120)
	cases := []struct {
		name   string
		period DailyPeriod
		want   cacheTier
	}{
		{"future period", cur + 1, tierToday},
		{"current period", cur, tierToday},
		{"just closed", cur - 1, tierToday},
		{"closed 3 periods ago (still settling)", cur - 3, tierToday},
		{"closed 4 periods ago (settled)", cur - 4, tierPast},
		{"opening day", 1, tierPast},
	}
	for _, tc := range cases {
		if got := tierFor(tc.period, cur); got != tc.want {
			t.Errorf("%s: tierFor(%d, %d) = %v, want %v", tc.name, tc.period, cur, got, tc.want)
		}
	}
}

// snapshotTTL is the shared per-period TTL decision for the caller-supplied
// snapshot caches (DailyFantasyPoints, GetTeamPitcherStarts). A caller asking
// for the long immutable-past TTL must not get it applied to a volatile period.
func TestSnapshotTTL(t *testing.T) {
	const cur = DailyPeriod(120)
	cases := []struct {
		name      string
		callerTTL time.Duration
		period    DailyPeriod
		want      time.Duration
	}{
		{"settled period keeps the caller's long TTL", PastPeriodTTL, cur - 4, PastPeriodTTL},
		{"current period capped to todayTTL", PastPeriodTTL, cur, todayTTL},
		{"still-settling period capped to todayTTL", PastPeriodTTL, cur - 2, todayTTL},
		{"caching off stays off for a volatile period", 0, cur, 0},
		{"caching off stays off for a settled period", 0, cur - 4, 0},
		{"a caller TTL shorter than todayTTL is not lengthened", time.Minute, cur, time.Minute},
	}
	for _, tc := range cases {
		if got := snapshotTTL(tc.callerTTL, tc.period, cur); got != tc.want {
			t.Errorf("%s: snapshotTTL(%v, %d, %d) = %v, want %v",
				tc.name, tc.callerTTL, tc.period, cur, got, tc.want)
		}
	}
}

// Per-period cache keys carry no season today, which is harmless at a 30-day
// TTL (the off-season is ~6 months) but not at an effectively-infinite one:
// next season's period 5 would serve this season's frozen snapshot. Every
// key held at the past tier must scope by season.
func TestSeasonScopedKey(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "roster stats",
			got:  seasonScopedKey(keyRosterStats, "4", 2026, 104),
			want: "fantrax-roster-stats-4-2026-104",
		},
		{
			name: "pitcher GS",
			got:  seasonScopedKey(keyPitcherGS, "4", 2026, 104),
			want: "fantrax-pitcher-gs-4-2026-104",
		},
		{
			// GS limits are keyed by the weekly period, not the daily one, but
			// the collision risk and the grammar are identical.
			name: "GS limits",
			got:  seasonScopedKey(keyGSLimits, "4", 2026, 16),
			want: "fantrax-gs-limits-4-2026-16",
		},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s key = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// The end-to-end guard for the fix: with the long immutable-past TTL, a second
// GetTeamPitcherStarts run over the same window must re-fetch only the periods
// that can still change (current + the settling lookback) and serve every
// settled period from cache. Before the fix GetTeamPitcherStarts applied the
// caller's flat TTL to every period, so an infinite TTL would pin the live
// period's snapshot forever.
func TestGetTeamPitcherStarts_RefetchesVolatilePeriodsOnly(t *testing.T) {
	// Anchor the season so today is period 10: periods 7..10 are volatile
	// (recentPeriodLookback = 3), periods 4..6 are settled.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seasonStart := today.AddDate(0, 0, -9)
	const curPeriod = DailyPeriod(10)

	c := &Client{teamID: "4", periodMapMemo: map[string]DailyPeriod{}}

	var fetched []DailyPeriod
	restore := stubPlayerGSSnapshot(func(_ *Client, _ string, period DailyPeriod) (map[string]playerGSSnapshot, error) {
		fetched = append(fetched, period)
		return map[string]playerGSSnapshot{}, nil
	})
	defer restore()

	cacheDir := t.TempDir()
	// Window covers periods 5..10; the baseline day pulls period 4 as well.
	start, end := today.AddDate(0, 0, -5), today

	if _, err := c.GetTeamPitcherStarts("4", start, end, seasonStart, cacheDir, PastPeriodTTL); err != nil {
		t.Fatalf("cold run: %v", err)
	}
	if want := []DailyPeriod{4, 5, 6, 7, 8, 9, 10}; !samePeriods(fetched, want) {
		t.Fatalf("cold run fetched %v, want %v", fetched, want)
	}

	// Age every cached entry past todayTTL but far short of PastPeriodTTL.
	if n := cachetest.AgeEntries(t, cacheDir, time.Hour); n == 0 {
		t.Fatal("AgeEntries aged 0 entries — the cold run wrote nothing to age")
	}

	fetched = nil
	if _, err := c.GetTeamPitcherStarts("4", start, end, seasonStart, cacheDir, PastPeriodTTL); err != nil {
		t.Fatalf("warm run: %v", err)
	}
	want := []DailyPeriod{curPeriod - 3, curPeriod - 2, curPeriod - 1, curPeriod}
	if !samePeriods(fetched, want) {
		t.Errorf("warm run fetched %v, want %v — settled periods must stay cached "+
			"and volatile ones must refresh", fetched, want)
	}
}

// stubPlayerGSSnapshot swaps the pitcher-GS snapshot fetch for a canned one and
// returns a restore func, mirroring how fetchPeriodSnapshotFn is stubbed for
// DailyFantasyPoints.
func stubPlayerGSSnapshot(fn func(*Client, string, DailyPeriod) (map[string]playerGSSnapshot, error)) func() {
	prev := getPlayerGSSnapshotFn
	getPlayerGSSnapshotFn = fn
	return func() { getPlayerGSSnapshotFn = prev }
}

// samePeriods compares two period slices as sets, since the baseline lookup and
// the day walk can visit a period in either order.
func samePeriods(got, want []DailyPeriod) bool {
	uniq := func(in []DailyPeriod) []DailyPeriod {
		seen := map[DailyPeriod]bool{}
		var out []DailyPeriod
		for _, p := range in {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	g, w := uniq(got), uniq(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

package fantrax

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// The three on-disk TTLs, one per durable-state mutability class. Keeping them
// as package constants (rather than per-Client fields) makes the tier the only
// place a TTL is named — see cacheTier. Documented in CLAUDE.md "Caching at a
// glance".
const (
	// todayTTL is the "today, but stable for a window" tier — data that drifts
	// during the day but is fine to reuse for an hourly loop or local iteration
	// (roster, FA pool, current period, pending trades).
	todayTTL = 15 * time.Minute
	// stableTTL is season-invariant config set at draft time (slots, scoring
	// weights, season range, period→date map).
	stableTTL = 7 * 24 * time.Hour
	// pastPeriodTTL is the lifetime for snapshots of past scoring periods —
	// immutable once the period closes (recent stats, period rosters, GS limits).
	pastPeriodTTL = 30 * 24 * time.Hour
)

// cacheTier names a durable-state mutability class. Making the tier a type
// (not a bare time.Duration threaded through 20 call sites) is what turns the
// three-tier scheme documented in CLAUDE.md into something the compiler and a
// reader can see: which tier a cached method gets is a named choice at its call
// site, and a fourth ad-hoc TTL has to be a deliberate departure. The
// provider-specific TTLs that legitimately sit outside the scheme
// (projections.ProjectionCacheTTL, statcast.CacheTTL, hkb 8h, prospect
// rankings, the 1h MLB game-log) live in other packages and are not tiers.
type cacheTier int

const (
	tierToday  cacheTier = iota // todayTTL — mutable "today" data
	tierStable                  // stableTTL — season-invariant config
	tierPast                    // pastPeriodTTL — immutable past-period snapshots
)

// duration resolves a tier to its concrete TTL.
func (t cacheTier) duration() time.Duration {
	switch t {
	case tierStable:
		return stableTTL
	case tierPast:
		return pastPeriodTTL
	default:
		return todayTTL
	}
}

// tierForPeriod picks the tier for a per-period snapshot. Past periods
// (period < current) are immutable and get tierPast; current/future periods
// still change and fall back to tierToday. The "current period" comparison
// uses PeriodForDate against today rather than GetCurrentPeriod (which would
// itself be cached and potentially circular). On a season-range fetch error it
// returns tierToday — a short TTL is always the safe pessimistic choice.
//
// This performs an upstream fetch (fetchSeasonDateRange), so cachedForPeriod
// only calls it after the caching-off short-circuit; never on the cacheDir==""
// (--no-cache / hermetic-test) path.
func (c *Client) tierForPeriod(period DailyPeriod) cacheTier {
	seasonStart, _, err := c.fetchSeasonDateRange()
	if err != nil {
		return tierToday
	}
	if period < PeriodForDate(seasonStart, time.Now().UTC()) {
		return tierPast
	}
	return tierToday
}

// cached is the single cache-plumbing seam for every method on *Client that
// reads a cacheable upstream value at a fixed tier. It owns the one policy the
// call sites used to each re-type by hand: short-circuit to a live fetch when
// caching is off (empty cacheDir — the --no-cache flag, and how tests stay
// hermetic), otherwise read-through a FileCache at the tier's TTL. Callers name
// only their key, their tier, and their fetch. Methods with a multi-value
// public signature (GetGSLimits, GetSeasonDateRange) run cached over a single
// struct value and unwrap it at the call site — the same shape the hand-written
// branches used.
func cached[T any](c *Client, key string, tier cacheTier, fetch func() (T, error)) (T, error) {
	if c.cacheDir == "" {
		return fetch()
	}
	return cache.New[T](c.cacheDir, tier.duration()).Get(key, fetch)
}

// cachedForPeriod is the per-period variant of cached. The tier isn't fixed —
// a past period is immutable (tierPast) while the current/future period still
// moves (tierToday) — so it's resolved from the period via tierForPeriod. That
// resolution needs the season range (an upstream fetch), so it runs only after
// the caching-off short-circuit, keeping the no-cache / hermetic path free of
// the extra round-trip that eager tier resolution would introduce.
func cachedForPeriod[T any](c *Client, key string, period DailyPeriod, fetch func() (T, error)) (T, error) {
	if c.cacheDir == "" {
		return fetch()
	}
	return cache.New[T](c.cacheDir, c.tierForPeriod(period).duration()).Get(key, fetch)
}

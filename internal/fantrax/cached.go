package fantrax

import (
	"strconv"
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
)

// PastPeriodTTL is the lifetime for snapshots of past scoring periods — recent
// stats, period rosters, pitcher-GS snapshots, GS limits. These are immutable
// once the period closes, so the TTL is effectively infinite: an entry written
// once is never re-fetched.
//
// It was 30 days until rosterbot-qoa. Because recap-site re-renders every
// completed week on every Monday build, a 30-day TTL meant each build
// re-fetched every week older than a month — cost O(season length), compounding
// weekly, and measurably so: wall time went 192s → 1411s between April and
// 2026-07-27, with 1,637 supposedly-immutable snapshot objects rewritten on
// 2026-07-20 alone.
//
// Exported because the same value is the `cacheTTL` argument callers hand to
// DailyFantasyPoints and GetTeamPitcherStarts; it used to be re-typed as a
// `30 * 24 * time.Hour` literal at six call sites, so changing "the" past-period
// TTL changed only a quarter of the caches it named.
//
// Two invariants make an unbounded TTL safe, and both must hold:
//
//   - Volatility. A period only reaches this tier once it can no longer change
//     — see periodIsVolatile / recentPeriodLookback, which hold the current
//     period and the last few closed ones on todayTTL to cover Fantrax's
//     YTD settling lag and UTC-vs-ET slop. Without that guard a snapshot
//     captured mid-settle is pinned wrong forever instead of self-healing at
//     the old 30-day expiry.
//   - Season scoping. Every key at this tier carries the season year (see
//     seasonScopedKey). Period numbers restart each season, so without it next
//     season's period 5 would read this season's frozen snapshot — a collision
//     the 30-day TTL used to make impossible for free.
const PastPeriodTTL = 100 * 365 * 24 * time.Hour

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
	tierPast                    // PastPeriodTTL — immutable past-period snapshots
)

// duration resolves a tier to its concrete TTL.
func (t cacheTier) duration() time.Duration {
	switch t {
	case tierStable:
		return stableTTL
	case tierPast:
		return PastPeriodTTL
	default:
		return todayTTL
	}
}

// tierFor picks the tier for one period's snapshot: tierPast once the period is
// settled, tierToday while it can still change.
//
// The volatility test is periodIsVolatile, not a bare `period < curPeriod`.
// That distinction did not matter at a 30-day TTL — a snapshot captured while
// Fantrax's YTD was still settling expired and self-corrected within the month.
// At PastPeriodTTL it is the whole safety argument: a period admitted to
// tierPast one day too early is wrong for the rest of the season.
func tierFor(period, curPeriod DailyPeriod) cacheTier {
	if periodIsVolatile(period, curPeriod) {
		return tierToday
	}
	return tierPast
}

// seasonScopedKey builds `<prefix>-<teamID>-<season>-<period>`, the key grammar
// for everything held at tierPast. Daily and weekly period numbers both restart
// each season, so the season part is what stops next year's period N from
// reading this year's entry now that the tier never expires. Matches the
// existing fantrax-period-date-map-<leagueID>-<year> precedent.
func seasonScopedKey(prefix, teamID string, season, period int) string {
	return cache.Key(prefix, teamID, strconv.Itoa(season), strconv.Itoa(period))
}

// periodCachePolicy resolves both halves of a per-period cache decision — the
// tier and the season the key is scoped to — from a single season-range read.
// On a fetch error it returns the pessimistic pair: the short tier (which
// self-heals) and the current calendar year.
//
// This performs a read that can reach upstream, so cachedForPeriod and
// cachedForSeason only call it after the caching-off short-circuit; never on
// the cacheDir=="" (--no-cache / hermetic-test) path. It reads the *cached*
// GetSeasonDateRange rather than the raw fetchSeasonDateRange, so repeated
// per-period calls cost a disk hit instead of a Fantrax round-trip.
func (c *Client) periodCachePolicy(period DailyPeriod) (cacheTier, int) {
	seasonStart, _, err := c.GetSeasonDateRange()
	if err != nil {
		return tierToday, time.Now().UTC().Year()
	}
	return tierFor(period, PeriodForDate(seasonStart, time.Now().UTC())), seasonStart.Year()
}

// seasonYear is periodCachePolicy's season half on its own, for keys held at a
// fixed tier (GS limits).
func (c *Client) seasonYear() int {
	seasonStart, _, err := c.GetSeasonDateRange()
	if err != nil {
		return time.Now().UTC().Year()
	}
	return seasonStart.Year()
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

// periodCacheKey is the single answer to "where does this per-period snapshot
// live, and how long does it keep?" — the one place a per-period key is built.
//
// It is a method, not two lines inlined at each site, because it was not:
// InvalidatePeriodRosterCache assembled its own key by hand, and when
// rosterbot-qoa spliced the season into this grammar the invalidator kept
// building the old one. Deleting a key that does not exist is not an error, so
// for four days it silently dropped nothing (rosterbot-sza). Anything that needs
// to name one of these keys must come through here.
//
// The caller is responsible for the caching-off short-circuit before calling:
// resolving the policy reads the season range, and the cacheDir=="" path
// (--no-cache, hermetic tests) must not trigger that round-trip.
func (c *Client) periodCacheKey(prefix, teamID string, period DailyPeriod) (string, cacheTier) {
	tier, season := c.periodCachePolicy(period)
	return seasonScopedKey(prefix, teamID, season, int(period)), tier
}

// cachedForPeriod is the per-period variant of cached. The tier isn't fixed —
// a settled period is immutable (tierPast) while the current and still-settling
// ones move (tierToday) — so both it and the key's season scope come from
// periodCacheKey. That resolution reads the season range, so it runs only after
// the caching-off short-circuit, keeping the no-cache / hermetic path free of
// the extra round-trip that eager resolution would introduce.
//
// It takes the key's prefix and teamID rather than a finished key because the
// season part has to be spliced in after that short-circuit.
func cachedForPeriod[T any](c *Client, prefix, teamID string, period DailyPeriod, fetch func() (T, error)) (T, error) {
	if c.cacheDir == "" {
		return fetch()
	}
	key, tier := c.periodCacheKey(prefix, teamID, period)
	return cache.New[T](c.cacheDir, tier.duration()).Get(key, fetch)
}

// cachedForSeason is cached() for a key whose tier is fixed but whose period
// number restarts each season (GS limits, keyed by the weekly period). Same
// laziness rule as cachedForPeriod: the season is resolved only once caching is
// known to be on.
func cachedForSeason[T any](c *Client, prefix, teamID string, period int, tier cacheTier, fetch func() (T, error)) (T, error) {
	if c.cacheDir == "" {
		return fetch()
	}
	key := seasonScopedKey(prefix, teamID, c.seasonYear(), period)
	return cache.New[T](c.cacheDir, tier.duration()).Get(key, fetch)
}

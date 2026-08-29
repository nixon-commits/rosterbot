package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// envelope wraps cached data with a timestamp for TTL checks.
type envelope[T any] struct {
	FetchedAt time.Time `json:"fetched_at"`
	Data      T         `json:"data"`
}

// Verbose controls whether cache hits and misses are logged to stderr.
// Off by default; set to true via --verbose.
var Verbose bool

// Notify, if set, surfaces the "fail through to cache" degraded path — a
// Pushover push in production. It lets callers report the event without
// coupling this leaf package to internal/notify or config. Nil by default; cmd
// wires it up at startup when Pushover creds are present.
//
// It is NOT called on every stale serve. Whether a degraded read is worth
// reporting is decided in stale.go, on the copy's AGE and on a durable dedup
// marker, because a level-triggered alert restates a standing outage once per
// key per run — 30 pushes a day during the 2026-08-19 FanGraphs Cloudflare
// block, of which the 30th said nothing the 1st had not.
var Notify func(title, message string)

// FileCache provides TTL-based file caching for any JSON-serializable type.
type FileCache[T any] struct {
	store Store
	ttl   time.Duration
}

// New creates a FileCache that stores entries under dir with the given TTL.
// A TTL of 0 means the cache is always bypassed (useful for --no-cache).
func New[T any](dir string, ttl time.Duration) *FileCache[T] {
	return &FileCache[T]{store: storeForDir(dir), ttl: ttl}
}

// NewWithStore creates a FileCache over an explicit Store, pinning it: the
// process-wide default installed by SetDefaultStore does not apply. This is
// the constructor tests reach for — TTL/envelope behavior against a MemStore,
// hermetic and t.Parallel-safe, where SetDefaultStore is process-global and
// cannot be. Before it existed, tests outside this package aged entries by
// hand-editing envelope JSON on disk, which spread the envelope layout to
// every test that needed it.
func NewWithStore[T any](store Store, ttl time.Duration) *FileCache[T] {
	return &FileCache[T]{store: store, ttl: ttl}
}

// GetOrFetch is the read-through policy every cached client method shares: an
// empty dir means caching is off for THIS CALLER (--no-cache, hermetic tests)
// and the fetch runs directly — deliberately even when SetDefaultStore has
// installed a process-wide store, because "this caller opted out" and "this
// process has an S3-backed cache" are different facts and the opt-out wins.
// Otherwise it reads through a FileCache at ttl.
//
// fantrax's cached()/cachedForPeriod()/cachedForSeason() and sleeper's call
// sites all come through here; the branch used to be restated per package
// (sleeper's copy carried a comment admitting it mirrored fantrax's).
func GetOrFetch[T any](dir, key string, ttl time.Duration, fetch func() (T, error)) (T, error) {
	if dir == "" {
		return fetch()
	}
	return New[T](dir, ttl).Get(key, fetch)
}

// SetFetchedAt rewrites the stored fetched_at for one entry through the Store
// seam, leaving the payload bytes untouched, and reports whether the entry
// existed. Test support (see cachetest): it is how a test ages or freshens an
// entry without learning the envelope layout — the field name and its JSON
// framing stay this package's alone.
func SetFetchedAt(s Store, key string, fetchedAt time.Time) (bool, error) {
	raw, found, err := s.Get(key)
	if err != nil || !found {
		return false, err
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return false, fmt.Errorf("corrupt cache entry %s: %w", key, err)
	}
	stamp, err := json.Marshal(fetchedAt)
	if err != nil {
		return false, err
	}
	env["fetched_at"] = stamp
	out, err := json.Marshal(env)
	if err != nil {
		return false, err
	}
	return true, s.Put(key, out)
}

// Get returns cached data if fresh, otherwise calls fetch, caches the result, and returns it.
// Cache I/O errors are non-fatal: they log to stderr and fall through to fetch.
func (c *FileCache[T]) Get(key string, fetch func() (T, error)) (T, error) {
	if c.ttl > 0 {
		if data, ok := c.load(key); ok {
			if Verbose {
				fmt.Fprintf(os.Stderr, "cache hit: %s\n", key)
			}
			return data, nil
		}
		if Verbose {
			fmt.Fprintf(os.Stderr, "cache miss: %s\n", key)
		}
	}
	data, err := fetch()
	if err != nil {
		return data, err
	}
	// The write obeys the same bypass as the read above. A zero TTL means every
	// Get refetches, so anything written here could never be read back — the
	// documented "TTL of 0 bypasses cache" was true of the read half only.
	if c.ttl > 0 {
		if err := c.save(key, data); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, err)
		}
	}
	return data, nil
}

// GetWithStaleFallback always attempts a fresh fetch regardless of TTL.
// On failure it serves any previously-cached value (ignoring expiry) if one
// exists, so a transient upstream outage never causes a hard error.
// Only errors if the fetch fails AND there is no cached file at all.
func (c *FileCache[T]) GetWithStaleFallback(key string, fetch func() (T, error)) (T, error) {
	data, err := fetch()
	if err == nil {
		reportRecovery(key)
		if saveErr := c.save(key, data); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, saveErr)
		}
		return data, nil
	}

	// Fresh fetch failed — serve any stale cached value.
	if stale, fetchedAt, ok := c.loadAnyAt(key); ok {
		age := time.Since(fetchedAt)
		fmt.Fprintf(os.Stderr, "⚠️ stale cache: %s (%s old) (%v)\n", key, roundAge(age), err)
		reportStale(key, fetchedAt, age, err)
		return stale, nil
	}

	return data, err
}

// loadAnyAt reads a cached entry ignoring TTL expiry, returning the timestamp
// it was fetched at alongside the data. The stale path needs the age, not just
// the bytes: whether a degraded read is worth reporting is a question about how
// old the copy is, and loadAny threw that away.
func (c *FileCache[T]) loadAnyAt(key string) (T, time.Time, bool) {
	var zero T
	raw, found, err := c.store.Get(key)
	if err != nil || !found {
		return zero, time.Time{}, false
	}
	var env envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, time.Time{}, false
	}
	return env.Data, env.FetchedAt, true
}

// Invalidate removes a single cached entry.
func (c *FileCache[T]) Invalidate(key string) error {
	return c.store.Remove(key)
}

// Key builds a cache key from parts joined by hyphens.
func Key(parts ...string) string {
	return strings.Join(parts, "-")
}

func (c *FileCache[T]) load(key string) (T, bool) {
	var zero T
	raw, found, err := c.store.Get(key)
	if err != nil || !found {
		return zero, false
	}
	var env envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintf(os.Stderr, "warning: corrupt cache entry %s: %v\n", key, err)
		return zero, false
	}
	if time.Since(env.FetchedAt) > c.ttl {
		return zero, false
	}
	return env.Data, true
}

func (c *FileCache[T]) save(key string, data T) error {
	env := envelope[T]{FetchedAt: time.Now(), Data: data}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return c.store.Put(key, b)
}

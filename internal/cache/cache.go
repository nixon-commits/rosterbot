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

// Notify, if set, is called when GetWithStaleFallback serves a stale cached
// value because the fresh fetch failed — i.e. the "fail through to cache"
// degraded path. It lets callers surface the event (e.g. a Pushover push)
// without coupling this leaf package to internal/notify or config. Nil by
// default; cmd wires it up at startup when Pushover creds are present.
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
	if err := c.save(key, data); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, err)
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
		if saveErr := c.save(key, data); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, saveErr)
		}
		return data, nil
	}

	// Fresh fetch failed — serve any stale cached value.
	if stale, ok := c.loadAny(key); ok {
		fmt.Fprintf(os.Stderr, "⚠️ stale cache: %s (%v)\n", key, err)
		if Notify != nil {
			Notify("⚠️ Stale cache", fmt.Sprintf("Serving stale %s", key))
		}
		return stale, nil
	}

	return data, err
}

// loadAny reads a cached entry ignoring TTL expiry.
func (c *FileCache[T]) loadAny(key string) (T, bool) {
	var zero T
	raw, found, err := c.store.Get(key)
	if err != nil || !found {
		return zero, false
	}
	var env envelope[T]
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, false
	}
	return env.Data, true
}

// Invalidate removes a single cached entry.
func (c *FileCache[T]) Invalidate(key string) error {
	return c.store.Remove(key)
}

// InvalidateAll removes the entire cache directory.
func InvalidateAll(dir string) error {
	err := os.RemoveAll(dir)
	if os.IsNotExist(err) {
		return nil
	}
	return err
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

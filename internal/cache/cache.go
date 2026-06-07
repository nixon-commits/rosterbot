package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	dir string
	ttl time.Duration
}

// New creates a FileCache that stores entries under dir with the given TTL.
// A TTL of 0 means the cache is always bypassed (useful for --no-cache).
func New[T any](dir string, ttl time.Duration) *FileCache[T] {
	return &FileCache[T]{dir: dir, ttl: ttl}
}

// Get returns cached data if fresh, otherwise calls fetch, caches the result, and returns it.
// Cache I/O errors are non-fatal: they log to stderr and fall through to fetch.
func (c *FileCache[T]) Get(key string, fetch func() (T, error)) (T, error) {
	path := c.path(key)

	// Try loading from cache (skip if TTL is 0).
	if c.ttl > 0 {
		if data, ok := c.load(path); ok {
			if Verbose {
				fmt.Fprintf(os.Stderr, "cache hit: %s\n", key)
			}
			return data, nil
		}
		if Verbose {
			fmt.Fprintf(os.Stderr, "cache miss: %s (path=%s)\n", key, path)
		}
	}

	// Cache miss or expired — fetch fresh data.
	data, err := fetch()
	if err != nil {
		return data, err
	}

	// Save to cache (non-fatal on failure).
	if err := c.save(path, data); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, err)
	}

	return data, nil
}

// GetWithStaleFallback always attempts a fresh fetch regardless of TTL.
// On failure it serves any previously-cached value (ignoring expiry) if one
// exists, so a transient upstream outage never causes a hard error.
// Only errors if the fetch fails AND there is no cached file at all.
func (c *FileCache[T]) GetWithStaleFallback(key string, fetch func() (T, error)) (T, error) {
	path := c.path(key)

	data, err := fetch()
	if err == nil {
		if saveErr := c.save(path, data); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save cache %s: %v\n", key, saveErr)
		}
		return data, nil
	}

	// Fresh fetch failed — serve any stale cached value.
	if stale, ok := c.loadAny(path); ok {
		fmt.Fprintf(os.Stderr, "⚠️ stale cache: %s (%v)\n", key, err)
		if Notify != nil {
			Notify("⚠️ Stale cache", fmt.Sprintf("Serving stale %s", key))
		}
		return stale, nil
	}

	return data, err
}

// loadAny reads a cached file ignoring TTL expiry.
func (c *FileCache[T]) loadAny(path string) (T, bool) {
	var zero T
	raw, err := os.ReadFile(path)
	if err != nil {
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
	err := os.Remove(c.path(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
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

func (c *FileCache[T]) path(key string) string {
	return filepath.Join(c.dir, key+".json")
}

func (c *FileCache[T]) load(path string) (T, bool) {
	var zero T

	data, err := os.ReadFile(path)
	if err != nil {
		return zero, false
	}

	var env envelope[T]
	if err := json.Unmarshal(data, &env); err != nil {
		fmt.Fprintf(os.Stderr, "warning: corrupt cache file %s: %v\n", path, err)
		return zero, false
	}

	if time.Since(env.FetchedAt) > c.ttl {
		return zero, false
	}

	return env.Data, true
}

func (c *FileCache[T]) save(path string, data T) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}

	env := envelope[T]{
		FetchedAt: time.Now(),
		Data:      data,
	}

	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0o644)
}

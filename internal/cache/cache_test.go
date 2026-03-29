package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGet_CacheMiss(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	called := false
	val, err := c.Get("test", func() (string, error) {
		called = true
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected fetch to be called on cache miss")
	}
	if val != "hello" {
		t.Fatalf("got %q, want %q", val, "hello")
	}

	// Verify file was written.
	if _, err := os.Stat(filepath.Join(dir, "test.json")); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
}

func TestGet_CacheHit(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	// Populate cache.
	_, _ = c.Get("test", func() (string, error) { return "first", nil })

	// Second call should use cache, not fetch.
	called := false
	val, err := c.Get("test", func() (string, error) {
		called = true
		return "second", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("fetch should not be called on cache hit")
	}
	if val != "first" {
		t.Fatalf("got %q, want %q (cached value)", val, "first")
	}
}

func TestGet_Expired(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Nanosecond) // Effectively expired immediately.

	// Populate cache.
	_, _ = c.Get("test", func() (string, error) { return "old", nil })

	time.Sleep(time.Millisecond)

	called := false
	val, err := c.Get("test", func() (string, error) {
		called = true
		return "new", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected fetch after expiry")
	}
	if val != "new" {
		t.Fatalf("got %q, want %q", val, "new")
	}
}

func TestGet_ZeroTTLBypassesCache(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, 0)

	// First call — fetches and saves.
	_, _ = c.Get("test", func() (string, error) { return "first", nil })

	// Second call with TTL=0 — should always fetch.
	called := false
	val, err := c.Get("test", func() (string, error) {
		called = true
		return "second", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("TTL=0 should always bypass cache")
	}
	if val != "second" {
		t.Fatalf("got %q, want %q", val, "second")
	}
}

func TestGet_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	// Write corrupt data.
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "test.json"), []byte("not json"), 0o644)

	called := false
	val, err := c.Get("test", func() (string, error) {
		called = true
		return "recovered", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected fetch after corrupt cache")
	}
	if val != "recovered" {
		t.Fatalf("got %q, want %q", val, "recovered")
	}
}

func TestGet_FetchError(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	_, err := c.Get("test", func() (string, error) {
		return "", errors.New("network down")
	})
	if err == nil {
		t.Fatal("expected error from failed fetch")
	}
}

func TestInvalidate(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	_, _ = c.Get("test", func() (string, error) { return "data", nil })

	if err := c.Invalidate("test"); err != nil {
		t.Fatalf("invalidate error: %v", err)
	}

	// Should be a miss now.
	called := false
	_, _ = c.Get("test", func() (string, error) {
		called = true
		return "fresh", nil
	})
	if !called {
		t.Fatal("expected fetch after invalidation")
	}
}

func TestInvalidate_NonExistent(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	if err := c.Invalidate("nonexistent"); err != nil {
		t.Fatalf("invalidate non-existent should not error: %v", err)
	}
}

func TestInvalidateAll(t *testing.T) {
	dir := t.TempDir()
	c := New[string](dir, time.Hour)

	_, _ = c.Get("a", func() (string, error) { return "1", nil })
	_, _ = c.Get("b", func() (string, error) { return "2", nil })

	if err := InvalidateAll(dir); err != nil {
		t.Fatalf("invalidate all error: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected cache directory to be removed")
	}
}

func TestKey(t *testing.T) {
	got := Key("fangraphs", "bat", "steamerr")
	want := "fangraphs-bat-steamerr"
	if got != want {
		t.Fatalf("Key() = %q, want %q", got, want)
	}
}

func TestGet_StructData(t *testing.T) {
	type Player struct {
		Name string  `json:"name"`
		Pts  float64 `json:"pts"`
	}

	dir := t.TempDir()
	c := New[[]Player](dir, time.Hour)

	players := []Player{{Name: "Judge", Pts: 5.5}, {Name: "Ohtani", Pts: 6.2}}
	_, _ = c.Get("players", func() ([]Player, error) { return players, nil })

	// Read from cache.
	called := false
	cached, err := c.Get("players", func() ([]Player, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("should use cache")
	}
	if len(cached) != 2 || cached[0].Name != "Judge" || cached[1].Pts != 6.2 {
		t.Fatalf("cached data mismatch: %+v", cached)
	}
}

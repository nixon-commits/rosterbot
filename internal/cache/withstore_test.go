package cache_test

// External-package tests for the injectable-store surface: NewWithStore,
// GetOrFetch and SetFetchedAt. They live outside package cache deliberately —
// the point of NewWithStore is that the interface alone, with no whitebox
// struct literal, is enough to drive TTL/envelope behavior hermetically.

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

func TestNewWithStore_TTLBehaviorAgainstMemStore(t *testing.T) {
	t.Parallel()
	st := cache.NewMemStore()
	c := cache.NewWithStore[string](st, time.Hour)

	fetches := 0
	fetch := func() (string, error) { fetches++; return "v", nil }

	if got, err := c.Get("k", fetch); err != nil || got != "v" {
		t.Fatalf("Get = (%q, %v)", got, err)
	}
	if got, err := c.Get("k", fetch); err != nil || got != "v" {
		t.Fatalf("second Get = (%q, %v)", got, err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1 — the second read must hit the store", fetches)
	}

	// Age the entry past the TTL through the seam; the next read must refetch.
	if ok, err := cache.SetFetchedAt(st, "k", time.Now().Add(-2*time.Hour)); err != nil || !ok {
		t.Fatalf("SetFetchedAt = (%v, %v)", ok, err)
	}
	if _, err := c.Get("k", fetch); err != nil {
		t.Fatalf("Get after aging: %v", err)
	}
	if fetches != 2 {
		t.Fatalf("fetches = %d, want 2 — an aged entry must miss", fetches)
	}
}

func TestNewWithStore_IgnoresTheProcessDefaultStore(t *testing.T) {
	// Not parallel: SetDefaultStore is the process-global this constructor
	// exists to escape, and this test must own it for its duration.
	defaultStore := cache.NewMemStore()
	cache.SetDefaultStore(defaultStore)
	t.Cleanup(func() { cache.SetDefaultStore(nil) })

	pinned := cache.NewMemStore()
	c := cache.NewWithStore[int](pinned, time.Hour)
	if _, err := c.Get("k", func() (int, error) { return 7, nil }); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := pinned.Get("k"); !found {
		t.Fatal("entry not written to the pinned store")
	}
	if _, found, _ := defaultStore.Get("k"); found {
		t.Fatal("entry leaked to the process default store — NewWithStore must pin")
	}
}

func TestGetOrFetch_EmptyDirBypassesCaching(t *testing.T) {
	fetches := 0
	fetch := func() (string, error) { fetches++; return "live", nil }

	for i := 0; i < 2; i++ {
		if got, err := cache.GetOrFetch("", "k", time.Hour, fetch); err != nil || got != "live" {
			t.Fatalf("GetOrFetch = (%q, %v)", got, err)
		}
	}
	if fetches != 2 {
		t.Fatalf("fetches = %d, want 2 — an empty dir means no caching for this caller", fetches)
	}
}

func TestGetOrFetch_ReadsThroughWhenDirSet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fetches := 0
	fetch := func() (string, error) { fetches++; return "v", nil }

	for i := 0; i < 2; i++ {
		if got, err := cache.GetOrFetch(dir, "k", time.Hour, fetch); err != nil || got != "v" {
			t.Fatalf("GetOrFetch = (%q, %v)", got, err)
		}
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1 — the second call must be served from disk", fetches)
	}
}

func TestSetFetchedAt_AbsentKeyReportsFalse(t *testing.T) {
	t.Parallel()
	ok, err := cache.SetFetchedAt(cache.NewMemStore(), "nope", time.Now())
	if err != nil || ok {
		t.Fatalf("SetFetchedAt on absent key = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestSetFetchedAt_CorruptEntryErrors(t *testing.T) {
	t.Parallel()
	st := cache.NewMemStore()
	if err := st.Put("k", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.SetFetchedAt(st, "k", time.Now()); err == nil {
		t.Fatal("corrupt entry silently rewritten")
	}
}

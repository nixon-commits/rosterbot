// Package cachetest is test support for packages that exercise a real on-disk
// cache directory. It owns the two facts such a test would otherwise restate
// for itself: the filesystem layout (<dir>/<key>.json, via cache.NewFileStore)
// and the envelope's timestamp field (via cache.SetFetchedAt). Before it
// existed, internal/fantrax's TTL tests aged entries by reading each file,
// hand-editing the fetched_at JSON, and writing it back — envelope knowledge
// copied into a package that shouldn't hold it.
package cachetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
)

// AgeEntries rewrites every cache entry under dir so it reads as fetched `age`
// ago — simulating a later run without sleeping or faking the clock — and
// returns how many entries it aged. Callers asserting that aging mattered
// should check the count is non-zero: aging an empty directory "succeeds"
// while proving nothing.
func AgeEntries(tb testing.TB, dir string, age time.Duration) int {
	tb.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("read cache dir: %v", err)
	}
	store := cache.NewFileStore(dir)
	stamp := time.Now().Add(-age)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".json")
		ok, err := cache.SetFetchedAt(store, key, stamp)
		if err != nil {
			tb.Fatalf("age %s: %v", filepath.Join(dir, e.Name()), err)
		}
		if ok {
			n++
		}
	}
	return n
}

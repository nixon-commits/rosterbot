package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFSStore_RoundTripAndSuffix(t *testing.T) {
	dir := t.TempDir()
	s := fsStore{root: dir}

	if _, found, err := s.Get("missing"); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v, want false,nil", found, err)
	}
	if err := s.Put("k", []byte("hi")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, found, err := s.Get("k")
	if err != nil || !found || string(got) != "hi" {
		t.Fatalf("get: %q found=%v err=%v", got, found, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "k.json")); err != nil {
		t.Fatalf("expected k.json on disk: %v", err)
	}
	if err := s.Remove("k"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := s.Remove("k"); err != nil {
		t.Fatalf("remove missing should be nil: %v", err)
	}
}

func TestSetDefaultStore_RoutesAllCaches(t *testing.T) {
	mem := NewMemStore()
	SetDefaultStore(mem)
	t.Cleanup(func() { SetDefaultStore(nil) })

	// dir is ignored when a default store is set.
	c := New[string]("/nonexistent-dir", time.Hour)
	if _, err := c.Get("k", func() (string, error) { return "v", nil }); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, found, _ := mem.Get("k"); !found {
		t.Fatal("expected value written to the default MemStore, not the filesystem")
	}

	// Cache hit comes back from the store without calling fetch.
	got, err := c.Get("k", func() (string, error) { return "SHOULD-NOT-RUN", nil })
	if err != nil || got != "v" {
		t.Fatalf("hit: got %q err %v", got, err)
	}
}

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

// rosterbot-9uqx: a FileCache with no directory has nowhere to store anything,
// so every write failed and Get printed a warning per key. On a --no-cache
// optimize run that is eleven lines of "failed to" on stderr describing nothing
// wrong, in the one command every repro instruction in this repo uses.
//
// An empty dir is a statement that there is no storage, not a broken path.
func TestStoreForDir_EmptyDirIsInert(t *testing.T) {
	SetDefaultStore(nil)
	s := storeForDir("")

	if err := s.Put("k", []byte("hi")); err != nil {
		t.Errorf("Put: %v, want nil — no directory means nothing to write, not a failure", err)
	}
	data, found, err := s.Get("k")
	if err != nil || found || data != nil {
		t.Errorf("Get = (%v, %v, %v), want (nil, false, nil)", data, found, err)
	}
	if err := s.Remove("k"); err != nil {
		t.Errorf("Remove: %v, want nil", err)
	}
}

// The S3 path must be untouched: SetDefaultStore backs every FileCache
// regardless of the dir passed to New, so an empty dir under a default store is
// a perfectly ordinary AWS cache write and must NOT become inert. This ordering
// is the whole reason the guard can live in the store rather than in the TTL.
func TestStoreForDir_DefaultStoreWinsOverAnEmptyDir(t *testing.T) {
	mem := NewMemStore()
	SetDefaultStore(mem)
	t.Cleanup(func() { SetDefaultStore(nil) })

	if err := storeForDir("").Put("k", []byte("hi")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, found, _ := mem.Get("k"); !found {
		t.Error("an empty dir under a default store must still reach that store")
	}
}

// End to end through the layer that printed the warning.
func TestFileCache_EmptyDirFetchesWithoutWarning(t *testing.T) {
	SetDefaultStore(nil)
	c := New[string]("", time.Hour)

	stderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	got, err := c.Get("mlb-player-ids-0b581697", func() (string, error) { return "fresh", nil })
	w.Close()
	os.Stderr = stderr

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	if noise := string(buf[:n]); noise != "" {
		t.Errorf("stderr = %q, want silence", noise)
	}
	if err != nil || got != "fresh" {
		t.Errorf("Get = (%q, %v), want (\"fresh\", nil) — the fetch itself must still work", got, err)
	}
}

// The documented contract is "TTL of 0 bypasses cache". That was true of the
// read and not of the write, so a zero-TTL cache over a real directory wrote
// files nothing would ever read back.
func TestFileCache_ZeroTTLWritesNothing(t *testing.T) {
	SetDefaultStore(nil)
	dir := t.TempDir()
	c := New[string](dir, 0)

	if _, err := c.Get("k", func() (string, error) { return "fresh", nil }); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("wrote %d file(s) at TTL 0, want none — a zero TTL never reads them back", len(entries))
	}
}

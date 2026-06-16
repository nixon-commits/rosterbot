package cache

import (
	"os"
	"path/filepath"
	"testing"
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

package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileProgressStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileProgressStore(dir)
	ctx := context.Background()

	if _, ok, err := s.GetProgress(ctx, "r1"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	want := []byte(`{"phase":"Roster","pct":10}`)
	if err := s.PutProgress(ctx, "r1", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetProgress(ctx, "r1")
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("get: got=%q ok=%v err=%v", got, ok, err)
	}
}

// TestFileProgressStore_ReadTraversalEscape plants a sentinel file at the
// traversal escape target (base/secret.json, sibling of the store dir) and
// asserts GetProgress does not return it. Without the safeRunID guard,
// path("../secret") = filepath.Join(base/store, "../secret.json") =
// base/secret.json, so an unguarded read would return the sentinel bytes —
// this is the discriminating assertion (data == nil, not merely err == nil).
func TestFileProgressStore_ReadTraversalEscape(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	sentinelPath := filepath.Join(base, "secret.json")
	if err := os.WriteFile(sentinelPath, []byte("SENTINEL"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	s := NewFileProgressStore(storeDir)
	ctx := context.Background()

	data, ok, err := s.GetProgress(ctx, "../secret")
	if err != nil {
		t.Fatalf("GetProgress(%q): unexpected error %v", "../secret", err)
	}
	if ok || data != nil {
		t.Fatalf("GetProgress(%q) = %q, ok=%v; want nil, false — guard should have blocked escape to %s", "../secret", data, ok, sentinelPath)
	}
}

// TestFileProgressStore_WriteTraversalEscape targets an escape directory
// (base) that already exists, so an unguarded write would succeed with no
// coincidental ENOENT to mask the missing guard. Without safeRunID,
// path("../evil") = filepath.Join(base/store, "../evil.json") = base/evil.json,
// and base exists — so the write would land. Both assertions matter: a
// non-nil error AND the escape target's absence, which is what actually
// proves nothing escaped.
func TestFileProgressStore_WriteTraversalEscape(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}

	s := NewFileProgressStore(storeDir)
	ctx := context.Background()

	escapeTarget := filepath.Join(base, "evil.json")
	if err := s.PutProgress(ctx, "../evil", []byte("data")); err == nil {
		t.Fatalf("PutProgress(%q) = nil error, want non-nil — guard should have blocked escape to %s", "../evil", escapeTarget)
	}

	if _, err := os.Stat(escapeTarget); !os.IsNotExist(err) {
		t.Fatalf("PutProgress(%q) wrote escape target %s (stat err=%v); traversal write succeeded", "../evil", escapeTarget, err)
	}
}

func TestFileProgressStore_NormalIDStillRoundTrips(t *testing.T) {
	dir := t.TempDir()
	s := NewFileProgressStore(dir)
	ctx := context.Background()

	want := []byte(`{"phase":"Done","pct":100}`)
	if err := s.PutProgress(ctx, "run123", want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetProgress(ctx, "run123")
	if err != nil || !ok || string(got) != string(want) {
		t.Fatalf("get: got=%q ok=%v err=%v", got, ok, err)
	}
}

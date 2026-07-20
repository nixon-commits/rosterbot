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

func TestFileProgressStore_PathTraversal(t *testing.T) {
	traversalIDs := []string{"../evil", "..", "a/b", "a\\b", "../../etc/foo"}

	for _, id := range traversalIDs {
		t.Run("get_"+id, func(t *testing.T) {
			dir := t.TempDir()
			s := NewFileProgressStore(dir)
			ctx := context.Background()

			data, ok, err := s.GetProgress(ctx, id)
			if err != nil || ok || data != nil {
				t.Fatalf("GetProgress(%q) = %v, %v, %v; want nil, false, nil", id, data, ok, err)
			}
		})

		t.Run("put_"+id, func(t *testing.T) {
			// Use a parent directory whose sibling we can inspect, so a traversal
			// id like "../evil" would (if unguarded) write outside dir.
			parent := t.TempDir()
			dir := filepath.Join(parent, "store")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			s := NewFileProgressStore(dir)
			ctx := context.Background()

			if err := s.PutProgress(ctx, id, []byte("data")); err == nil {
				t.Fatalf("PutProgress(%q) = nil error, want non-nil", id)
			}

			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("readdir parent: %v", err)
			}
			for _, e := range entries {
				if e.Name() != "store" {
					t.Fatalf("PutProgress(%q) wrote stray entry %q outside dir", id, e.Name())
				}
			}
		})
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

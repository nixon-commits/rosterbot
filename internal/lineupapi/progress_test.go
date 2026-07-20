package lineupapi

import (
	"context"
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

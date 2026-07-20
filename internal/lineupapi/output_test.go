package lineupapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMarshalOutputEnvelope(t *testing.T) {
	data := WaiversResult{
		Picks: []WaiverPickOut{{
			Name: "Jane Doe", Team: "BAL", Pos: "OF", Signal: "HOT",
			ProjectedFPG: 4.2, Rank: 1,
		}},
		Total: 1,
	}
	b, err := MarshalOutput("waivers", data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != "waivers" {
		t.Fatalf("type = %q, want waivers", env.Type)
	}
	var got WaiversResult
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(got.Picks) != 1 || got.Picks[0].Name != "Jane Doe" || got.Picks[0].Signal != "HOT" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestFileOutputStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileOutputStore(dir)
	if _, ok, _ := s.GetOutput(context.Background(), "run-1"); ok {
		t.Fatal("expected miss before write")
	}
	body, _ := MarshalOutput("gs-check", GSCheckResult{Violations: []GSViolationOut{{Team: "X", Kind: "over", Used: 6, Limit: 5, OverBy: 1}}})
	if err := s.PutOutput(context.Background(), "run-1", body); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetOutput(context.Background(), "run-1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != string(body) {
		t.Fatalf("bytes mismatch")
	}
}

func TestFileOutputStore_PathTraversal(t *testing.T) {
	traversalIDs := []string{"../evil", "..", "a/b", "a\\b", "../../etc/foo"}

	for _, id := range traversalIDs {
		t.Run("get_"+id, func(t *testing.T) {
			dir := t.TempDir()
			s := NewFileOutputStore(dir)
			ctx := context.Background()

			data, ok, err := s.GetOutput(ctx, id)
			if err != nil || ok || data != nil {
				t.Fatalf("GetOutput(%q) = %v, %v, %v; want nil, false, nil", id, data, ok, err)
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
			s := NewFileOutputStore(dir)
			ctx := context.Background()

			if err := s.PutOutput(ctx, id, []byte("data")); err == nil {
				t.Fatalf("PutOutput(%q) = nil error, want non-nil", id)
			}

			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("readdir parent: %v", err)
			}
			for _, e := range entries {
				if e.Name() != "store" {
					t.Fatalf("PutOutput(%q) wrote stray entry %q outside dir", id, e.Name())
				}
			}
		})
	}
}

func TestFileOutputStore_NormalIDStillRoundTrips(t *testing.T) {
	dir := t.TempDir()
	s := NewFileOutputStore(dir)
	ctx := context.Background()

	body, _ := MarshalOutput("waivers", WaiversResult{Total: 0})
	if err := s.PutOutput(ctx, "run123", body); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := s.GetOutput(ctx, "run123")
	if err != nil || !ok || string(got) != string(body) {
		t.Fatalf("get: got=%q ok=%v err=%v", got, ok, err)
	}
}

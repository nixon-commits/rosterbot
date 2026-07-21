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

// TestFileOutputStore_ReadTraversalEscape plants a sentinel file at the
// traversal escape target (base/secret.json, sibling of the store dir) and
// asserts GetOutput does not return it. Without the safeRunID guard,
// path("../secret") = filepath.Join(base/store, "../secret.json") =
// base/secret.json, so an unguarded read would return the sentinel bytes —
// this is the discriminating assertion (data == nil, not merely err == nil).
func TestFileOutputStore_ReadTraversalEscape(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	sentinelPath := filepath.Join(base, "secret.json")
	if err := os.WriteFile(sentinelPath, []byte("SENTINEL"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	s := NewFileOutputStore(storeDir)
	ctx := context.Background()

	data, ok, err := s.GetOutput(ctx, "../secret")
	if err != nil {
		t.Fatalf("GetOutput(%q): unexpected error %v", "../secret", err)
	}
	if ok || data != nil {
		t.Fatalf("GetOutput(%q) = %q, ok=%v; want nil, false — guard should have blocked escape to %s", "../secret", data, ok, sentinelPath)
	}
}

// TestFileOutputStore_WriteTraversalEscape targets an escape directory
// (base) that already exists, so an unguarded write would succeed with no
// coincidental ENOENT to mask the missing guard. Without safeRunID,
// path("../evil") = filepath.Join(base/store, "../evil.json") = base/evil.json,
// and base exists — so the write would land. Both assertions matter: a
// non-nil error AND the escape target's absence, which is what actually
// proves nothing escaped.
func TestFileOutputStore_WriteTraversalEscape(t *testing.T) {
	base := t.TempDir()
	storeDir := filepath.Join(base, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}

	s := NewFileOutputStore(storeDir)
	ctx := context.Background()

	escapeTarget := filepath.Join(base, "evil.json")
	if err := s.PutOutput(ctx, "../evil", []byte("data")); err == nil {
		t.Fatalf("PutOutput(%q) = nil error, want non-nil — guard should have blocked escape to %s", "../evil", escapeTarget)
	}

	if _, err := os.Stat(escapeTarget); !os.IsNotExist(err) {
		t.Fatalf("PutOutput(%q) wrote escape target %s (stat err=%v); traversal write succeeded", "../evil", escapeTarget, err)
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

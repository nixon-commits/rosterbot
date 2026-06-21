package lineupapi

import (
	"context"
	"encoding/json"
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

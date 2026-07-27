package s3lineup

import (
	"context"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestLineupPublishAndGet(t *testing.T) {
	f := s3blobtest.New()
	s := &Store{blob: f.Blob("b", "lineup/")}
	ctx := context.Background()

	if _, ok, err := s.Get(ctx, "today"); err != nil || ok {
		t.Fatalf("Get on empty store: ok=%v err=%v, want false/nil", ok, err)
	}
	if err := s.Publish("today", []byte(`{"date":"2026-07-27"}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, stored := f.Objects["lineup/today.json"]; !stored {
		t.Fatalf("object not stored at lineup/today.json; got keys %v", f.Keys())
	}
	got, ok, err := s.Get(ctx, "today")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if string(got) != `{"date":"2026-07-27"}` {
		t.Fatalf("bytes mismatch: %s", got)
	}
}

func TestRunsStorePutRunKeysByInvertedTimestamp(t *testing.T) {
	f := s3blobtest.New()
	s := &RunsStore{blob: f.Blob("b", "runledger/")}
	ctx := context.Background()

	rec := lineupapi.RunDetail{Run: lineupapi.Run{
		ID:        "abc",
		Command:   "optimize",
		Status:    "SUCCESS",
		StartedAt: "2026-07-27T13:30:00Z",
		Trigger:   "schedule",
	}}
	if err := s.PutRun(ctx, rec); err != nil {
		t.Fatalf("PutRun: %v", err)
	}
	got, ok, err := s.Get(ctx, "abc")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.ID != "abc" {
		t.Errorf("ID = %q, want abc", got.ID)
	}
	if _, found, _ := (&RunsStore{blob: f.Blob("b", "runledger/")}).Get(ctx, "not-a-run"); found {
		t.Error("want found=false for an unknown run id")
	}
}

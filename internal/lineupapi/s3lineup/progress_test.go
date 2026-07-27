package s3lineup

import (
	"context"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestProgressStoreRoundTrip(t *testing.T) {
	f := s3blobtest.New()
	s := &ProgressStore{blob: f.Blob("b", "runs/")}

	if _, ok, _ := s.GetProgress(context.Background(), "abc"); ok {
		t.Fatal("expected miss")
	}
	if err := s.PutProgress(context.Background(), "abc", []byte(`{"stage":"optimizing","pct":42}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, stored := f.Objects["runs/abc/progress.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", f.Keys())
	}
	got, ok, err := s.GetProgress(context.Background(), "abc")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != `{"stage":"optimizing","pct":42}` {
		t.Fatalf("bytes mismatch: %s", got)
	}
}

func TestProgressStoreObjKey(t *testing.T) {
	f := s3blobtest.New()
	key := f.Blob("b", "runs/").Key(progressKey("abc"))
	if key != "runs/abc/progress.json" {
		t.Fatalf("key = %q, want runs/abc/progress.json", key)
	}
	if !strings.HasSuffix(key, "/progress.json") {
		t.Fatalf("key %q does not end in /progress.json", key)
	}
}

func TestProgressStoreGetMissingReturnsNoErrorNoData(t *testing.T) {
	s := &ProgressStore{blob: s3blobtest.New().Blob("b", "runs/")}

	data, ok, err := s.GetProgress(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("expected nil error on NoSuchKey, got %v", err)
	}
	if ok {
		t.Fatal("expected ok=false on missing key")
	}
	if data != nil {
		t.Fatalf("expected nil data on miss, got %v", data)
	}
}

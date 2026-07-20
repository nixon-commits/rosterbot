package s3lineup

import (
	"context"
	"strings"
	"testing"
)

func TestProgressStoreRoundTrip(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &ProgressStore{client: f, bucket: "b", prefix: "runs/"}

	if _, ok, _ := s.GetProgress(context.Background(), "abc"); ok {
		t.Fatal("expected miss")
	}
	if err := s.PutProgress(context.Background(), "abc", []byte(`{"stage":"optimizing","pct":42}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, stored := f.objects["runs/abc/progress.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", keys(f.objects))
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
	s := &ProgressStore{prefix: "runs/"}
	key := s.objKey("abc")
	if key != "runs/abc/progress.json" {
		t.Fatalf("objKey = %q, want runs/abc/progress.json", key)
	}
	if !strings.HasSuffix(key, "/progress.json") {
		t.Fatalf("objKey %q does not end in /progress.json", key)
	}
}

func TestProgressStoreGetMissingReturnsNoErrorNoData(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &ProgressStore{client: f, bucket: "b", prefix: "runs/"}

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

package s3store

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestS3Store_KeyPrefixAndNotFound(t *testing.T) {
	f := s3blobtest.New()
	s := &Store{blob: f.Blob("b", "cache/")}

	if _, found, err := s.Get("fangraphs-bat"); err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
	if err := s.Put("fangraphs-bat", []byte("xyz")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok := f.Objects["cache/fangraphs-bat.json"]; !ok {
		t.Fatalf("object not stored under cache/fangraphs-bat.json: keys=%v", f.Keys())
	}
	got, found, err := s.Get("fangraphs-bat")
	if err != nil || !found || string(got) != "xyz" {
		t.Fatalf("get: %q found=%v err=%v", got, found, err)
	}
	if err := s.Remove("fangraphs-bat"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, found, _ := s.Get("fangraphs-bat"); found {
		t.Fatal("expected removed")
	}
}

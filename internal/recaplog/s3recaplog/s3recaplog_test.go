package s3recaplog

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func newStore(f *s3blobtest.Fake) *Store { return &Store{blob: f.Blob("logbkt", "recap/")} }

func TestListReturnsPrefixRelativeKeys(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"recap/E19.2026-07-27-20.aaa.gz": []byte("x"),
		"recap/E19.2026-07-27-19.bbb.gz": []byte("y"),
		"other/ignore.gz":                []byte("z"),
	})
	keys, err := newStore(f).List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 keys scoped to the recap/ prefix, got %d: %v", len(keys), keys)
	}
	for _, k := range keys {
		if strings.HasPrefix(k, "recap/") {
			t.Errorf("key %q still carries the bucket prefix; keys must be prefix-relative", k)
		}
	}
}

func TestGetJoinsThePrefix(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{"recap/E19.2026-07-27-20.aaa.gz": []byte("payload")})
	got, err := newStore(f).Get("E19.2026-07-27-20.aaa.gz")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("Get = %q, want payload", got)
	}
}

func TestGetMissingObjectIsAnError(t *testing.T) {
	_, err := newStore(s3blobtest.New()).Get("E19.2026-07-27-20.aaa.gz")
	if err == nil {
		t.Fatal("want an error for a missing object")
	}
	if !strings.Contains(err.Error(), "recap/E19.2026-07-27-20.aaa.gz") {
		t.Errorf("error %q should name the full object key", err)
	}
}

func TestListPaginates(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 2
	for h := 10; h < 20; h++ {
		f.Objects["recap/E19.2026-07-27-"+string(rune('0'+h/10))+string(rune('0'+h%10))+".aaa.gz"] = []byte("x")
	}
	keys, err := newStore(f).List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 10 {
		t.Fatalf("want all 10 keys across pages, got %d", len(keys))
	}
	if f.ListCalls < 2 {
		t.Errorf("expected the continuation loop to page, got %d list calls", f.ListCalls)
	}
}

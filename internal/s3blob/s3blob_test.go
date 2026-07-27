package s3blob_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/s3blob"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestGetPutDeleteJoinAndStripThePrefix(t *testing.T) {
	f := s3blobtest.New()
	b := f.Blob("bkt", "cache/")
	ctx := context.Background()

	if _, found, err := b.Get(ctx, "fangraphs-bat"); err != nil || found {
		t.Fatalf("Get on empty store: found=%v err=%v, want false/nil", found, err)
	}
	if err := b.Put(ctx, "fangraphs-bat", []byte("xyz")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := f.Objects["cache/fangraphs-bat"]; !ok {
		t.Fatalf("object not stored under the bucket prefix; keys=%v", f.Keys())
	}
	got, found, err := b.Get(ctx, "fangraphs-bat")
	if err != nil || !found || string(got) != "xyz" {
		t.Fatalf("Get: %q found=%v err=%v", got, found, err)
	}
	if err := b.Delete(ctx, "fangraphs-bat"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := b.Get(ctx, "fangraphs-bat"); found {
		t.Fatal("expected the object to be gone after Delete")
	}
}

// A missing object is an ordinary outcome, not an error — every adapter above
// this layer relies on that (cache miss, run with no output yet, no passkey).
func TestGetMissingIsNotAnError(t *testing.T) {
	b := s3blobtest.New().Blob("bkt", "runs/")
	data, found, err := b.Get(context.Background(), "nope/output.json")
	if err != nil || found || data != nil {
		t.Fatalf("got (%v, %v, %v), want (nil, false, nil)", data, found, err)
	}
}

func TestPutJSONTagsContentType(t *testing.T) {
	f := s3blobtest.New()
	b := f.Blob("bkt", "lineup/")
	ctx := context.Background()

	if err := b.PutJSON(ctx, "today.json", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}
	got, found, err := b.Get(ctx, "today.json")
	if err != nil || !found || string(got) != `{"a":1}` {
		t.Fatalf("round trip: %q found=%v err=%v", got, found, err)
	}
}

func TestDeleteMissingKeyIsNotAnError(t *testing.T) {
	b := s3blobtest.New().Blob("bkt", "cache/")
	if err := b.Delete(context.Background(), "never-written"); err != nil {
		t.Fatalf("Delete of a missing key: %v", err)
	}
}

func TestListReturnsPrefixRelativeKeys(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"analysis/grades/dt=2026-07-20/system=atc-ros/grades.ndjson": []byte("x"),
		"analysis/grades/dt=2026-07-19/grades.ndjson":                []byte("y"),
		"analysis/team-values/dt=2026-07-20/values.ndjson":           []byte("z"),
	})
	keys, err := f.Blob("bkt", "analysis/").List(context.Background(), "grades/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		"grades/dt=2026-07-19/grades.ndjson",
		"grades/dt=2026-07-20/system=atc-ros/grades.ndjson",
	}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("List = %v, want %v (relative to the Blob prefix, and scoped to the walk prefix)", keys, want)
	}
}

// Keys are reported relative to the Blob's prefix, not the walk prefix, so a
// bucket-root Blob (the infra lister, the ledger migration) sees whole keys.
func TestWalkOnABucketRootBlobReportsWholeKeys(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{"archive/hkb/dt=2026-07-20/ranks.json": []byte("x")})
	keys, err := f.Blob("bkt", "").List(context.Background(), "archive/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != "archive/hkb/dt=2026-07-20/ranks.json" {
		t.Errorf("List = %v, want the whole key", keys)
	}
}

func TestListPaginatesPastTheFirstPage(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 2
	for i := 0; i < 5; i++ {
		f.Objects[fmt.Sprintf("analysis/grades/dt=2026-07-%02d/grades.ndjson", i+1)] = []byte("x")
	}
	keys, err := f.Blob("bkt", "analysis/").List(context.Background(), "grades/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 5 {
		t.Fatalf("want all 5 keys across pages, got %d: %v", len(keys), keys)
	}
	if f.ListCalls < 3 {
		t.Errorf("expected the continuation loop to page, got %d list calls", f.ListCalls)
	}
}

// Stopping early must stop dead — not finish the page, and not fetch another.
func TestWalkStopsWhenTheCallbackReturnsFalse(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 2
	for i := 0; i < 10; i++ {
		f.Objects[fmt.Sprintf("runledger/%02d.json", i)] = []byte("x")
	}

	var seen []string
	err := f.Blob("bkt", "runledger/").Walk(context.Background(), "", func(o s3blob.Object) bool {
		seen = append(seen, o.Key)
		return len(seen) < 3
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("want exactly 3 objects before the stop, got %d: %v", len(seen), seen)
	}
	if f.ListCalls != 2 {
		t.Errorf("want 2 list calls (stop lands mid-second-page), got %d", f.ListCalls)
	}
}

func TestWalkReportsSizeAndLastModified(t *testing.T) {
	ts := time.Date(2026, 7, 20, 15, 4, 5, 0, time.UTC)
	f := s3blobtest.With(map[string][]byte{"archive/hkb/dt=2026-07-20/ranks.json": []byte("hello")})
	f.Modified["archive/hkb/dt=2026-07-20/ranks.json"] = ts

	var got s3blob.Object
	if err := f.Blob("bkt", "").Walk(context.Background(), "archive/", func(o s3blob.Object) bool {
		got = o
		return true
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got.Size != 5 {
		t.Errorf("Size = %d, want 5", got.Size)
	}
	if !got.LastModified.Equal(ts) {
		t.Errorf("LastModified = %v, want %v", got.LastModified, ts)
	}
}

func TestListOfAnEmptyPrefixIsNotAnError(t *testing.T) {
	keys, err := s3blobtest.New().Blob("bkt", "analysis/").List(context.Background(), "grades/")
	if err != nil {
		t.Fatalf("List on an empty store: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("want no keys, got %v", keys)
	}
}

func TestKeyJoinsThePrefix(t *testing.T) {
	b := s3blobtest.New().Blob("bkt", "runs/")
	if got := b.Key("abc/output.json"); got != "runs/abc/output.json" {
		t.Errorf("Key = %q, want runs/abc/output.json", got)
	}
	if got := b.Bucket(); got != "bkt" {
		t.Errorf("Bucket = %q, want bkt", got)
	}
}

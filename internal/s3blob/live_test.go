//go:build live

// Live exercises the real AWS path — credential resolution, GetObject/PutObject/
// DeleteObject and the ListObjectsV2 continuation loop — against a scratch
// prefix of a real bucket. The fake in s3blobtest cannot catch SDK-level
// mistakes (a wrong region chain, a mis-set field the fake ignores), so this is
// the check to run before merging a change to s3blob.
//
//	STATE_BUCKET=<bucket> AWS_REGION=us-west-1 go test -tags live ./internal/s3blob/ -v
//
// Everything it writes lives under scratch/s3blob-live/ and is deleted on the
// way out; the test skips rather than fails when STATE_BUCKET is unset.
package s3blob_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

func TestLiveRoundTrip(t *testing.T) {
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		t.Skip("STATE_BUCKET unset; skipping the live S3 exercise")
	}
	ctx := context.Background()
	run := fmt.Sprintf("run-%d/", time.Now().UnixNano())

	b, err := s3blob.New(ctx, bucket, "scratch/s3blob-live/"+run)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		keys, err := b.List(context.Background(), "")
		if err != nil {
			t.Errorf("cleanup list: %v", err)
			return
		}
		for _, k := range keys {
			if err := b.Delete(context.Background(), k); err != nil {
				t.Errorf("cleanup delete %s: %v", k, err)
			}
		}
	})

	if _, found, err := b.Get(ctx, "absent.json"); err != nil || found {
		t.Fatalf("Get of an absent key: found=%v err=%v, want false/nil", found, err)
	}

	body := []byte(`{"hello":"s3blob"}`)
	if err := b.PutJSON(ctx, "dt=2026-07-27/a.json", body); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}
	if err := b.Put(ctx, "dt=2026-07-27/b.ndjson", []byte("{\"n\":1}\n")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, found, err := b.Get(ctx, "dt=2026-07-27/a.json")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if string(got) != string(body) {
		t.Fatalf("bytes mismatch: got %s want %s", got, body)
	}

	keys, err := b.List(ctx, "dt=2026-07-27/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 || keys[0] != "dt=2026-07-27/a.json" || keys[1] != "dt=2026-07-27/b.ndjson" {
		t.Fatalf("List = %v, want both prefix-relative keys in order", keys)
	}

	var meta s3blob.Object
	if err := b.Walk(ctx, "dt=2026-07-27/", func(o s3blob.Object) bool {
		meta = o
		return false // also proves an early stop does not error
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if meta.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", meta.Size, len(body))
	}
	if meta.LastModified.IsZero() {
		t.Error("LastModified not populated from a real listing")
	}

	if err := b.Delete(ctx, "dt=2026-07-27/a.json"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := b.Get(ctx, "dt=2026-07-27/a.json"); found {
		t.Fatal("object still present after Delete")
	}
}

package s3lineup

import (
	"context"
	"fmt"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// Objects hitting the cap is the only signal ListPrefix has that it stopped
// early rather than exhausting the prefix — Walk's callback (`return
// out.Objects < maxKeys`) is what makes >= maxKeys mean "cut off, not
// counted".
func TestListPrefix_TruncatedIsSetWhenTheWalkHitsTheCap(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 1000
	for i := 0; i < maxKeys; i++ {
		f.Objects[fmt.Sprintf("analysis/grades/dt=2020-01-01/filler-%05d.json", i)] = []byte("x")
	}

	got, err := (&InfraStore{blob: f.Blob("b", "")}).ListPrefix(context.Background(), "analysis/grades/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if !got.Truncated {
		t.Errorf("Truncated = false, want true — the walk hit maxKeys (%d) and stopped", maxKeys)
	}
}

// The ordinary case, well under the cap, must not carry the flag — this
// pagination-emulating double is what makes that assertion trustworthy: it
// reproduces real ListObjectsV2 paging (lexicographic order, IsTruncated,
// NextContinuationToken), so a listing that finishes here finishes against
// the real API too.
func TestListPrefix_UntruncatedWalkReportsFalse(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"analysis/grades/dt=2026-07-20/system=atc-ros/grades.ndjson": []byte("x"),
	})
	f.PageSize = 1000

	got, err := (&InfraStore{blob: f.Blob("b", "")}).ListPrefix(context.Background(), "analysis/grades/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false — the prefix has one object, nowhere near the cap")
	}
}

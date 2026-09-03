package s3lineup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// TestReadNewest_AWideWindowComesBackCompleteAndNewestFirst pins the two
// properties the parallel reader (rosterbot-91c4) must keep from the serial
// one it replaced: every listed record comes back, and in listing order —
// newest first, by RunKey's inverted timestamp — however the concurrent reads
// happen to land. 250 is tenantRunScanCap, the escalated window the admin
// page reads for a tenant whose weekly job sits behind a week of hourly ones;
// serially that read was 250 S3 round trips inside a 4 s budget, which is the
// cost this reader exists to remove.
//
// MUTATION: append results in completion order instead of by slot — the IDs
// below then come back shuffled on any run where two reads overlap, which
// under -race and eight workers is every run.
func TestReadNewest_AWideWindowComesBackCompleteAndNewestFirst(t *testing.T) {
	seed := s3blobtest.New()
	s := &RunsStore{blob: seed.Blob("b", "runs/")}
	ctx := context.Background()

	const n = 250
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		rec := lineupapi.RunDetail{Run: lineupapi.Run{
			ID: fmt.Sprintf("r-%03d", i), Command: "optimize", Status: "SUCCESS",
			StartedAt: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		}}
		if err := s.PutRun(ctx, rec); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	runs, err := s.List(ctx, n)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) != n {
		t.Fatalf("List returned %d records, want all %d", len(runs), n)
	}
	for i, r := range runs {
		if want := fmt.Sprintf("r-%03d", n-1-i); r.ID != want {
			t.Fatalf("runs[%d].ID = %q, want %q — results must keep listing order (newest "+
				"first), not the order the concurrent reads completed in", i, r.ID, want)
		}
	}
}

// TestReadNewest_ALimitBelowTheLedgerStillReadsOnlyThatMany keeps the cheap
// window cheap: the parallel reader must not turn "read the newest 25" into
// "read everything and truncate".
func TestReadNewest_ALimitBelowTheLedgerStillReadsOnlyThatMany(t *testing.T) {
	seed := s3blobtest.New()
	s := &RunsStore{blob: seed.Blob("b", "runs/")}
	ctx := context.Background()

	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		rec := lineupapi.RunDetail{Run: lineupapi.Run{
			ID: fmt.Sprintf("r-%03d", i), Command: "optimize", Status: "SUCCESS",
			StartedAt: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339),
		}}
		if err := s.PutRun(ctx, rec); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	runs, err := s.List(ctx, 25)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) != 25 {
		t.Fatalf("List(25) returned %d records", len(runs))
	}
	if runs[0].ID != "r-039" || runs[24].ID != "r-015" {
		t.Fatalf("List(25) window = %s..%s, want r-039..r-015 (the newest 25)", runs[0].ID, runs[24].ID)
	}
}

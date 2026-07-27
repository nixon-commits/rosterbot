//go:build live

// Live reads the real state bucket through the stores that otherwise only ever
// run inside the lineup-API Lambda — the run ledger, the notification feed and
// the infra lister. No CLI command wires them, so a refactor here would
// otherwise reach production untested by anything but the Lambda itself.
//
//	STATE_BUCKET=<bucket> AWS_REGION=us-west-1 go test -tags live ./internal/lineupapi/s3lineup/ -v
//
// It is strictly read-only and skips when STATE_BUCKET is unset.
package s3lineup

import (
	"context"
	"os"
	"testing"
)

func liveBucket(t *testing.T) string {
	t.Helper()
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		t.Skip("STATE_BUCKET unset; skipping the live S3 read")
	}
	return bucket
}

func TestLiveRunLedgerReadsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s, err := NewRuns(ctx, liveBucket(t), "runledger/")
	if err != nil {
		t.Fatalf("NewRuns: %v", err)
	}

	runs, err := s.List(ctx, 5)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("no ledger records read back; the bucket has run history, so this is a read-path failure")
	}
	t.Logf("read %d runs, newest %s (%s, %s)", len(runs), runs[0].ID, runs[0].Command, runs[0].StartedAt)

	// Ledger keys carry an inverted timestamp, so listing order is newest-first.
	for i := 1; i < len(runs); i++ {
		if runs[i].StartedAt > runs[i-1].StartedAt {
			t.Errorf("runs[%d] (%s) starts after runs[%d] (%s); want newest-first",
				i, runs[i].StartedAt, i-1, runs[i-1].StartedAt)
		}
	}

	// A detail lookup must find a run the listing just returned.
	got, found, err := s.Get(ctx, runs[0].ID)
	if err != nil || !found {
		t.Fatalf("Get(%s): found=%v err=%v", runs[0].ID, found, err)
	}
	if got.ID != runs[0].ID {
		t.Errorf("Get returned %s, want %s", got.ID, runs[0].ID)
	}
}

func TestLiveNotificationsFillTheLimit(t *testing.T) {
	ctx := context.Background()
	s, err := NewNotifications(ctx, liveBucket(t), "notifications/")
	if err != nil {
		t.Fatalf("NewNotifications: %v", err)
	}

	notifs, err := s.List(ctx, 3)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notifs) == 0 {
		t.Fatal("no notifications read back from a bucket that has them")
	}
	if len(notifs) > 3 {
		t.Errorf("got %d notifications, want at most the requested 3", len(notifs))
	}
	t.Logf("read %d notifications, newest %s at %s", len(notifs), notifs[0].ID, notifs[0].CreatedAt)
}

func TestLiveInfraListsPartitionsAndSystems(t *testing.T) {
	ctx := context.Background()
	s, err := NewInfra(ctx, liveBucket(t))
	if err != nil {
		t.Fatalf("NewInfra: %v", err)
	}

	got, err := s.ListPrefix(ctx, "analysis/grades/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if got.Objects == 0 {
		t.Fatal("no objects under analysis/grades/; the Analysis Store is populated, so this is a listing failure")
	}
	if len(got.Partitions) == 0 {
		t.Error("no dt= partitions parsed from a populated prefix")
	}
	if len(got.Subkeys) == 0 {
		t.Error("no system= sub-dimensions parsed; the shadow-system coverage chips depend on these")
	}
	if got.LastModified.IsZero() {
		t.Error("LastModified not populated; the staleness judgement depends on it")
	}
	t.Logf("analysis/grades/: %d objects, %d bytes, %d dt= partitions, systems=%v, newest %s",
		got.Objects, got.Bytes, len(got.Partitions), got.Subkeys, got.LastModified.Format("2006-01-02T15:04:05Z"))
}

package s3lineup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestNotificationsRoundTripNewestFirst(t *testing.T) {
	f := s3blobtest.New()
	s := &NotificationsStore{blob: f.Blob("b", "notifications/")}
	ctx := context.Background()

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"oldest", "middle", "newest"} {
		n := lineupapi.Notification{ID: id, CreatedAt: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)}
		if err := s.PutNotification(ctx, n); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}

	got, err := s.List(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 notifications, got %d: %+v", len(got), got)
	}
	// RunKey inverts the timestamp, so ascending key order is newest first.
	if got[0].ID != "newest" || got[2].ID != "oldest" {
		t.Errorf("ids = [%s %s %s], want newest-first", got[0].ID, got[1].ID, got[2].ID)
	}
}

// The feed used to cap one listing at MaxKeys=limit, which returns short when a
// page ends early even though more records remain. Walking with an early stop
// returns exactly `limit` regardless of where page boundaries fall.
func TestNotificationsListFillsTheLimitAcrossPages(t *testing.T) {
	f := s3blobtest.New()
	f.PageSize = 2
	ctx := context.Background()
	s := &NotificationsStore{blob: f.Blob("b", "notifications/")}

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		n := lineupapi.Notification{ID: fmt.Sprintf("n%02d", i), CreatedAt: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
		if err := s.PutNotification(ctx, n); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	got, err := s.List(ctx, 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("want exactly 5 notifications across 2-key pages, got %d", len(got))
	}
}

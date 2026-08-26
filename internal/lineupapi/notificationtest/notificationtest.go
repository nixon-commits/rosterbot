// Package notificationtest is the shared conformance suite for
// lineupapi.NotificationStore implementations, in the runstoretest mold: the
// newest-first ordering and the limit are contract, not adapter behavior —
// the feed the dashboard and the iOS app render must read identically off
// the local file store and S3.
package notificationtest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Store is the read+write pair under test; both adapters implement both
// sides.
type Store interface {
	lineupapi.NotificationStore
	lineupapi.NotificationWriter
}

// Run exercises the NotificationStore contract. newStore must return a
// freshly-empty store on every call.
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	notif := func(id string, at time.Time) lineupapi.Notification {
		return lineupapi.Notification{
			ID:        id,
			Kind:      "lineup",
			Status:    "success",
			Title:     "Fantrax Lineup",
			Message:   "body of " + id,
			CreatedAt: at.Format(time.RFC3339),
			RunID:     "run-" + id,
			UserID:    "tenant-1",
		}
	}

	t.Run("EmptyStoreListsNothing", func(t *testing.T) {
		s := newStore(t)
		ns, err := s.List(ctx, 10)
		if err != nil || len(ns) != 0 {
			t.Fatalf("List(empty) = (%v, %v), want ([], nil)", ns, err)
		}
	})

	t.Run("RoundTripCarriesEveryField", func(t *testing.T) {
		s := newStore(t)
		want := notif("n1", base)
		if err := s.PutNotification(ctx, want); err != nil {
			t.Fatalf("PutNotification: %v", err)
		}
		ns, err := s.List(ctx, 10)
		if err != nil || len(ns) != 1 {
			t.Fatalf("List = (%v, %v), want one record", ns, err)
		}
		if !reflect.DeepEqual(ns[0], want) {
			t.Fatalf("List returned %+v, want %+v", ns[0], want)
		}
	})

	t.Run("ListIsNewestFirstAndHonorsLimit", func(t *testing.T) {
		s := newStore(t)
		for i, id := range []string{"old", "mid", "new"} {
			if err := s.PutNotification(ctx, notif(id, base.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatalf("PutNotification(%s): %v", id, err)
			}
		}
		ns, err := s.List(ctx, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(ns) != 2 || ns[0].ID != "new" || ns[1].ID != "mid" {
			t.Fatalf("List(2) = %v, want [new mid]", ns)
		}
	})
}

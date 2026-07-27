package s3lineup

import (
	"context"
	"encoding/json"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// NotificationsStore is the S3-backed activity feed. Records live at
// <prefix><key>.json with key = lineupapi.RunKey (inverted-timestamp prefix),
// so a plain ascending listing returns newest first.
type NotificationsStore struct{ blob *s3blob.Blob }

// NewNotifications builds a store. prefix should end in "/", e.g. "notifications/".
func NewNotifications(ctx context.Context, bucket, prefix string) (*NotificationsStore, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &NotificationsStore{blob: b}, nil
}

func (s *NotificationsStore) PutNotification(ctx context.Context, n lineupapi.Notification) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return s.blob.PutJSON(ctx, ledgerKey(lineupapi.RunKey(n.CreatedAt, n.ID)), data)
}

// List returns the newest `limit` notifications. It walks (rather than capping
// one listing at MaxKeys) so a short page can't return fewer than requested
// while more remain — the same pagination trap the run ledger hit.
func (s *NotificationsStore) List(ctx context.Context, limit int) ([]lineupapi.Notification, error) {
	var keys []string
	err := s.blob.Walk(ctx, "", func(o s3blob.Object) bool {
		keys = append(keys, o.Key)
		return limit <= 0 || len(keys) < limit
	})
	if err != nil {
		return nil, err
	}

	notifs := make([]lineupapi.Notification, 0, len(keys))
	for _, k := range keys {
		data, found, err := s.blob.Get(ctx, k)
		if err != nil || !found {
			continue
		}
		var n lineupapi.Notification
		if json.Unmarshal(data, &n) == nil {
			notifs = append(notifs, n)
		}
	}
	return notifs, nil
}

var (
	_ lineupapi.NotificationStore  = (*NotificationsStore)(nil)
	_ lineupapi.NotificationWriter = (*NotificationsStore)(nil)
)

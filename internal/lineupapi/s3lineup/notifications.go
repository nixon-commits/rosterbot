package s3lineup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// NotificationsStore is the S3-backed activity feed. Records live at
// <prefix><key>.json with key = lineupapi.RunKey (inverted-timestamp prefix),
// so a plain ascending ListObjectsV2 returns newest first.
type NotificationsStore struct {
	client listAPI
	bucket string
	prefix string
}

// NewNotifications builds a store. prefix should end in "/", e.g. "notifications/".
func NewNotifications(ctx context.Context, bucket, prefix string) (*NotificationsStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &NotificationsStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *NotificationsStore) objKey(key string) string { return s.prefix + key + ".json" }

func (s *NotificationsStore) PutNotification(ctx context.Context, n lineupapi.Notification) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey(lineupapi.RunKey(n.CreatedAt, n.ID))),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

func (s *NotificationsStore) List(ctx context.Context, limit int) ([]lineupapi.Notification, error) {
	out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &s.bucket,
		Prefix:  &s.prefix,
		MaxKeys: aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		if o.Key != nil {
			keys = append(keys, *o.Key)
		}
	}
	sort.Strings(keys)

	notifs := make([]lineupapi.Notification, 0, len(keys))
	for _, k := range keys {
		obj, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
		if err != nil {
			continue
		}
		data, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
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

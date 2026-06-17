package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/notify"
)

// installNotificationRecorder wires notify.Recorder so every Pushover send is
// also persisted to the activity feed (dual-send). Best-effort: feed-write
// failures never affect the push or the command. STATE_BUCKET -> S3
// (notifications/ prefix); otherwise local .lineup/notifications.
func installNotificationRecorder() {
	var w lineupapi.NotificationWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewNotifications(context.Background(), bucket, "notifications/")
		if err != nil {
			return
		}
		w = s
	} else {
		w = lineupapi.NewFileNotificationStore(".lineup/notifications")
	}

	notify.Recorder = func(title, message string) {
		kind := lineupapi.KindFromTitle(title)
		n := lineupapi.Notification{
			ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
			Kind:      kind,
			Status:    lineupapi.ClassifyStatus(kind, title, message),
			Title:     title,
			Message:   message,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			RunID:     os.Getenv("RUN_ID"), // set by entrypoint.sh; links feed -> run
		}
		_ = w.PutNotification(context.Background(), n)
	}
}

package lineupapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Change is one lineup move within a lineup-kind notification.
type Change struct {
	Action string  `json:"action"` // activate | bench
	Player string  `json:"player"`
	Slot   string  `json:"slot,omitempty"`
	Delta  float64 `json:"delta"`
}

// Notification is one activity-feed item — the durable record of an event that
// also went to Pushover (lineup applied, waiver picks, trades, etc.). This feed
// is the app's replacement for Pushover as the primary surface.
type Notification struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // lineup|waivers|claims|transactions|prospects|gs-check|alert
	Title     string   `json:"title"`
	Message   string   `json:"message"`
	CreatedAt string   `json:"created_at"` // RFC3339 UTC
	RunID     string   `json:"run_id,omitempty"`
	Changes   []Change `json:"changes,omitempty"`
}

// NotificationsResponse is the GET /v1/notifications body.
type NotificationsResponse struct {
	Notifications []Notification `json:"notifications"`
}

// NotificationStore is the read side of the activity feed (GET /v1/notifications).
type NotificationStore interface {
	List(ctx context.Context, limit int) ([]Notification, error)
}

// NotificationWriter is the write side, called from the bot's Pushover hook.
type NotificationWriter interface {
	PutNotification(ctx context.Context, n Notification) error
}

// KindFromTitle maps a Pushover title to a feed kind for badging. Claims are
// checked before waivers because the claims title ("Waiver Claims") contains
// both words.
func KindFromTitle(title string) string {
	switch {
	case strings.Contains(title, "Lineup"):
		return "lineup"
	case strings.Contains(title, "Prospect"):
		return "prospects"
	case strings.Contains(title, "Trade"):
		return "transactions"
	case strings.Contains(title, "Claim"):
		return "claims"
	case strings.Contains(title, "Waiver"):
		return "waivers"
	case strings.Contains(title, "GS"):
		return "gs-check"
	default:
		return "alert"
	}
}

// FileNotificationStore is a local-filesystem activity feed: one file per item
// at <dir>/notif-<key>.json, key = RunKey(createdAt, id) for newest-first sort.
type FileNotificationStore struct {
	dir string
}

// NewFileNotificationStore returns a store rooted at dir.
func NewFileNotificationStore(dir string) *FileNotificationStore {
	return &FileNotificationStore{dir: dir}
}

func (s *FileNotificationStore) path(key string) string {
	return filepath.Join(s.dir, "notif-"+key+".json")
}

func (s *FileNotificationStore) PutNotification(_ context.Context, n Notification) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(RunKey(n.CreatedAt, n.ID)), data, 0o644)
}

func (s *FileNotificationStore) List(_ context.Context, limit int) ([]Notification, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "notif-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // inverted-ts prefix => newest first

	out := make([]Notification, 0, limit)
	for _, name := range names {
		if len(out) >= limit {
			break
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var n Notification
		if json.Unmarshal(data, &n) == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

var (
	_ NotificationStore  = (*FileNotificationStore)(nil)
	_ NotificationWriter = (*FileNotificationStore)(nil)
)

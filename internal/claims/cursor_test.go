package claims

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last-claims.json")

	if got := loadCursor(path); !got.IsZero() {
		t.Fatalf("missing cursor file should yield zero time, got %v", got)
	}

	want := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if err := saveCursor(path, want); err != nil {
		t.Fatalf("saveCursor: %v", err)
	}
	if got := loadCursor(path); !got.Equal(want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

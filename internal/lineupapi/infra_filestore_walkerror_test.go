package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// An unreadable entry under the local artifact directory drops that entry's
// contribution to Objects/Bytes exactly the way the S3 cap does — the walk
// continues past it (returning the error would abort WalkDir and blank the
// whole /v1/infra listing over one bad entry), but the resulting Objects/Bytes
// are then a floor, not a count. Before rosterbot-xi3p
// nothing marked that: the S3 lister sets Truncated when it hits its cap, but
// FileInfraStore.ListPrefix skipped a WalkDir error and returned nil without
// ever assigning Truncated, so a listing degraded by a permission error read
// identical to a fully-read one.
func TestFileInfraStore_ListPrefix_UnreadableEntrySetsTruncated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions; the walk would not fail")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 does not block directory listing on Windows")
	}

	root := t.TempDir()
	dir := filepath.Join(root, ".cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	readable := []byte(`{"ok":true}`)
	if err := os.WriteFile(filepath.Join(dir, "readable.json"), readable, 0o644); err != nil {
		t.Fatalf("write readable: %v", err)
	}

	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("mkdir blocked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "hidden.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write hidden: %v", err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod blocked: %v", err)
	}
	t.Cleanup(func() {
		// TempDir's removal needs to be able to descend into blocked/ again.
		_ = os.Chmod(blocked, 0o755)
	})

	listing, err := NewFileInfraStore(root).ListPrefix(context.Background(), layout.Cache.S3Prefix)
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}

	if !listing.Truncated {
		t.Error("Truncated must be true — the walk could not read every entry under the prefix")
	}
	// The readable sibling must still be counted: skipping the unreadable
	// directory must not abort the whole walk (that's the point of skipping
	// rather than returning the error), only mark it degraded.
	if listing.Objects != 1 {
		t.Errorf("objects = %d, want 1 (only the readable file, the walk continued past the blocked entry)", listing.Objects)
	}
	if listing.Bytes != int64(len(readable)) {
		t.Errorf("bytes = %d, want %d", listing.Bytes, len(readable))
	}
}

// The common case must stay a no-op: a fully readable local tree reports an
// exact count, not a floor.
func TestFileInfraStore_ListPrefix_CleanWalkLeavesTruncatedFalse(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := []byte(`{"ok":true}`)
	if err := os.WriteFile(filepath.Join(dir, "a.json"), content, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	listing, err := NewFileInfraStore(root).ListPrefix(context.Background(), layout.Cache.S3Prefix)
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}

	if listing.Truncated {
		t.Error("Truncated must be false — every entry under the prefix was read")
	}
	if listing.Objects != 1 {
		t.Errorf("objects = %d, want 1", listing.Objects)
	}
	if listing.Bytes != int64(len(content)) {
		t.Errorf("bytes = %d, want %d", listing.Bytes, len(content))
	}
}

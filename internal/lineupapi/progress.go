package lineupapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ProgressStore is the read side for live run progress (GET /v1/runs/{id}/progress).
type ProgressStore interface {
	GetProgress(ctx context.Context, runID string) ([]byte, bool, error)
}

// ProgressWriter is the write side: persist the snapshot bytes for a run id.
type ProgressWriter interface {
	PutProgress(ctx context.Context, runID string, data []byte) error
}

// FileProgressStore is a local-filesystem ProgressStore+Writer, one file per run
// at <dir>/<runID>.json. Used by `serve` and local job runs.
type FileProgressStore struct{ dir string }

func NewFileProgressStore(dir string) *FileProgressStore { return &FileProgressStore{dir: dir} }

func (s *FileProgressStore) path(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

// safeRunID reports whether id is a single clean path component (no separators,
// not "." / ".."), so <dir>/<id>.json cannot escape dir. Run ids are opaque
// tokens; a value that isn't a clean component can only be malformed or hostile.
func safeRunID(id string) bool {
	return id != "" && id != "." && id != ".." &&
		!strings.ContainsAny(id, `/\`) && id == filepath.Clean(id)
}

func (s *FileProgressStore) GetProgress(_ context.Context, runID string) ([]byte, bool, error) {
	if !safeRunID(runID) {
		return nil, false, nil
	}
	data, err := os.ReadFile(s.path(runID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *FileProgressStore) PutProgress(_ context.Context, runID string, data []byte) error {
	if !safeRunID(runID) {
		return fmt.Errorf("invalid run id %q", runID)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(runID), data, 0o644)
}

var (
	_ ProgressStore  = (*FileProgressStore)(nil)
	_ ProgressWriter = (*FileProgressStore)(nil)
)

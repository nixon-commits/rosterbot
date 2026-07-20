package lineupapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

func (s *FileProgressStore) GetProgress(_ context.Context, runID string) ([]byte, bool, error) {
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
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(runID), data, 0o644)
}

var (
	_ ProgressStore  = (*FileProgressStore)(nil)
	_ ProgressWriter = (*FileProgressStore)(nil)
)

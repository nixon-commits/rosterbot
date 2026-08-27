package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RunOutput is the GET /v1/runs/{id}/output body: a job-type discriminator plus
// the job-specific result object. Stored verbatim at runs/<id>/output.json.
type RunOutput struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// MarshalOutput serializes a job result into the {type, data} envelope. Indented
// for curl-ability; the iOS decoder is whitespace-agnostic.
func MarshalOutput(jobType string, data any) ([]byte, error) {
	return json.MarshalIndent(RunOutput{Type: jobType, Data: data}, "", "  ")
}

// --- Store interfaces + local file adapter + global hook ---

// OutputStore is the read side for captured job output: fetch the stored bytes
// for a run id. ok=false means 404; err means a backend failure (502).
type OutputStore interface {
	GetOutput(ctx context.Context, runID string) ([]byte, bool, error)
}

// OutputWriter is the write side: persist the marshaled envelope for a run id.
type OutputWriter interface {
	PutOutput(ctx context.Context, runID string, data []byte) error
}

// FileOutputStore is a local-filesystem OutputStore+OutputWriter, one file per
// run at <dir>/<runID>.json. Used by `serve` and local job runs.
type FileOutputStore struct {
	dir string
}

func NewFileOutputStore(dir string) *FileOutputStore { return &FileOutputStore{dir: dir} }

func (s *FileOutputStore) path(runID string) string {
	return filepath.Join(s.dir, runID+".json")
}

func (s *FileOutputStore) GetOutput(_ context.Context, runID string) ([]byte, bool, error) {
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

func (s *FileOutputStore) PutOutput(_ context.Context, runID string, data []byte) error {
	if !safeRunID(runID) {
		return fmt.Errorf("invalid run id %q", runID)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(runID), data, 0o644)
}

var (
	_ OutputStore  = (*FileOutputStore)(nil)
	_ OutputWriter = (*FileOutputStore)(nil)
)

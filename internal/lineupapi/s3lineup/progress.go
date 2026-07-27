package s3lineup

import (
	"context"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// ProgressStore reads/writes live run progress at <prefix><runID>/progress.json,
// beside the run's ledger + output under the same runs/ prefix.
type ProgressStore struct{ blob *s3blob.Blob }

// NewProgress builds a ProgressStore. prefix should end in "/", e.g. "runs/".
func NewProgress(ctx context.Context, bucket, prefix string) (*ProgressStore, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &ProgressStore{blob: b}, nil
}

func progressKey(runID string) string { return runID + "/progress.json" }

func (s *ProgressStore) GetProgress(ctx context.Context, runID string) ([]byte, bool, error) {
	return s.blob.Get(ctx, progressKey(runID))
}

func (s *ProgressStore) PutProgress(ctx context.Context, runID string, data []byte) error {
	return s.blob.PutJSON(ctx, progressKey(runID), data)
}

var (
	_ lineupapi.ProgressStore  = (*ProgressStore)(nil)
	_ lineupapi.ProgressWriter = (*ProgressStore)(nil)
)

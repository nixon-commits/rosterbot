package s3lineup

import (
	"context"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// OutputStore reads/writes captured job output at <prefix><runID>/output.json.
// The per-id sub-path keeps each run's output beside (but distinct from) its
// ledger record under the same runs/ prefix; the ledger listing skips these.
type OutputStore struct{ blob *s3blob.Blob }

// NewOutput builds an OutputStore. prefix should end in "/", e.g. "runs/".
func NewOutput(ctx context.Context, bucket, prefix string) (*OutputStore, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &OutputStore{blob: b}, nil
}

func outputKey(runID string) string { return runID + "/output.json" }

func (s *OutputStore) GetOutput(ctx context.Context, runID string) ([]byte, bool, error) {
	return s.blob.Get(ctx, outputKey(runID))
}

func (s *OutputStore) PutOutput(ctx context.Context, runID string, data []byte) error {
	return s.blob.PutJSON(ctx, outputKey(runID), data)
}

var (
	_ lineupapi.OutputStore  = (*OutputStore)(nil)
	_ lineupapi.OutputWriter = (*OutputStore)(nil)
)

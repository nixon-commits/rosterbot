package s3lineup

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// ProgressStore reads/writes live run progress at <prefix><runID>/progress.json,
// beside the run's ledger + output under the same runs/ prefix.
type ProgressStore struct {
	client api
	bucket string
	prefix string
}

// NewProgress builds a ProgressStore. prefix should end in "/", e.g. "runs/".
func NewProgress(ctx context.Context, bucket, prefix string) (*ProgressStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &ProgressStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *ProgressStore) objKey(runID string) string { return s.prefix + runID + "/progress.json" }

func (s *ProgressStore) GetProgress(ctx context.Context, runID string) ([]byte, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: ptr(s.objKey(runID))})
	if err != nil {
		var nsk *types.NoSuchKey
		var nf *types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *ProgressStore) PutProgress(ctx context.Context, runID string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey(runID)),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

var (
	_ lineupapi.ProgressStore  = (*ProgressStore)(nil)
	_ lineupapi.ProgressWriter = (*ProgressStore)(nil)
)

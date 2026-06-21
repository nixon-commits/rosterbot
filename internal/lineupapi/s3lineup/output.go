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

// OutputStore reads/writes captured job output at <prefix><runID>/output.json.
// The per-id sub-path keeps each run's output beside (but distinct from) its
// ledger record under the same runs/ prefix; the ledger listing skips these.
type OutputStore struct {
	client api
	bucket string
	prefix string
}

// NewOutput builds an OutputStore. prefix should end in "/", e.g. "runs/".
func NewOutput(ctx context.Context, bucket, prefix string) (*OutputStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &OutputStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *OutputStore) objKey(runID string) string { return s.prefix + runID + "/output.json" }

func (s *OutputStore) GetOutput(ctx context.Context, runID string) ([]byte, bool, error) {
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

func (s *OutputStore) PutOutput(ctx context.Context, runID string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey(runID)),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

var (
	_ lineupapi.OutputStore  = (*OutputStore)(nil)
	_ lineupapi.OutputWriter = (*OutputStore)(nil)
)

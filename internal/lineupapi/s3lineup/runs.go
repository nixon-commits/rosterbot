package s3lineup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// listAPI is the slice of the S3 client the run store needs (adds ListObjectsV2
// to the base api).
type listAPI interface {
	api
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// RunsStore is the S3-backed run ledger. Records live at <prefix><key>.json
// where key is lineupapi.RunKey (inverted-timestamp prefix), so a plain
// ascending ListObjectsV2 returns newest first.
type RunsStore struct {
	client listAPI
	bucket string
	prefix string
}

// NewRuns builds a RunsStore. prefix should end in "/", e.g. "runs/".
func NewRuns(ctx context.Context, bucket, prefix string) (*RunsStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &RunsStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *RunsStore) objKey(key string) string { return s.prefix + key + ".json" }

// PutRun writes (or overwrites) a run ledger record. Called by the run-ledger
// CLI command from inside the task.
func (s *RunsStore) PutRun(ctx context.Context, rec lineupapi.RunDetail) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey(lineupapi.RunKey(rec.StartedAt, rec.ID))),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

func (s *RunsStore) List(ctx context.Context, limit int) ([]lineupapi.Run, error) {
	recs, err := s.recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	runs := make([]lineupapi.Run, 0, len(recs))
	for _, r := range recs {
		runs = append(runs, r.Run)
	}
	return runs, nil
}

func (s *RunsStore) Get(ctx context.Context, id string) (*lineupapi.RunDetail, bool, error) {
	// Run detail lookups are always for a recent run (just triggered or visible
	// in the list), so scanning the newest window is sufficient and bounded.
	recs, err := s.recent(ctx, 200)
	if err != nil {
		return nil, false, err
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], true, nil
		}
	}
	return nil, false, nil
}

// recent lists the newest `limit` ledger objects and reads each. Keys sort
// newest-first (inverted-timestamp prefix), so a single MaxKeys=limit page is
// the newest window.
func (s *RunsStore) recent(ctx context.Context, limit int) ([]lineupapi.RunDetail, error) {
	out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  &s.bucket,
		Prefix:  &s.prefix,
		MaxKeys: aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		if o.Key != nil {
			keys = append(keys, *o.Key)
		}
	}
	sort.Strings(keys) // defensive: ensure newest-first ordering

	var recs []lineupapi.RunDetail
	for _, k := range keys {
		obj, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
		if err != nil {
			continue
		}
		data, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			continue
		}
		var rec lineupapi.RunDetail
		if json.Unmarshal(data, &rec) == nil {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

var _ lineupapi.RunStore = (*RunsStore)(nil)

// Package s3grades is the S3 adapter for analysis.Writer (the Analysis Store).
package s3grades

import (
	"bytes"
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/nixon-commits/rosterbot/internal/analysis"
)

type api interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Writer implements analysis.Writer against S3 (prefix should end in "/", e.g. "analysis/").
type Writer struct {
	client api
	bucket string
	prefix string
}

// New constructs a Writer using the default AWS config (env vars / ~/.aws/).
func New(ctx context.Context, bucket, prefix string) (*Writer, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Writer{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

// WriteGrades serializes rows as NDJSON and puts them to S3 at
// <prefix>grades/dt=YYYY-MM-DD/grades.ndjson.
func (w *Writer) WriteGrades(date time.Time, rows []analysis.GradeRow) error {
	b, err := analysis.MarshalNDJSON(rows)
	if err != nil {
		return err
	}
	key := w.prefix + analysis.ObjectKey(date)
	_, err = w.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &w.bucket, Key: &key, Body: bytes.NewReader(b),
	})
	return err
}

// Compile-time proof that *Writer satisfies analysis.Writer.
var _ analysis.Writer = (*Writer)(nil)

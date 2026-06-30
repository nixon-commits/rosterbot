// Package s3grades is the S3 adapter for analysis.Writer and analysis.Reader (the Analysis Store).
package s3grades

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
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

type readAPI interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Reader implements analysis.Reader against S3 (prefix should end in "/").
type Reader struct {
	client readAPI
	bucket string
	prefix string
}

// NewReader constructs a Reader using the default AWS config.
func NewReader(ctx context.Context, bucket, prefix string) (*Reader, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Reader{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

// ReadAll lists every <prefix>grades/dt=*/grades.ndjson object and returns the
// concatenated rows, ordered by object key (date) ascending.
func (r *Reader) ReadAll() ([]analysis.GradeRow, error) {
	ctx := context.Background()
	gradesPrefix := r.prefix + "grades/"
	var keys []string
	var token *string
	for {
		out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &r.bucket, Prefix: &gradesPrefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if strings.HasSuffix(*o.Key, "grades.ndjson") {
				keys = append(keys, *o.Key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	var rows []analysis.GradeRow
	for _, k := range keys {
		obj, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &r.bucket, Key: &k})
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			return nil, err
		}
		rs, err := analysis.UnmarshalNDJSON(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}

// Compile-time proof that *Reader satisfies analysis.Reader.
var _ analysis.Reader = (*Reader)(nil)

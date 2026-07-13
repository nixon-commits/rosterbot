// Package s3teamvalue is the S3 adapter for teamvalue.Writer and
// teamvalue.Reader (the Team Value Store). It mirrors internal/analysis/s3grades
// so the AWS SDK stays out of the teamvalue leaf. The prefix is the full entity
// prefix (e.g. "analysis/team-values/") and should end in "/".
package s3teamvalue

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

	"github.com/nixon-commits/rosterbot/internal/teamvalue"
)

type api interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Writer implements teamvalue.Writer against S3.
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

// WriteValues serializes rows as NDJSON and puts them to S3 at
// <prefix>dt=YYYY-MM-DD/values.ndjson.
func (w *Writer) WriteValues(date time.Time, rows []teamvalue.Row) error {
	b, err := teamvalue.MarshalNDJSON(rows)
	if err != nil {
		return err
	}
	key := w.prefix + teamvalue.ObjectKey(date)
	_, err = w.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &w.bucket, Key: &key, Body: bytes.NewReader(b),
	})
	return err
}

// Compile-time proof that *Writer satisfies teamvalue.Writer.
var _ teamvalue.Writer = (*Writer)(nil)

type readAPI interface {
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// Reader implements teamvalue.Reader against S3.
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

// ReadAll lists every <prefix>dt=*/values.ndjson object and returns the
// concatenated rows, ordered by object key (date) ascending.
func (r *Reader) ReadAll() ([]teamvalue.Row, error) {
	ctx := context.Background()
	var keys []string
	var token *string
	for {
		out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &r.bucket, Prefix: &r.prefix, ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			if strings.HasSuffix(*o.Key, "values.ndjson") {
				keys = append(keys, *o.Key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	var rows []teamvalue.Row
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
		rs, err := teamvalue.UnmarshalNDJSON(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		rows = append(rows, rs...)
	}
	return rows, nil
}

// Compile-time proof that *Reader satisfies teamvalue.Reader.
var _ teamvalue.Reader = (*Reader)(nil)

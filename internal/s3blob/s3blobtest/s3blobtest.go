// Package s3blobtest is an in-memory s3blob.API for tests. It exists as its own
// package (rather than a _test.go helper) because all three adapter packages —
// s3store, s3ndjson and s3lineup — need the same double, and each previously
// hand-rolled its own with slightly different fidelity.
//
// Its listing deliberately emulates real S3 pagination: keys come back in
// lexicographic order, at most PageSize per call, with IsTruncated and
// NextContinuationToken set while more remain. Pagination bugs — assuming one
// page holds everything relevant — only surface against a fake that actually
// pages (rosterbot-432).
package s3blobtest

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// defaultPageSize matches S3's own ListObjectsV2 maximum.
const defaultPageSize = 1000

// Fake serves objects from a map, keyed by full object key (bucket prefix
// included, exactly as the SDK would see it).
type Fake struct {
	Objects map[string][]byte
	// Modified optionally sets an object's LastModified; the zero time is
	// reported for keys absent from this map.
	Modified map[string]time.Time
	// PageSize caps keys returned per list call; 0 means S3's 1000.
	PageSize int
	// ListCalls counts ListObjectsV2 invocations, so a test can assert that
	// the continuation loop actually paged.
	ListCalls int
}

// New returns an empty Fake.
func New() *Fake {
	return &Fake{Objects: map[string][]byte{}, Modified: map[string]time.Time{}}
}

// With returns a Fake preloaded with objects.
func With(objects map[string][]byte) *Fake {
	f := New()
	for k, v := range objects {
		f.Objects[k] = v
	}
	return f
}

// Blob wraps this Fake as a Blob scoped to bucket+prefix.
func (f *Fake) Blob(bucket, prefix string) *s3blob.Blob {
	return s3blob.NewWithClient(f, bucket, prefix)
}

// Keys returns every stored key in sorted order (handy in failure messages).
func (f *Fake) Keys() []string {
	out := make([]string, 0, len(f.Objects))
	for k := range f.Objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *Fake) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.Objects[*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *Fake) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.Objects == nil {
		f.Objects = map[string][]byte{}
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.Objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func (f *Fake) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.Objects, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func (f *Fake) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.ListCalls++

	var matched []string
	for _, k := range f.Keys() {
		if in.Prefix == nil || strings.HasPrefix(k, *in.Prefix) {
			matched = append(matched, k)
		}
	}

	// The continuation token is the last key of the previous page; resume at
	// the first key strictly after it, as real S3 does.
	start := 0
	if in.ContinuationToken != nil {
		start = sort.SearchStrings(matched, *in.ContinuationToken)
		if start < len(matched) && matched[start] == *in.ContinuationToken {
			start++
		}
	}

	size := defaultPageSize
	if f.PageSize > 0 {
		size = f.PageSize
	}
	if in.MaxKeys != nil && *in.MaxKeys > 0 && int(*in.MaxKeys) < size {
		size = int(*in.MaxKeys)
	}

	end := start + size
	truncated := end < len(matched)
	if end > len(matched) {
		end = len(matched)
	}

	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(truncated)}
	for _, k := range matched[start:end] {
		key := k
		obj := types.Object{Key: &key, Size: aws.Int64(int64(len(f.Objects[key])))}
		if ts, ok := f.Modified[key]; ok {
			obj.LastModified = aws.Time(ts)
		}
		out.Contents = append(out.Contents, obj)
	}
	if truncated {
		out.NextContinuationToken = aws.String(matched[end-1])
	}
	return out, nil
}

var _ s3blob.API = (*Fake)(nil)

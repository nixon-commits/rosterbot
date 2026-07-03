package s3lineup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeS3 struct{ objects map[string][]byte }

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objects[*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.objects == nil {
		f.objects = map[string][]byte{}
	}
	b, _ := io.ReadAll(in.Body)
	f.objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func TestOutputStoreRoundTrip(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &OutputStore{client: f, bucket: "b", prefix: "runs/"}

	if _, ok, _ := s.GetOutput(context.Background(), "abc"); ok {
		t.Fatal("expected miss")
	}
	if err := s.PutOutput(context.Background(), "abc", []byte(`{"type":"grade","data":{}}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, stored := f.objects["runs/abc/output.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", keys(f.objects))
	}
	got, ok, err := s.GetOutput(context.Background(), "abc")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if string(got) != `{"type":"grade","data":{}}` {
		t.Fatalf("bytes mismatch: %s", got)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// listFakeS3 emulates real S3 ListObjectsV2 pagination semantics: keys are
// returned in lexicographic order, at most MaxKeys per call, with
// IsTruncated/NextContinuationToken set when more keys remain. Real bugs in
// pagination logic (e.g. assuming a single page holds everything relevant)
// only surface if the fake actually pages like S3 does.
type listFakeS3 struct {
	*fakeS3
}

func (f *listFakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	keys := make([]string, 0, len(f.objects))
	for k := range f.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	start := 0
	if in.ContinuationToken != nil {
		for i, k := range keys {
			if k > *in.ContinuationToken {
				start = i
				break
			}
			start = i + 1
		}
	}

	maxKeys := 1000
	if in.MaxKeys != nil && *in.MaxKeys > 0 {
		maxKeys = int(*in.MaxKeys)
	}

	end := start + maxKeys
	truncated := end < len(keys)
	if end > len(keys) {
		end = len(keys)
	}

	var contents []types.Object
	for _, k := range keys[start:end] {
		k := k
		contents = append(contents, types.Object{Key: &k})
	}

	out := &s3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(truncated)}
	if truncated {
		last := keys[end-1]
		out.NextContinuationToken = &last
	}
	return out, nil
}

func TestRunsListIgnoresOutputSubKeys(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"runs/9999999999-abc.json": []byte(`{"id":"abc","status":"SUCCESS","started_at":"2026-06-20T00:00:00Z"}`),
		"runs/abc/output.json":     []byte(`{"type":"grade","data":{}}`),
	}}
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runs/"}
	runs, err := s.List(context.Background(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "abc" {
		t.Fatalf("want exactly the ledger run, got %+v", runs)
	}
}

// TestRunsListPaginatesPastOutputSubKeys reproduces the live bug: run output
// sub-objects (runs/<hex-id>/output.json) whose hex id starts with a
// character below the ledger's inverted-timestamp prefix ("8...") sort first
// in S3's lexicographic listing. With enough of them to fill a page, a
// single-page list (the old MaxKeys=limit behavior) sees zero ledger records
// and returns an empty run list even though ledger records exist. The reader
// must paginate (follow NextContinuationToken) until it collects `limit`
// ledger records or exhausts all pages.
func TestRunsListPaginatesPastOutputSubKeys(t *testing.T) {
	objects := map[string][]byte{}
	// 32 output sub-objects with hex ids "00".."1f" - all start with a digit
	// below '8', so they sort before every ledger key below and would fully
	// occupy a small page on their own.
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("%02x", i)
		objects["runs/"+id+"/output.json"] = []byte(`{"type":"grade","data":{}}`)
	}
	// 3 ledger records, newest first by inverted timestamp, sorting after all
	// of the above.
	objects["runs/8214999999-newest.json"] = []byte(`{"id":"newest","status":"SUCCESS","started_at":"2026-07-03T00:00:00Z"}`)
	objects["runs/8215999999-middle.json"] = []byte(`{"id":"middle","status":"SUCCESS","started_at":"2026-07-02T00:00:00Z"}`)
	objects["runs/8216999999-oldest.json"] = []byte(`{"id":"oldest","status":"SUCCESS","started_at":"2026-07-01T00:00:00Z"}`)

	f := &fakeS3{objects: objects}
	lf := &listFakeS3{fakeS3: f}
	s := &RunsStore{client: lf, bucket: "b", prefix: "runs/"}

	runs, err := s.List(context.Background(), 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 ledger runs, got %d: %+v", len(runs), runs)
	}
	if runs[0].ID != "newest" || runs[1].ID != "middle" {
		t.Fatalf("want newest-first [newest, middle], got %+v", runs)
	}
}

package s3lineup

import (
	"bytes"
	"context"
	"io"
	"testing"

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

type listFakeS3 struct {
	*fakeS3
}

func (f *listFakeS3) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var contents []types.Object
	for k := range f.objects {
		k := k
		contents = append(contents, types.Object{Key: &k})
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
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

package s3store

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeAPI struct{ objects map[string][]byte }

func (f *fakeAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objects[*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}
func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}
func (f *fakeAPI) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3Store_KeyPrefixAndNotFound(t *testing.T) {
	f := &fakeAPI{objects: map[string][]byte{}}
	s := &Store{client: f, bucket: "b", prefix: "cache/"}

	if _, found, err := s.Get("fangraphs-bat"); err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
	if err := s.Put("fangraphs-bat", []byte("xyz")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok := f.objects["cache/fangraphs-bat.json"]; !ok {
		t.Fatalf("object not stored under cache/fangraphs-bat.json: keys=%v", f.objects)
	}
	got, found, err := s.Get("fangraphs-bat")
	if err != nil || !found || string(got) != "xyz" {
		t.Fatalf("get: %q found=%v err=%v", got, found, err)
	}
	if err := s.Remove("fangraphs-bat"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, found, _ := s.Get("fangraphs-bat"); found {
		t.Fatal("expected removed")
	}
}

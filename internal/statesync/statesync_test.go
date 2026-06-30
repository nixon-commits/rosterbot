package statesync

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 is an in-memory object store keyed by full object key.
type fakeS3 struct{ objects map[string][]byte }

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var contents []types.Object
	prefix := ""
	if in.Prefix != nil {
		prefix = *in.Prefix
	}
	for k := range f.objects {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			contents = append(contents, types.Object{Key: aws.String(k)})
		}
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}
func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objects[*in.Key]
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}
func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}
func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDown_MapsKeysUnderPrefixToLocalTree(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"session/cookie.json":    []byte("c"),
		"session/sub/extra.json": []byte("e"),
		"other/ignored.json":     []byte("x"),
	}}
	s := &Syncer{s3: f}
	dir := t.TempDir()
	if err := s.Down(context.Background(), "b", "session/", dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cookie.json")); err != nil || string(b) != "c" {
		t.Fatalf("cookie.json: %q err=%v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sub", "extra.json")); err != nil || string(b) != "e" {
		t.Fatalf("sub/extra.json: %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ignored.json")); !os.IsNotExist(err) {
		t.Fatal("out-of-prefix object should not have been downloaded")
	}
}

func TestUp_WithDelete_RemovesOnlyOrphansUnderPrefix(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{
		"dist/old.html":  []byte("stale"), // orphan under prefix -> must be deleted
		"keep/other.txt": []byte("safe"),  // outside prefix -> must survive
	}}
	s := &Syncer{s3: f}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Up(context.Background(), "b", "dist/", dir, true); err != nil {
		t.Fatal(err)
	}
	got := keys(f.objects)
	want := []string{"dist/index.html", "keep/other.txt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after --delete sync got %v, want %v", got, want)
	}
}

func TestUp_NoDelete_LeavesRemoteUntouched(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{"backtest/old.json": []byte("keep")}}
	s := &Syncer{s3: f}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "new.json"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Up(context.Background(), "b", "backtest/", dir, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.objects["backtest/old.json"]; !ok {
		t.Fatal("non-delete sync must not remove pre-existing remote objects")
	}
	if _, ok := f.objects["backtest/new.json"]; !ok {
		t.Fatal("expected uploaded object")
	}
}

func TestUp_MissingLocalDirIsNoop(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &Syncer{s3: f}
	if err := s.Up(context.Background(), "b", "session/", filepath.Join(t.TempDir(), "absent"), true); err != nil {
		t.Fatalf("missing dir should be a no-op, got %v", err)
	}
}

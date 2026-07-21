package s3ndjson

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// fakeAPI serves objects from a map. pageSize > 0 forces pagination so the
// continuation-token loop is exercised.
type fakeAPI struct {
	objs     map[string][]byte
	pageSize int
	listCall int
}

func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objs[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, io.EOF
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listCall++

	var all []string
	for k := range f.objs {
		if strings.HasPrefix(k, *in.Prefix) {
			all = append(all, k)
		}
	}
	sort.Strings(all)

	start := 0
	if in.ContinuationToken != nil {
		for i, k := range all {
			if k == *in.ContinuationToken {
				start = i
				break
			}
		}
	}
	end := len(all)
	if f.pageSize > 0 && start+f.pageSize < end {
		end = start + f.pageSize
	}

	out := &s3.ListObjectsV2Output{}
	for _, k := range all[start:end] {
		key := k
		out.Contents = append(out.Contents, s3types.Object{Key: &key})
	}
	if end < len(all) {
		truncated := true
		next := all[end]
		out.IsTruncated = &truncated
		out.NextContinuationToken = &next
	}
	return out, nil
}

type row struct {
	Dt   string `json:"dt"`
	Name string `json:"name"`
}

func newStore(f *fakeAPI) *Store {
	return &Store{client: f, bucket: "b", prefix: "analysis/"}
}

func TestPutJoinsBucketPrefix(t *testing.T) {
	f := &fakeAPI{objs: map[string][]byte{}}
	s := newStore(f)

	if err := s.Put("grades/dt=2026-07-20/rows.ndjson", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok := f.objs["analysis/grades/dt=2026-07-20/rows.ndjson"]; !ok {
		t.Fatalf("object not written under the bucket prefix; keys=%v", keysOf(f))
	}
}

func TestListStripsBucketPrefix(t *testing.T) {
	f := &fakeAPI{objs: map[string][]byte{
		"analysis/grades/dt=2026-07-20/rows.ndjson": []byte("x"),
	}}
	keys, err := newStore(f).List("grades/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || keys[0] != "grades/dt=2026-07-20/rows.ndjson" {
		t.Errorf("keys = %v, want prefix-relative keys", keys)
	}
}

func TestListPaginates(t *testing.T) {
	f := &fakeAPI{objs: map[string][]byte{}, pageSize: 2}
	for _, d := range []string{"01", "02", "03", "04", "05"} {
		f.objs["analysis/grades/dt=2026-07-"+d+"/rows.ndjson"] = []byte("x")
	}
	keys, err := newStore(f).List("grades/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 5 {
		t.Fatalf("want all 5 keys across pages, got %d: %v", len(keys), keys)
	}
	if f.listCall < 2 {
		t.Errorf("expected the continuation loop to page, got %d list calls", f.listCall)
	}
}

func TestRoundTripThroughReadAll(t *testing.T) {
	f := &fakeAPI{objs: map[string][]byte{}}
	s := newStore(f)

	if err := ndjsonstore.Write(s, "grades/dt=2026-07-18/rows.ndjson", []row{{Dt: "2026-07-18", Name: "early"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ndjsonstore.Write(s, "grades/dt=2026-07-20/rows.ndjson", []row{{Dt: "2026-07-20", Name: "late"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.objs["analysis/grades/dt=2026-07-19/notes.txt"] = []byte("ignore me")

	got, err := ndjsonstore.ReadAll[row](s, "grades/", "rows.ndjson", nil)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(got), got)
	}
	if got[0].Name != "early" || got[1].Name != "late" {
		t.Errorf("rows = %+v, want chronological order", got)
	}
}

func keysOf(f *fakeAPI) []string {
	var ks []string
	for k := range f.objs {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

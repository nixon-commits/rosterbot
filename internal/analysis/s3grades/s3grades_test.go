package s3grades

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/nixon-commits/rosterbot/internal/analysis"
)

type fakeAPI struct{ puts map[string][]byte }

func (f *fakeAPI) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.puts[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func TestS3Writer_KeyAndBody(t *testing.T) {
	f := &fakeAPI{puts: map[string][]byte{}}
	w := &Writer{client: f, bucket: "b", prefix: "analysis/"}
	date := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGrades(date, []analysis.GradeRow{{Dt: "2026-06-15", PlayerID: "1"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	key := "analysis/grades/dt=2026-06-15/grades.ndjson"
	if _, ok := f.puts[key]; !ok {
		t.Fatalf("object not at %s; keys=%v", key, f.puts)
	}
}

type fakeReadAPI struct{ objs map[string][]byte }

func (f *fakeReadAPI) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out := &s3.ListObjectsV2Output{}
	for k := range f.objs {
		if strings.HasPrefix(k, *in.Prefix) {
			key := k
			out.Contents = append(out.Contents, s3types.Object{Key: &key})
		}
	}
	return out, nil
}

func (f *fakeReadAPI) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, io.EOF
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func TestS3Reader_ReadAll(t *testing.T) {
	d1, _ := analysis.MarshalNDJSON([]analysis.GradeRow{{Dt: "2026-06-14", PlayerID: "1"}})
	d2, _ := analysis.MarshalNDJSON([]analysis.GradeRow{{Dt: "2026-06-15", PlayerID: "2"}, {Dt: "2026-06-15", PlayerID: "3"}})
	f := &fakeReadAPI{objs: map[string][]byte{
		"analysis/grades/dt=2026-06-14/grades.ndjson": d1,
		"analysis/grades/dt=2026-06-15/grades.ndjson": d2,
		"analysis/other/ignore.json":                  []byte("{}"),
	}}
	r := &Reader{client: f, bucket: "b", prefix: "analysis/"}
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Dt != "2026-06-14" {
		t.Fatalf("want sorted-by-key first row 2026-06-14, got %q", rows[0].Dt)
	}
}

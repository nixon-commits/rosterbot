package s3grades

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
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

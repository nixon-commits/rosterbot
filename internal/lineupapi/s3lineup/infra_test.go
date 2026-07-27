package s3lineup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func TestInfraListPrefixAggregatesAndFindsSystemPartitions(t *testing.T) {
	newest := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	f := s3blobtest.With(map[string][]byte{
		"analysis/grades/dt=2026-07-20/system=atc-ros/grades.ndjson":     []byte("aaa"),
		"analysis/grades/dt=2026-07-20/system=steamer-ros/grades.ndjson": []byte("bb"),
		"analysis/grades/dt=2026-07-21/system=atc-ros/grades.ndjson":     []byte("c"),
		"analysis/grades/dt=2026-07-19/grades.ndjson":                    []byte("dddd"), // legacy, no system=
		"cache/unrelated.json": []byte("nope"),
	})
	f.Modified["analysis/grades/dt=2026-07-21/system=atc-ros/grades.ndjson"] = newest
	f.Modified["analysis/grades/dt=2026-07-20/system=atc-ros/grades.ndjson"] = newest.Add(-48 * time.Hour)

	got, err := (&InfraStore{blob: f.Blob("b", "")}).ListPrefix(context.Background(), "analysis/grades/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if got.Objects != 4 {
		t.Errorf("Objects = %d, want 4 (cache/ must not be counted)", got.Objects)
	}
	if got.Bytes != 10 {
		t.Errorf("Bytes = %d, want 10", got.Bytes)
	}
	if !got.LastModified.Equal(newest) {
		t.Errorf("LastModified = %v, want the newest object's %v", got.LastModified, newest)
	}
	if fmt.Sprint(got.Partitions) != "[2026-07-19 2026-07-20 2026-07-21]" {
		t.Errorf("Partitions = %v, want all three dt= values sorted", got.Partitions)
	}
	// A shadow system that stops writing shows up as a missing chip, which is
	// only possible if sub-dimensions come from the key structure.
	if fmt.Sprint(got.Subkeys) != "[atc-ros steamer-ros]" {
		t.Errorf("Subkeys = %v, want the system= values sorted", got.Subkeys)
	}
}

// Under archive/ the sub-dimension is the per-source directory, not a system=
// segment, so one listing still reports which sources produced that day.
func TestInfraListPrefixReportsArchiveSources(t *testing.T) {
	f := s3blobtest.With(map[string][]byte{
		"archive/hkb/dt=2026-07-20/ranks.json":       []byte("x"),
		"archive/savant/dt=2026-07-20/season.csv":    []byte("y"),
		"archive/fangraphs/dt=2026-07-20/atc.json":   []byte("z"),
		"archive/prospects/dt=2026-07-20/board.json": []byte("w"),
	})

	got, err := (&InfraStore{blob: f.Blob("b", "")}).ListPrefix(context.Background(), "archive/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if fmt.Sprint(got.Subkeys) != "[fangraphs hkb prospects savant]" {
		t.Errorf("Subkeys = %v, want the four source directories sorted", got.Subkeys)
	}
}

func TestInfraListPrefixOnAnEmptyPrefixIsNotAnError(t *testing.T) {
	got, err := (&InfraStore{blob: s3blobtest.New().Blob("b", "")}).ListPrefix(context.Background(), "runledger/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if got.Objects != 0 || got.Partitions != nil || got.Subkeys != nil {
		t.Errorf("got %+v, want a zero listing", got)
	}
}

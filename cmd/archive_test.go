package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

func TestRunArchiveSourcesIsolatesFailures(t *testing.T) {
	root := t.TempDir()
	good := archive.FuncSource{N: "good", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return []archive.Artifact{{Filename: "ok.json", Bytes: []byte("1")}}, nil
	}}
	bad := archive.FuncSource{N: "bad", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return nil, errors.New("boom")
	}}
	date := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	err := runArchiveSources(context.Background(), []archive.Source{good, bad}, archive.Writer{Root: root}, date, false)
	if err != nil {
		t.Fatalf("one failure should not fail the command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "good", "dt=2026-06-30", "ok.json")); err != nil {
		t.Errorf("good source should have written: %v", err)
	}
}

func TestRunArchiveSourcesAllFailedIsError(t *testing.T) {
	bad := archive.FuncSource{N: "bad", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return nil, errors.New("boom")
	}}
	err := runArchiveSources(context.Background(), []archive.Source{bad}, archive.Writer{Root: t.TempDir()},
		time.Now(), false)
	if err == nil {
		t.Fatal("all sources failing must return an error")
	}
}

func TestRunArchiveSourcesDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	good := archive.FuncSource{N: "good", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return []archive.Artifact{{Filename: "ok.json", Bytes: []byte("1")}}, nil
	}}
	if err := runArchiveSources(context.Background(), []archive.Source{good}, archive.Writer{Root: root},
		time.Now(), true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "good")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write")
	}
}

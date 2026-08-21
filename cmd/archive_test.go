package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
)

// --- --date resolution ---

// The bug this pins: `archive --date <past>` looked like a backfill and was
// not. Every source fetches current upstream data, so the run would have
// written TODAY'S HKB rankings, FanGraphs projections and prospect board under
// a historical dt=, filling the gap on the Infra page with bytes that describe
// a different day (rosterbot-0l31). A visible gap is recoverable knowledge; a
// partition of the wrong day's data is not, because nothing downstream can ever
// tell it apart from a real one.
func TestResolveArchiveDate_RefusesAPastDate(t *testing.T) {
	now := time.Date(2026, 8, 21, 14, 15, 0, 0, time.UTC)
	_, err := resolveArchiveDate("2026-08-01", now)
	if err == nil {
		t.Fatal("a past --date must be refused: the sources would write today's data under dt=2026-08-01")
	}
	if !strings.Contains(err.Error(), "2026-08-01") {
		t.Errorf("error must name the refused date so the operator can see what it would have written: %v", err)
	}
}

// The legitimate case, and the reason this is a refusal rather than dropping
// the flag: a run that failed at 14:15 UTC is re-run the same day, and today is
// the one date the fetches actually describe.
func TestResolveArchiveDate_AllowsTodayAsASameDayRetry(t *testing.T) {
	now := time.Date(2026, 8, 21, 22, 0, 0, 0, time.UTC)
	got, err := resolveArchiveDate("2026-08-21", now)
	if err != nil {
		t.Fatalf("today must be allowed — it is the same-day retry after a failed run: %v", err)
	}
	if got.Format("2006-01-02") != "2026-08-21" {
		t.Errorf("date = %s, want 2026-08-21", got.Format("2006-01-02"))
	}
}

// A future date is wrong for the same reason plus one of its own: a dt= ahead
// of today keeps LatestPartition permanently fresh, so the Infra row would stop
// reporting staleness for the artifact it was dated into.
func TestResolveArchiveDate_RefusesAFutureDate(t *testing.T) {
	now := time.Date(2026, 8, 21, 14, 15, 0, 0, time.UTC)
	if _, err := resolveArchiveDate("2026-08-22", now); err == nil {
		t.Fatal("a future --date must be refused: it captures today's data and masks staleness")
	}
}

// No flag is the scheduled path — the CDK's Archive schedule launches bare
// `archive` — so it must stay the zero-friction default.
func TestResolveArchiveDate_EmptyFlagIsTodayInUTC(t *testing.T) {
	// 01:30 UTC on the 21st is still the 20th in ET, so a local-time default
	// would silently write the previous day's partition for a third of the day.
	now := time.Date(2026, 8, 21, 1, 30, 0, 0, time.UTC)
	got, err := resolveArchiveDate("", now)
	if err != nil {
		t.Fatalf("no --date must always resolve: %v", err)
	}
	if got.UTC().Format("2006-01-02") != "2026-08-21" {
		t.Errorf("date = %s, want 2026-08-21 (UTC)", got.UTC().Format("2006-01-02"))
	}
}

func TestResolveArchiveDate_RejectsAnUnparseableDate(t *testing.T) {
	if _, err := resolveArchiveDate("21-08-2026", time.Now()); err == nil {
		t.Fatal("an unparseable --date must be refused, not silently treated as today")
	}
}

// --- source isolation ---

func TestRunArchiveSourcesIsolatesFailures(t *testing.T) {
	root := t.TempDir()
	good := archive.FuncSource{N: "good", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return []archive.Artifact{{Filename: "ok.json", Bytes: []byte("1")}}, nil
	}}
	bad := archive.FuncSource{N: "bad", F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return nil, errors.New("boom")
	}}
	date := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	err := runArchiveSources(context.Background(), []archive.Source{good, bad}, archive.NewFileWriter(root), date, false)
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
	err := runArchiveSources(context.Background(), []archive.Source{bad}, archive.NewFileWriter(t.TempDir()),
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
	if err := runArchiveSources(context.Background(), []archive.Source{good}, archive.NewFileWriter(root),
		time.Now(), true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "good")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write")
	}
}

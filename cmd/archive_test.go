package cmd

import (
	"context"
	"errors"
	"fmt"
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

// --- source isolation and the positive control ---
//
// A healthy archive run used to print NOTHING: warn-per-source, error only
// when every source failed. Measured 2026-09-30 over the 14:15Z CloudWatch
// slot: zero events on nine of eleven days, nine `warn:` lines on the two
// FanGraphs-403 days -- so a flawless run and a total loss of projections and
// prospects were indistinguishable by log content, and dt=2026-09-26/27 of two
// as-of-date sources were gone before anyone looked (rosterbot-kvgw). Every run
// now prints one reconcilable summary line, names each lost source on a line a
// log filter can match, and a whole-source loss fails the run so opsalert's
// Streak sees it. Isolation is kept: the surviving sources are written first.

func okSource(name string, n int) archive.FuncSource {
	return archive.FuncSource{N: name, F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		arts := make([]archive.Artifact, 0, n)
		for i := 0; i < n; i++ {
			arts = append(arts, archive.Artifact{Filename: fmt.Sprintf("%d.json", i), Bytes: []byte("1")})
		}
		return arts, nil
	}}
}

func badSource(name string) archive.FuncSource {
	return archive.FuncSource{N: name, F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		return nil, errors.New("boom")
	}}
}

// failingStore is a Store whose every write fails, so a lost WRITE (as opposed
// to a lost fetch) can be asserted.
type failingStore struct{}

func (failingStore) WritePartition(string, []archive.Artifact) error { return errors.New("s3 down") }

// Nothing is ever captured in a store whose writes all fail, so the catch-up
// read answers "missing" rather than erroring: the write is the thing under
// test here, and a read error would mask it.
func (failingStore) HasPartition(string) (bool, error) { return false, nil }

var archiveTestDate = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

func TestRunArchiveSources_CleanRunPrintsOneSummaryNamingEverySource(t *testing.T) {
	var out strings.Builder
	err := runArchiveSources(context.Background(), []archive.Source{okSource("hkb", 1), okSource("savant", 5)},
		archive.NewFileWriter(t.TempDir()), archiveTestDate, archiveRun{}, &out)
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if !strings.Contains(out.String(), "archive: wrote 2/2 sources (hkb, savant)") {
		t.Errorf("a clean run must print a summary naming the count and every source; got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "LOST") {
		t.Errorf("a clean run must not name a lost source; got:\n%s", out.String())
	}
}

func TestRunArchiveSources_OneLostSourceIsNamedWrittenAroundAndFailsTheRun(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder
	err := runArchiveSources(context.Background(), []archive.Source{okSource("hkb", 1), badSource("projections"), okSource("savant", 1)},
		archive.NewFileWriter(root), archiveTestDate, archiveRun{}, &out)

	// Isolation: the surviving sources are written regardless.
	for _, name := range []string{"hkb", "savant"} {
		if _, serr := os.Stat(filepath.Join(root, name, "dt=2026-06-30", "0.json")); serr != nil {
			t.Errorf("%s should have been written despite the projections loss: %v", name, serr)
		}
	}
	// Escalation: a whole source lost is a failed run, and the error names it.
	if err == nil {
		t.Fatal("losing a whole source must fail the run so the ledger and opsalert see it")
	}
	if !strings.Contains(err.Error(), "projections") {
		t.Errorf("the error must name the lost source: %v", err)
	}
	// Log content alone distinguishes this run from a clean one.
	got := out.String()
	if !strings.Contains(got, "archive: LOST source projections: boom") {
		t.Errorf("the lost source must be named on a filter-matchable line; got:\n%s", got)
	}
	if !strings.Contains(got, "archive: wrote 2/3 sources (hkb, savant); lost 1 (projections)") {
		t.Errorf("the summary must reconcile written against lost; got:\n%s", got)
	}
}

func TestRunArchiveSources_AllLostIsAnErrorAndSaysSo(t *testing.T) {
	var out strings.Builder
	err := runArchiveSources(context.Background(), []archive.Source{badSource("hkb")},
		archive.NewFileWriter(t.TempDir()), archiveTestDate, archiveRun{}, &out)
	if err == nil {
		t.Fatal("all sources failing must return an error")
	}
	if !strings.Contains(out.String(), "archive: wrote 0/1 sources") {
		t.Errorf("even a total loss prints the summary, so the line is unconditional; got:\n%s", out.String())
	}
}

// A source that succeeds with nothing to write has captured nothing: the
// day's partition is as absent as after a fetch error, so it is lost, not ok.
func TestRunArchiveSources_ZeroArtifactsIsALostSource(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder
	err := runArchiveSources(context.Background(), []archive.Source{okSource("prospects", 0), okSource("hkb", 1)},
		archive.NewFileWriter(root), archiveTestDate, archiveRun{}, &out)
	if err == nil {
		t.Fatal("a source that produced zero artifacts is a lost source and must fail the run")
	}
	if !strings.Contains(out.String(), "archive: LOST source prospects: produced 0 artifacts") {
		t.Errorf("the empty source must be named with its reason; got:\n%s", out.String())
	}
	if _, serr := os.Stat(filepath.Join(root, "prospects")); !os.IsNotExist(serr) {
		t.Error("an empty source must not write a partition")
	}
	if !strings.Contains(out.String(), "wrote 1/2 sources (hkb); lost 1 (prospects)") {
		t.Errorf("summary must count the empty source as lost; got:\n%s", out.String())
	}
}

func TestRunArchiveSources_WriteFailureIsALostSource(t *testing.T) {
	var out strings.Builder
	err := runArchiveSources(context.Background(), []archive.Source{okSource("hkb", 1)},
		archive.NewWriter(failingStore{}), archiveTestDate, archiveRun{}, &out)
	if err == nil {
		t.Fatal("a fetched source whose write failed was not archived and must fail the run")
	}
	if !strings.Contains(out.String(), "archive: LOST source hkb: write: s3 down") {
		t.Errorf("a lost write must be named as such; got:\n%s", out.String())
	}
}

// A partial feed failure INSIDE a source (projections archiving 6 of its 8
// FanGraphs blobs, warning about the rest) is the source's own warning and
// not a loss here: the source returned artifacts, the day has a partition.
func TestRunArchiveSources_PartialFeedInsideASourceIsNotALoss(t *testing.T) {
	var out strings.Builder
	err := runArchiveSources(context.Background(), []archive.Source{okSource("projections", 6)},
		archive.NewFileWriter(t.TempDir()), archiveTestDate, archiveRun{}, &out)
	if err != nil {
		t.Fatalf("a source that archived some of its feeds is written, not lost: %v", err)
	}
	if !strings.Contains(out.String(), "archive: wrote 1/1 sources (projections)") || strings.Contains(out.String(), "LOST") {
		t.Errorf("partial feed loss is the source's warning, not a lost source; got:\n%s", out.String())
	}
}

func TestRunArchiveSources_DryRunWritesNothingAndSaysFetched(t *testing.T) {
	root := t.TempDir()
	var out strings.Builder
	if err := runArchiveSources(context.Background(), []archive.Source{okSource("hkb", 1)}, archive.NewFileWriter(root),
		archiveTestDate, archiveRun{dryRun: true}, &out); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "hkb")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write")
	}
	if !strings.Contains(out.String(), "archive (dry-run): fetched 1/1 sources (hkb)") {
		t.Errorf("a dry run must say it fetched rather than wrote; got:\n%s", out.String())
	}
}

// --- catch-up: the second, third and later chances at the same day ---
//
// One fixed daily slot is one chance, and FanGraphs' Cloudflare challenge
// lasts hours. From 2026-11-24 the 14:15Z run was blocked on four consecutive
// days, so four days of projections and prospects are gone for good. What
// fetch.go already names as the real protection against the challenge --
// "some run in the clean window lands the refresh" -- archive did not have.
//
// It does now: the ArchiveCatchUp schedule fires hourly from 15:15Z to 23:15Z
// and each fire asks the store what today already holds. That read is what
// makes the extra fires cheap enough to be worth having, and it is also what
// keeps them honest -- a run that re-fetched everything would turn a healthy
// day into nine more full captures of HKB and five Savant CSVs.

// memStore records what it was asked to write and answers the catch-up read
// from the same map, so a test can seed a partially captured day.
type memStore struct {
	captured map[string]bool
	hasErr   error
}

func newMemStore(capturedDirs ...string) *memStore {
	m := &memStore{captured: map[string]bool{}}
	for _, d := range capturedDirs {
		m.captured[d] = true
	}
	return m
}

func (m *memStore) WritePartition(dir string, arts []archive.Artifact) error {
	m.captured[dir] = len(arts) > 0
	return nil
}

func (m *memStore) HasPartition(dir string) (bool, error) {
	if m.hasErr != nil {
		return false, m.hasErr
	}
	return m.captured[dir], nil
}

// countingSource reports how many times it was fetched, which is the whole
// assertion for a skip: a skipped source must not touch the upstream at all.
func countingSource(name string, calls *int) archive.FuncSource {
	return archive.FuncSource{N: name, F: func(_ context.Context, _ time.Time) ([]archive.Artifact, error) {
		*calls++
		return []archive.Artifact{{Filename: "0.json", Bytes: []byte("1")}}, nil
	}}
}

func TestRunArchiveSources_CatchUpDoesNotRefetchASourceAlreadyCapturedToday(t *testing.T) {
	var hkbCalls int
	store := newMemStore("hkb/dt=2026-06-30")
	var out strings.Builder

	err := runArchiveSources(context.Background(),
		[]archive.Source{countingSource("hkb", &hkbCalls), okSource("projections", 8)},
		archive.NewWriter(store), archiveTestDate, archiveRun{catchUp: true}, &out)
	if err != nil {
		t.Fatalf("filling the one hole is a successful catch-up: %v", err)
	}
	if hkbCalls != 0 {
		t.Errorf("hkb was fetched %d times; a source already captured today must not be re-fetched -- "+
			"nine extra fires a day only pay for themselves if a healthy day costs nothing", hkbCalls)
	}
	if !store.captured["projections/dt=2026-06-30"] {
		t.Error("the missing source must actually be written")
	}
	got := out.String()
	if !strings.Contains(got, "archive (catch-up): dt=2026-06-30 filled 1/1 attempted (projections); already captured 1 (hkb)") {
		t.Errorf("the summary must reconcile filled against already-captured; got:\n%s", got)
	}
}

func TestRunArchiveSources_CatchUpOnACompleteDayFetchesNothingAndSucceeds(t *testing.T) {
	var hkbCalls, projCalls int
	store := newMemStore("hkb/dt=2026-06-30", "projections/dt=2026-06-30")
	var out strings.Builder

	err := runArchiveSources(context.Background(),
		[]archive.Source{countingSource("hkb", &hkbCalls), countingSource("projections", &projCalls)},
		archive.NewWriter(store), archiveTestDate, archiveRun{catchUp: true}, &out)
	if err != nil {
		t.Fatalf("a day that is already complete is not a failure: %v", err)
	}
	if hkbCalls+projCalls != 0 {
		t.Errorf("a complete day must cost zero upstream fetches; got %d", hkbCalls+projCalls)
	}
	if !strings.Contains(out.String(), "archive (catch-up): dt=2026-06-30 already complete; all 2 sources captured") {
		t.Errorf("an idle catch-up must still say what it found, so silence never has to be interpreted; got:\n%s", out.String())
	}
}

// The exit code is the ledger's only summary of the day, so a catch-up that
// could not close the hole must stay FAILED. A SUCCESS record from a run that
// captured nothing is the exact trap the positive-control work closed
// (rosterbot-kvgw).
func TestRunArchiveSources_CatchUpStillFailsWhileTheHoleRemains(t *testing.T) {
	store := newMemStore("hkb/dt=2026-06-30")
	var out strings.Builder

	err := runArchiveSources(context.Background(),
		[]archive.Source{okSource("hkb", 1), badSource("projections")},
		archive.NewWriter(store), archiveTestDate, archiveRun{catchUp: true}, &out)
	if err == nil {
		t.Fatal("a day still missing a source must fail the run, however many times it has been attempted")
	}
	got := out.String()
	if !strings.Contains(got, "archive: LOST source projections: boom") {
		t.Errorf("the loss must use the same line a log filter already matches; got:\n%s", got)
	}
	if !strings.Contains(got, "lost 1 (projections)") {
		t.Errorf("the summary must still name the loss; got:\n%s", got)
	}
}

// A store read that fails tells us nothing about the day. Treating "I could
// not look" as "already captured" would silently convert every catch-up fire
// into a no-op during an S3 incident, which is the one time they matter most.
func TestRunArchiveSources_CatchUpFetchesWhenItCannotReadTheStore(t *testing.T) {
	var calls int
	store := newMemStore("hkb/dt=2026-06-30")
	store.hasErr = errors.New("list denied")
	var out strings.Builder

	err := runArchiveSources(context.Background(),
		[]archive.Source{countingSource("hkb", &calls)},
		archive.NewWriter(store), archiveTestDate, archiveRun{catchUp: true}, &out)
	if err != nil {
		t.Fatalf("an unreadable store must not fail a run that then captured the source anyway: %v", err)
	}
	if calls != 1 {
		t.Errorf("hkb was fetched %d times, want 1: an unreadable partition must be fetched, not assumed present", calls)
	}
	if !strings.Contains(out.String(), "archive: cannot tell whether hkb is captured (list denied); fetching it") {
		t.Errorf("the degraded read must be named, so a day of needless re-fetching is explicable; got:\n%s", out.String())
	}
}

// --dry-run --catch-up is a human diagnostic (the schedule never passes both),
// and it used to print a line that contradicted itself: the "archive
// (dry-run)" label with the catch-up body, whose "already captured" clause
// only means anything for a catch-up. The two catch-up exits also disagreed --
// the already-complete line said "(catch-up)" under the very same flags. A
// summary is the job's positive control, so a label that misnames the mode it
// describes is the one thing it cannot do.
func TestRunArchiveSources_CatchUpDryRunNamesBothModes(t *testing.T) {
	store := newMemStore("hkb/dt=2026-06-30")
	var out strings.Builder

	err := runArchiveSources(context.Background(),
		[]archive.Source{okSource("hkb", 1), okSource("projections", 8)},
		archive.NewWriter(store), archiveTestDate, archiveRun{dryRun: true, catchUp: true}, &out)
	if err != nil {
		t.Fatalf("dry-run catch-up: %v", err)
	}
	// "fetched", not "filled": a dry run wrote nothing, and the verb has to
	// keep saying so even in the catch-up shape.
	want := "archive (catch-up, dry-run): dt=2026-06-30 fetched 1/1 attempted (projections); already captured 1 (hkb)"
	if !strings.Contains(out.String(), want) {
		t.Errorf("summary must name both modes;\n got:\n%s\nwant to contain:\n%s", out.String(), want)
	}
	if store.captured["projections/dt=2026-06-30"] {
		t.Error("a dry-run catch-up must still write nothing")
	}
}

func TestRunArchiveSources_CatchUpDryRunOnACompleteDayUsesTheSameLabel(t *testing.T) {
	store := newMemStore("hkb/dt=2026-06-30", "projections/dt=2026-06-30")
	var out strings.Builder

	if err := runArchiveSources(context.Background(),
		[]archive.Source{okSource("hkb", 1), okSource("projections", 8)},
		archive.NewWriter(store), archiveTestDate, archiveRun{dryRun: true, catchUp: true}, &out); err != nil {
		t.Fatalf("dry-run catch-up on a complete day: %v", err)
	}
	want := "archive (catch-up, dry-run): dt=2026-06-30 already complete; all 2 sources captured"
	if !strings.Contains(out.String(), want) {
		t.Errorf("both catch-up exits must carry the same label;\n got:\n%s\nwant to contain:\n%s", out.String(), want)
	}
}

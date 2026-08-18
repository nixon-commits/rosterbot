package s3backtest

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func newStore(f *s3blobtest.Fake) *Store { return &Store{blob: f.Blob("statebkt", "backtest/")} }

// The key layout is the contract this store shares with
// backtest.FileSnapshotStore's local tree. Both the flat optimize partition and
// the per-system shadow partition have to round-trip, because grade reads the
// second one for every system it compares.
func TestPutGet_RoundTripsBothPartitionLayouts(t *testing.T) {
	for _, key := range []string{
		"snapshots/2026-08-18.json",
		"snapshots-systems/system=atc-ros/2026-08-18.json",
	} {
		f := s3blobtest.New()
		s := newStore(f)
		body := []byte(`{"date":"2026-08-18"}`)
		if err := s.Put(key, body); err != nil {
			t.Fatalf("%s: put: %v", key, err)
		}
		got, found, err := s.Get(key)
		if err != nil || !found {
			t.Fatalf("%s: get: found=%v err=%v", key, found, err)
		}
		if string(got) != string(body) {
			t.Fatalf("%s: got %q, want %q", key, got, body)
		}
		if keys := f.Keys(); len(keys) != 1 || keys[0] != "backtest/"+key {
			t.Fatalf("%s: stored under %v, want [backtest/%s]", key, keys, key)
		}
	}
}

// A missing snapshot is an ordinary outcome, not an error: it is exactly what
// RunProjectionAnalysis records as the "missing" grading source for a day the
// producer never ran. Returning an error here would turn every un-run day into
// a failure.
func TestGet_MissingSnapshotIsNotAnError(t *testing.T) {
	b, found, err := newStore(s3blobtest.New()).Get("snapshots/2020-01-01.json")
	if err != nil {
		t.Fatalf("missing snapshot returned an error: %v", err)
	}
	if found || b != nil {
		t.Fatalf("found=%v bytes=%q, want a clean miss", found, b)
	}
}

// The hourly lineup job re-optimizes today repeatedly and the last write is the
// one graded, so Put must overwrite rather than refuse or append.
func TestPut_OverwritesTheSameDateLastWriteWins(t *testing.T) {
	s := newStore(s3blobtest.New())
	key := "snapshots/2026-08-18.json"
	if err := s.Put(key, []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(key, []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"n":2}` {
		t.Fatalf("got %q, want the second write", got)
	}
}

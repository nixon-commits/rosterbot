package s3archive

import (
	"sort"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

func newStore(f *s3blobtest.Fake) *Store { return &Store{blob: f.Blob("statebkt", "archive/")} }

// The key layout is the contract this store shares with archive.FileStore's
// local tree: <prefix><source>/dt=YYYY-MM-DD/<filename>. If it drifts, the
// Infra page's dt= partition scan and every downstream reader stop agreeing
// with what the writer produced.
func TestWritePartition_LaysKeysOutUnderTheDatedPartition(t *testing.T) {
	f := s3blobtest.New()
	err := newStore(f).WritePartition("hkb/dt=2026-08-18", []archive.Artifact{
		{Filename: "rankings.html", Bytes: []byte("<html>")},
		{Filename: "meta.json", Bytes: []byte(`{"ok":true}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Keys()
	sort.Strings(got)
	want := []string{
		"archive/hkb/dt=2026-08-18/meta.json",
		"archive/hkb/dt=2026-08-18/rankings.html",
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

// Bytes must land verbatim. These are raw upstream captures whose whole value
// is being faithful — a transformed archive is worse than no archive, because
// it looks authoritative.
func TestWritePartition_StoresBytesVerbatim(t *testing.T) {
	f := s3blobtest.New()
	body := []byte("player,xwoba\nSoto,0.412\n")
	if err := newStore(f).WritePartition("savant/dt=2026-08-18", []archive.Artifact{
		{Filename: "expected_stats.csv", Bytes: body},
	}); err != nil {
		t.Fatal(err)
	}
	got, found, err := f.Blob("statebkt", "archive/").Get(t.Context(), "savant/dt=2026-08-18/expected_stats.csv")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if string(got) != string(body) {
		t.Fatalf("stored %q, want %q", got, body)
	}
}

// An empty artifact list is a no-op rather than an error: a source that
// legitimately has nothing for a date should not fail the whole run, and
// runArchiveSources already treats a returned error as a failed source.
func TestWritePartition_EmptyArtifactListWritesNothing(t *testing.T) {
	f := s3blobtest.New()
	if err := newStore(f).WritePartition("prospects/dt=2026-08-18", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.Keys()) != 0 {
		t.Fatalf("wrote %v, want nothing", f.Keys())
	}
}

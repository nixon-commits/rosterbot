package dynasty

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

func TestRow_ValueFor(t *testing.T) {
	r := Row{ValueSFDynasty: 100, ValueNonSFDynasty: 200, ValueSFRedraft: 300, ValueNonSFRedraft: 400}
	if got := r.ValueFor("sf_dynasty"); got != 100 {
		t.Errorf("ValueFor(sf_dynasty) = %d, want 100", got)
	}
	if got := r.ValueFor("non_sf_redraft"); got != 400 {
		t.Errorf("ValueFor(non_sf_redraft) = %d, want 400", got)
	}
	if got := r.ValueFor("unknown"); got != 0 {
		t.Errorf("ValueFor(unknown) = %d, want 0", got)
	}
}

func TestObjectKey(t *testing.T) {
	d := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	if got, want := ObjectKey(d), "dt=2026-08-11/values.ndjson"; got != want {
		t.Errorf("ObjectKey = %q, want %q", got, want)
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	store := ndjsonstore.NewMemStore()
	w := NewWriter(store)

	d := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	rows := []Row{
		{
			Dt: "2026-08-11", TeamID: "1", TeamName: "Palm Trees", AssetType: "player",
			AssetID: "9509", Name: "Bijan Robinson", Position: "RB", IsStarter: true,
			ValueSFDynasty: 10000, ValueNonSFDynasty: 10000, ValueSFRedraft: 9772, ValueNonSFRedraft: 9996,
		},
		{
			Dt: "2026-08-11", TeamID: "1", TeamName: "Palm Trees", AssetType: "pick",
			AssetID: "pick:2027:1", Name: "2027 Round 1 Pick", Estimated: true,
			ValueSFDynasty: 3748, ValueNonSFDynasty: 3783,
		},
	}
	if err := w.WriteValues(d, rows); err != nil {
		t.Fatalf("WriteValues: %v", err)
	}

	got, err := NewReader(store).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].AssetType != "player" || got[0].IsStarter != true || got[0].ValueSFDynasty != 10000 {
		t.Errorf("got[0] = %+v, round-trip mismatch", got[0])
	}
	if got[1].AssetType != "pick" || got[1].Estimated != true {
		t.Errorf("got[1] = %+v, round-trip mismatch", got[1])
	}
}

func TestReadAll_EmptyStoreIsNotAnError(t *testing.T) {
	rows, err := NewReader(ndjsonstore.NewMemStore()).ReadAll()
	if err != nil {
		t.Fatalf("empty store must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows from an empty store, want 0", len(rows))
	}
}

func TestWriteCoverageThenReadAllCoverage_RoundTrip(t *testing.T) {
	store := ndjsonstore.NewMemStore()
	w := NewWriter(store)

	d1 := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if err := w.WriteCoverage(d1, []Coverage{
		{Dt: "2026-08-11", TeamID: "1", TeamName: "A", RosteredCount: 35, MatchedCount: 33, Unmatched: []string{"X"}},
	}); err != nil {
		t.Fatalf("WriteCoverage d1: %v", err)
	}
	if err := w.WriteCoverage(d2, []Coverage{
		{Dt: "2026-08-12", TeamID: "1", TeamName: "A", RosteredCount: 36, MatchedCount: 34},
	}); err != nil {
		t.Fatalf("WriteCoverage d2: %v", err)
	}

	got, err := NewReader(store).ReadAllCoverage()
	if err != nil {
		t.Fatalf("ReadAllCoverage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	// dt= partitions sort lexically, which is chronological.
	if got[0].Dt != "2026-08-11" || got[1].Dt != "2026-08-12" {
		t.Errorf("coverage out of chronological order: %q, %q", got[0].Dt, got[1].Dt)
	}
	if got[0].RosteredCount != 35 || got[0].MatchedCount != 33 {
		t.Errorf("got[0] = %+v, round-trip mismatch", got[0])
	}
	// Unmatched MUST round-trip: it is the only answer the dashboard's Matched
	// column can give to "which players", and BuildModel reads the store rather
	// than the live join, so a name dropped here is a name nobody can recover.
	if len(got[0].Unmatched) != 1 || got[0].Unmatched[0] != "X" {
		t.Errorf("Unmatched = %v, want [X] after round-trip", got[0].Unmatched)
	}
	// A team that matched everyone stays empty, so an empty list is only
	// meaningful read against the counts -- see Coverage.Unmatched's comment.
	if len(got[1].Unmatched) != 0 {
		t.Errorf("Unmatched = %v on a fully-matched team, want empty", got[1].Unmatched)
	}
}

package lineupgap

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

func TestRow_Efficiency(t *testing.T) {
	r := Row{ActualPts: 90, OptimalPts: 100}
	if got := r.Efficiency(); got != 0.9 {
		t.Errorf("Efficiency = %v, want 0.9", got)
	}
	// A day where the hindsight-optimal lineup scored nothing (no games) must
	// not divide by zero.
	if got := (Row{ActualPts: 0, OptimalPts: 0}).Efficiency(); got != 0 {
		t.Errorf("Efficiency with zero optimal = %v, want 0", got)
	}
}

func TestObjectKey(t *testing.T) {
	d := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if got, want := ObjectKey(d), "dt=2026-07-20/gaps.ndjson"; got != want {
		t.Errorf("ObjectKey = %q, want %q", got, want)
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	store := ndjsonstore.NewMemStore()
	w := NewWriter(store)

	d1 := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGaps(d1, []Row{{Dt: "2026-07-20", ActualPts: 90, OptimalPts: 100, Gap: -10, StartedN: 13, BenchedN: 2}}); err != nil {
		t.Fatalf("write d1: %v", err)
	}
	if err := w.WriteGaps(d2, []Row{{Dt: "2026-07-21", ActualPts: 80, OptimalPts: 80, Gap: 0, StartedN: 13, BenchedN: 0}}); err != nil {
		t.Fatalf("write d2: %v", err)
	}

	rows, err := NewReader(store).ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// dt= partitions sort lexically, which is chronological.
	if rows[0].Dt != "2026-07-20" || rows[1].Dt != "2026-07-21" {
		t.Errorf("rows out of chronological order: %q, %q", rows[0].Dt, rows[1].Dt)
	}
	if rows[0].Gap != -10 || rows[0].StartedN != 13 || rows[0].BenchedN != 2 {
		t.Errorf("round-trip lost fields: %+v", rows[0])
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

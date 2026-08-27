// Package runstoretest is the shared conformance suite for lineupapi.RunStore
// implementations, on the same reasoning as usertest and identitytest: the
// contract is not expressible in the method signatures, so a store that
// ignores it still compiles and still satisfies the interface.
//
// The property that forced this suite into existence is the lookback window.
// Before it, FileRunStore.Get scanned every ledger record while the S3
// adapter bounded its scan to the newest window — so a run older than the
// window resolved in local `serve` and 404'd in production, a divergence no
// per-adapter test could see. Get's window is now part of the interface
// (lineupapi.RunLookback) and this suite holds both adapters to it from both
// sides: a run inside the window must resolve, a run pushed past it must
// read as not-found.
package runstoretest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// RunWriter seeds the ledger the store under test reads. Both adapters' own
// PutRun methods satisfy it.
type RunWriter interface {
	PutRun(ctx context.Context, rec lineupapi.RunDetail) error
}

// Run exercises the whole RunStore contract. newStore must return a
// freshly-empty store (and its writer) on every call; subtests do not share
// state.
func Run(t *testing.T, newStore func(t *testing.T) (lineupapi.RunStore, RunWriter)) {
	t.Helper()
	ctx := context.Background()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rec := func(id string, at time.Time) lineupapi.RunDetail {
		return lineupapi.RunDetail{
			Run: lineupapi.Run{
				ID:        id,
				Command:   "optimize",
				Status:    "SUCCESS",
				StartedAt: at.Format(time.RFC3339),
				Trigger:   "schedule",
			},
			LogTail: "tail of " + id,
		}
	}

	t.Run("UnknownRunIsNotAnError", func(t *testing.T) {
		s, _ := newStore(t)
		d, ok, err := s.Get(ctx, "nobody")
		if err != nil || ok || d != nil {
			t.Fatalf("Get(unknown) = (%v, %v, %v), want (nil, false, nil)", d, ok, err)
		}
	})

	t.Run("RoundTripCarriesTheDetail", func(t *testing.T) {
		s, w := newStore(t)
		if err := w.PutRun(ctx, rec("run-1", base)); err != nil {
			t.Fatalf("PutRun: %v", err)
		}
		d, ok, err := s.Get(ctx, "run-1")
		if err != nil || !ok {
			t.Fatalf("Get = (_, %v, %v), want found", ok, err)
		}
		if d.ID != "run-1" || d.Command != "optimize" || d.LogTail != "tail of run-1" {
			t.Fatalf("Get returned %+v, want the stored record", d)
		}
		runs, err := s.List(ctx, 10)
		if err != nil || len(runs) != 1 || runs[0].ID != "run-1" {
			t.Fatalf("List = (%v, %v), want the one stored run", runs, err)
		}
	})

	t.Run("ListIsNewestFirstAndHonorsLimit", func(t *testing.T) {
		s, w := newStore(t)
		for i, id := range []string{"old", "mid", "new"} {
			if err := w.PutRun(ctx, rec(id, base.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatalf("PutRun(%s): %v", id, err)
			}
		}
		runs, err := s.List(ctx, 2)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(runs) != 2 || runs[0].ID != "new" || runs[1].ID != "mid" {
			t.Fatalf("List(2) = %v, want [new mid]", runs)
		}
	})

	t.Run("GetResolvesOnlyTheLookbackWindow", func(t *testing.T) {
		s, w := newStore(t)
		// RunLookback+1 records: the oldest is one past the window, the
		// second-oldest sits exactly on its edge.
		for i := 0; i <= lineupapi.RunLookback; i++ {
			if err := w.PutRun(ctx, rec(fmt.Sprintf("run-%03d", i), base.Add(time.Duration(i)*time.Minute))); err != nil {
				t.Fatalf("PutRun(%d): %v", i, err)
			}
		}
		if _, ok, err := s.Get(ctx, "run-000"); err != nil || ok {
			t.Fatalf("Get(one past the window) = (_, %v, %v), want not-found — the window is the contract, on every adapter", ok, err)
		}
		if _, ok, err := s.Get(ctx, "run-001"); err != nil || !ok {
			t.Fatalf("Get(edge of the window) = (_, %v, %v), want found", ok, err)
		}
		if _, ok, err := s.Get(ctx, fmt.Sprintf("run-%03d", lineupapi.RunLookback)); err != nil || !ok {
			t.Fatalf("Get(newest) = (_, %v, %v), want found", ok, err)
		}
	})
}

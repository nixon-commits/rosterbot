// Package progresstest is the shared conformance suite for
// lineupapi.ProgressStore implementations — the live run-progress snapshot
// behind GET /v1/runs/{id}/progress. The contract mirrors outputtest's
// (byte-fidelity, absence-is-not-an-error, last-write-wins); the two suites
// stay separate because the stores are separate seams that can drift
// independently, which is the whole reason contract suites exist here.
package progresstest

import (
	"bytes"
	"context"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Store is the read+write pair under test.
type Store interface {
	lineupapi.ProgressStore
	lineupapi.ProgressWriter
}

// Run exercises the ProgressStore contract. newStore must return a
// freshly-empty store on every call.
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("MissingRunIsNotAnError", func(t *testing.T) {
		s := newStore(t)
		data, ok, err := s.GetProgress(ctx, "nobody")
		if err != nil || ok || data != nil {
			t.Fatalf("GetProgress(missing) = (%v, %v, %v), want (nil, false, nil)", data, ok, err)
		}
	})

	t.Run("RoundTripIsByteExact", func(t *testing.T) {
		s := newStore(t)
		payload := []byte(`{"phase":"Optimize","pct":62}`)
		if err := s.PutProgress(ctx, "run-1", payload); err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		data, ok, err := s.GetProgress(ctx, "run-1")
		if err != nil || !ok || !bytes.Equal(data, payload) {
			t.Fatalf("GetProgress = (%q, %v, %v), want the stored bytes", data, ok, err)
		}
	})

	t.Run("OverwriteIsLastWriteWins", func(t *testing.T) {
		// Progress is overwritten on every phase transition; the newest
		// snapshot must always win or the live hero renders a stale phase.
		s := newStore(t)
		if err := s.PutProgress(ctx, "run-1", []byte(`{"pct":10}`)); err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		if err := s.PutProgress(ctx, "run-1", []byte(`{"pct":90}`)); err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		data, ok, err := s.GetProgress(ctx, "run-1")
		if err != nil || !ok || string(data) != `{"pct":90}` {
			t.Fatalf("GetProgress = (%q, %v, %v), want the second write", data, ok, err)
		}
	})

	t.Run("RunsAreIsolated", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutProgress(ctx, "run-1", []byte("one")); err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		if err := s.PutProgress(ctx, "run-2", []byte("two")); err != nil {
			t.Fatalf("PutProgress: %v", err)
		}
		data, ok, err := s.GetProgress(ctx, "run-1")
		if err != nil || !ok || string(data) != "one" {
			t.Fatalf("GetProgress(run-1) = (%q, %v, %v), want run-1's own bytes", data, ok, err)
		}
	})
}

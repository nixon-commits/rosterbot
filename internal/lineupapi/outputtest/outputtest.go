// Package outputtest is the shared conformance suite for
// lineupapi.OutputStore implementations — the per-run captured-output bytes
// behind GET /v1/runs/{id}/output. Small on purpose: the contract is
// byte-fidelity, absence-is-not-an-error, and last-write-wins, and both
// adapters must answer identically.
package outputtest

import (
	"bytes"
	"context"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Store is the read+write pair under test.
type Store interface {
	lineupapi.OutputStore
	lineupapi.OutputWriter
}

// Run exercises the OutputStore contract. newStore must return a
// freshly-empty store on every call.
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("MissingRunIsNotAnError", func(t *testing.T) {
		s := newStore(t)
		data, ok, err := s.GetOutput(ctx, "nobody")
		if err != nil || ok || data != nil {
			t.Fatalf("GetOutput(missing) = (%v, %v, %v), want (nil, false, nil)", data, ok, err)
		}
	})

	t.Run("RoundTripIsByteExact", func(t *testing.T) {
		s := newStore(t)
		payload := []byte(`{"note":"multibyte García ✓"}`)
		if err := s.PutOutput(ctx, "run-1", payload); err != nil {
			t.Fatalf("PutOutput: %v", err)
		}
		data, ok, err := s.GetOutput(ctx, "run-1")
		if err != nil || !ok || !bytes.Equal(data, payload) {
			t.Fatalf("GetOutput = (%q, %v, %v), want the stored bytes", data, ok, err)
		}
	})

	t.Run("OverwriteIsLastWriteWins", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutOutput(ctx, "run-1", []byte("first")); err != nil {
			t.Fatalf("PutOutput: %v", err)
		}
		if err := s.PutOutput(ctx, "run-1", []byte("second")); err != nil {
			t.Fatalf("PutOutput: %v", err)
		}
		data, ok, err := s.GetOutput(ctx, "run-1")
		if err != nil || !ok || string(data) != "second" {
			t.Fatalf("GetOutput = (%q, %v, %v), want the second write", data, ok, err)
		}
	})

	t.Run("RunsAreIsolated", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutOutput(ctx, "run-1", []byte("one")); err != nil {
			t.Fatalf("PutOutput: %v", err)
		}
		if err := s.PutOutput(ctx, "run-2", []byte("two")); err != nil {
			t.Fatalf("PutOutput: %v", err)
		}
		data, ok, err := s.GetOutput(ctx, "run-1")
		if err != nil || !ok || string(data) != "one" {
			t.Fatalf("GetOutput(run-1) = (%q, %v, %v), want run-1's own bytes", data, ok, err)
		}
	})
}

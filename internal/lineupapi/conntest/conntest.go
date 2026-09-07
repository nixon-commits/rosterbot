// Package conntest is the shared conformance suite for
// lineupapi.ConnectionStore implementations — the ConnectionStore twin of
// identitytest and usertest, and it exists for the reason identitytest gives:
// the optimistic-concurrency version rides on the FantraxConnection struct
// rather than the method signature, so an implementation that ignores Version
// entirely still satisfies the interface and still compiles. Nothing but this
// suite stops a store from silently going back to the blind last-writer-wins
// overwrite rosterbot-wm9g closed.
//
// The suite never constructs a Version itself. It reads them off Get/Put
// results and hands them back unexamined, which is the whole of what a caller
// is allowed to do with an opaque token — a suite that compared versions to
// literals would be asserting a backend's encoding, not the contract.
package conntest

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Run exercises the whole ConnectionStore contract. newStore must return a
// freshly-empty store on every call — the subtests do not share state.
func Run(t *testing.T, newStore func(t *testing.T) lineupapi.ConnectionStore) {
	t.Helper()
	ctx := context.Background()

	const uid lineupapi.UserID = "alice"

	fresh := func() *lineupapi.FantraxConnection {
		return &lineupapi.FantraxConnection{
			UserID:          uid,
			TeamID:          "team-7",
			Status:          lineupapi.ConnPending,
			CredsCiphertext: []byte("sealed:first"),
		}
	}

	// seed creates the record and returns the store plus the record exactly as
	// PutConnection left it, Version included.
	seed := func(t *testing.T) (lineupapi.ConnectionStore, *lineupapi.FantraxConnection) {
		t.Helper()
		s := newStore(t)
		c := fresh()
		if err := s.PutConnection(ctx, c); err != nil {
			t.Fatalf("seeding PutConnection: %v", err)
		}
		return s, c
	}

	get := func(t *testing.T, s lineupapi.ConnectionStore) *lineupapi.FantraxConnection {
		t.Helper()
		got, ok, err := s.GetConnection(ctx, uid)
		if err != nil || !ok {
			t.Fatalf("GetConnection: ok=%v err=%v", ok, err)
		}
		return got
	}

	t.Run("EmptyStoreReportsNotFound", func(t *testing.T) {
		s := newStore(t)
		c, ok, err := s.GetConnection(ctx, uid)
		if err != nil || ok || c != nil {
			t.Fatalf("GetConnection on an empty store = (%v, %v, %v), want (nil, false, nil)", c, ok, err)
		}
	})

	t.Run("CreateFromEmptyVersionAdvancesIt", func(t *testing.T) {
		_, c := seed(t)
		if c.Version == "" {
			t.Fatal("PutConnection with an empty Version succeeded but did not advance it; " +
				"the caller cannot write again without a re-read, and a second create would " +
				"be refused")
		}
	})

	t.Run("RoundTripReportsTheVersionTheWriteAdvancedTo", func(t *testing.T) {
		s, want := seed(t)
		got := get(t, s)
		if got.Version != want.Version {
			t.Fatalf("GetConnection.Version = %q, want %q (the value PutConnection advanced to)", got.Version, want.Version)
		}
		if got.Version == "" || got.Version == lineupapi.ConnUnversioned {
			t.Fatalf("a record this suite just wrote reads back as %q; a fresh write must be versioned", got.Version)
		}
		if got.UserID != want.UserID || got.TeamID != want.TeamID || got.Status != want.Status ||
			string(got.CredsCiphertext) != string(want.CredsCiphertext) {
			t.Fatalf("record = %+v, want the fields that were written", *got)
		}
	})

	t.Run("CreateOverAnExistingRecordIsRefusedAndChangesNothing", func(t *testing.T) {
		s, before := seed(t)
		dup := fresh()
		dup.CredsCiphertext = []byte("sealed:blind-overwrite")
		if err := s.PutConnection(ctx, dup); !errors.Is(err, lineupapi.ErrConnectionConflict) {
			t.Fatalf("PutConnection with an empty Version over an existing record = %v, want "+
				"ErrConnectionConflict — an empty version asserts that no record exists, and "+
				"this is exactly the blind overwrite rosterbot-wm9g closed", err)
		}
		got := get(t, s)
		if string(got.CredsCiphertext) != string(before.CredsCiphertext) || got.Version != before.Version {
			t.Fatalf("the refused write landed anyway: record = %+v", *got)
		}
	})

	t.Run("UpdateAtTheCurrentVersionSucceedsAndAdvances", func(t *testing.T) {
		s, c := seed(t)
		v1 := c.Version
		c.Status = lineupapi.ConnVerified
		c.FXRMCiphertext = []byte("sealed:cookie")
		if err := s.PutConnection(ctx, c); err != nil {
			t.Fatalf("PutConnection at the current version: %v", err)
		}
		if c.Version == v1 {
			t.Fatalf("Version stayed %q across a successful write; the next write from this "+
				"caller would then be refused as stale", v1)
		}
		got := get(t, s)
		if got.Version != c.Version {
			t.Fatalf("GetConnection.Version = %q, want %q", got.Version, c.Version)
		}
		if got.Status != lineupapi.ConnVerified || string(got.FXRMCiphertext) != "sealed:cookie" {
			t.Fatalf("record = %+v, want the updated fields", *got)
		}
	})

	t.Run("UpdateAtAStaleVersionIsRefusedAndChangesNothing", func(t *testing.T) {
		s, c := seed(t)
		stale := *c // a second reader's copy, taken at the same version

		c.Status = lineupapi.ConnVerified
		c.CredsCiphertext = []byte("sealed:second")
		if err := s.PutConnection(ctx, c); err != nil {
			t.Fatalf("first writer's PutConnection: %v", err)
		}

		// THE BEAD'S SCENARIO: the second reader is a session ladder that read
		// the record before a reconnect replaced the credentials, and now
		// writes its verdict about the OLD ones.
		stale.Status = lineupapi.ConnNeedsReconnect
		stale.LastError = lineupapi.ConnErrBadCredentials
		if err := s.PutConnection(ctx, &stale); !errors.Is(err, lineupapi.ErrConnectionConflict) {
			t.Fatalf("PutConnection at a superseded version = %v, want ErrConnectionConflict — "+
				"the fresh credentials would otherwise be replaced by a verdict on the old ones", err)
		}
		got := get(t, s)
		if got.Status != lineupapi.ConnVerified || string(got.CredsCiphertext) != "sealed:second" {
			t.Fatalf("the stale write clobbered the record: %+v", *got)
		}
		if got.Version != c.Version {
			t.Fatalf("Version = %q after a refused write, want %q unchanged", got.Version, c.Version)
		}
	})

	t.Run("ReReadingAfterAConflictLetsTheWriterProceed", func(t *testing.T) {
		s, c := seed(t)
		other := *c
		other.TeamID = "team-8"
		if err := s.PutConnection(ctx, &other); err != nil {
			t.Fatalf("other writer's PutConnection: %v", err)
		}
		if err := s.PutConnection(ctx, c); !errors.Is(err, lineupapi.ErrConnectionConflict) {
			t.Fatalf("stale PutConnection = %v, want ErrConnectionConflict", err)
		}
		// The re-read carries the version that won; a write at it succeeds.
		cur := get(t, s)
		cur.Status = lineupapi.ConnVerified
		if err := s.PutConnection(ctx, cur); err != nil {
			t.Fatalf("PutConnection after a re-read: %v", err)
		}
		if got := get(t, s); got.TeamID != "team-8" || got.Status != lineupapi.ConnVerified {
			t.Fatalf("record = %+v, want the other writer's team AND this writer's status", *got)
		}
	})

	t.Run("ConsecutiveWritesFromOneCallerNeedNoReRead", func(t *testing.T) {
		s, c := seed(t)
		for i := 0; i < 3; i++ {
			c.LastError = ""
			if err := s.PutConnection(ctx, c); err != nil {
				t.Fatalf("write %d at the version the previous write advanced to: %v", i+1, err)
			}
		}
		if got := get(t, s); got.Version != c.Version {
			t.Fatalf("GetConnection.Version = %q, want %q", got.Version, c.Version)
		}
	})
}

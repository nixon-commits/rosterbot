// Package enrollmenttest is the shared conformance suite for
// lineupapi.EnrollmentStore, on the same reasoning as usertest and
// identitytest: the properties that matter are not expressible in the method
// signatures.
//
// Two in particular. Redemption must be ATOMIC — a store that reads, checks,
// then marks satisfies the interface perfectly and lets two concurrent
// redemptions of one recovery link both succeed. And expiry must be enforced in
// CODE — a store that leans on DynamoDB's TTL to have deleted the row satisfies
// the interface too, right up until TTL lags (documented at up to 48 hours) and
// an expired link still works.
package enrollmenttest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Run exercises the whole EnrollmentStore contract. newStore must return a
// freshly-empty store on every call.
func Run(t *testing.T, newStore func(t *testing.T) lineupapi.EnrollmentStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	live := func() lineupapi.Enrollment {
		return lineupapi.Enrollment{
			UserID: "alice", TeamID: "team-7", Email: "a@example.test",
			ExpiresAt: now.Add(24 * time.Hour),
		}
	}
	seed := func(t *testing.T, e lineupapi.Enrollment) (lineupapi.EnrollmentStore, string) {
		t.Helper()
		s := newStore(t)
		_, hash, err := lineupapi.MintEnrollmentToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateEnrollment(ctx, hash, e); err != nil {
			t.Fatalf("CreateEnrollment: %v", err)
		}
		return s, hash
	}

	t.Run("UnknownTokenIsInvalid", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.RedeemEnrollment(ctx, "nope", now); !errors.Is(err, lineupapi.ErrEnrollmentInvalid) {
			t.Fatalf("redeeming an unknown token = %v, want ErrEnrollmentInvalid", err)
		}
	})

	t.Run("RedeemReturnsTheAssertions", func(t *testing.T) {
		s, hash := seed(t, live())
		got, err := s.RedeemEnrollment(ctx, hash, now)
		if err != nil {
			t.Fatalf("RedeemEnrollment: %v", err)
		}
		if got.UserID != "alice" || got.TeamID != "team-7" || got.Email != "a@example.test" {
			t.Fatalf("redeemed = %+v, want the minted assertions back", got)
		}
	})

	t.Run("RedeemIsSingleUse", func(t *testing.T) {
		s, hash := seed(t, live())
		if _, err := s.RedeemEnrollment(ctx, hash, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RedeemEnrollment(ctx, hash, now); !errors.Is(err, lineupapi.ErrEnrollmentInvalid) {
			t.Fatalf("second redemption = %v, want ErrEnrollmentInvalid — a recovery "+
				"link usable twice is one an attacker can use after its owner already did", err)
		}
	})

	t.Run("ConcurrentRedemptionYieldsExactlyOneWinner", func(t *testing.T) {
		// The property a check-then-mark implementation fails while passing
		// every other subtest here.
		s, hash := seed(t, live())

		const n = 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		var wins int
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				if _, err := s.RedeemEnrollment(ctx, hash, now); err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("%d concurrent redemptions succeeded, want exactly 1 — redemption "+
				"must be atomic, not check-then-mark", wins)
		}
	})

	t.Run("ExpiryIsEnforcedInCode", func(t *testing.T) {
		// Deliberately NOT dependent on any storage-level TTL having fired.
		// DynamoDB's TTL deletion is asynchronous and documented to lag by up
		// to 48 hours, so a store that relies on the row being gone will serve
		// an expired link for as long as the lag lasts.
		e := live()
		e.ExpiresAt = now.Add(-time.Second)
		s, hash := seed(t, e)

		if _, err := s.RedeemEnrollment(ctx, hash, now); !errors.Is(err, lineupapi.ErrEnrollmentInvalid) {
			t.Fatalf("redeeming an expired link = %v, want ErrEnrollmentInvalid. The "+
				"row is still present -- expiry has to be checked, not assumed reaped", err)
		}
	})

	t.Run("GetDoesNotRedeem", func(t *testing.T) {
		s, hash := seed(t, live())
		got, ok, err := s.GetEnrollment(ctx, hash)
		if err != nil || !ok {
			t.Fatalf("GetEnrollment: ok=%v err=%v", ok, err)
		}
		if got.Redeemed() {
			t.Fatal("GetEnrollment reported the link as redeemed; reading an invite " +
				"to show what it is for must not spend it")
		}
		if _, err := s.RedeemEnrollment(ctx, hash, now); err != nil {
			t.Fatalf("redeeming after a read = %v, want nil", err)
		}
	})

	t.Run("GetOnUnknownTokenIsNotAnError", func(t *testing.T) {
		s := newStore(t)
		e, ok, err := s.GetEnrollment(ctx, "nope")
		if err != nil || ok {
			t.Fatalf("GetEnrollment(unknown) = (%+v, %v, %v), want (_, false, nil)", e, ok, err)
		}
	})

	t.Run("DuplicateHashIsRejected", func(t *testing.T) {
		s, hash := seed(t, live())
		if err := s.CreateEnrollment(ctx, hash, live()); !errors.Is(err, lineupapi.ErrUserConflict) {
			t.Fatalf("re-creating an existing hash = %v, want ErrUserConflict — silently "+
				"overwriting would reset a spent link back to unused", err)
		}
	})
}

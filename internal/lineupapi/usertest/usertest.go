// Package usertest is the shared conformance suite for lineupapi.UserStore
// implementations, on the same reasoning as identitytest and s3blobtest: the
// contract is not expressible in the method signatures, so a store that ignores
// it still compiles and still satisfies the interface.
//
// Three properties in particular are invisible to the type system and each has
// a concrete failure behind it:
//
//   - Optimistic concurrency on the profile. Ignore User.Version and you are
//     back to blind last-writer-wins, the bug rosterbot-crq.2 closed.
//   - One claimant per team. Let two users hold one Fantrax team and both
//     tenants' hourly jobs optimize and apply to the same roster, each reading
//     the other's changes as drift, fighting every hour, invisibly.
//   - Credentials as independent keys. Fold them back into the profile and a
//     concurrent registration and login contend again.
package usertest

import (
	"context"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func cred(id, pk string) webauthn.Credential {
	return webauthn.Credential{ID: []byte(id), PublicKey: []byte(pk)}
}

// Run exercises the whole UserStore contract. newStore must return a
// freshly-empty store on every call; subtests do not share state.
func Run(t *testing.T, newStore func(t *testing.T) lineupapi.UserStore) {
	t.Helper()
	ctx := context.Background()

	newUser := func(id lineupapi.UserID) *lineupapi.User {
		return &lineupapi.User{
			ID: id, DisplayName: string(id), Email: string(id) + "@example.test",
			Role: lineupapi.RoleMember, Status: lineupapi.UserActive,
		}
	}
	seed := func(t *testing.T) (lineupapi.UserStore, *lineupapi.User) {
		t.Helper()
		s := newStore(t)
		u := newUser("alice")
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("seeding CreateUser: %v", err)
		}
		return s, u
	}

	t.Run("UnknownUserIsNotAnError", func(t *testing.T) {
		s := newStore(t)
		u, ok, err := s.GetUser(ctx, "nobody")
		if err != nil || ok || u != nil {
			t.Fatalf("GetUser(unknown) = (%v, %v, %v), want (nil, false, nil)", u, ok, err)
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		s, want := seed(t)
		got, ok, err := s.GetUser(ctx, want.ID)
		if err != nil || !ok {
			t.Fatalf("GetUser: ok=%v err=%v", ok, err)
		}
		if got.DisplayName != want.DisplayName || got.Email != want.Email ||
			got.Role != want.Role || got.Status != want.Status {
			t.Fatalf("round trip lost fields: got %+v want %+v", got, want)
		}
		if got.AutoApply {
			t.Error("AutoApply must default false — a new tenant must not have the " +
				"bot writing to their roster until they opt in")
		}
	})

	t.Run("CreateIsConditionalOnAbsence", func(t *testing.T) {
		s, u := seed(t)
		again := newUser(u.ID)
		again.Email = "different@example.test"
		if err := s.CreateUser(ctx, again); !errors.Is(err, lineupapi.ErrUserConflict) {
			t.Fatalf("re-creating an existing user = %v, want ErrUserConflict", err)
		}
	})

	t.Run("EmailIsUnique", func(t *testing.T) {
		s, u := seed(t)
		other := newUser("bob")
		other.Email = u.Email
		if err := s.CreateUser(ctx, other); !errors.Is(err, lineupapi.ErrEmailTaken) {
			t.Fatalf("second user with same email = %v, want ErrEmailTaken", err)
		}
	})

	t.Run("GetReportsAVersion", func(t *testing.T) {
		s, u := seed(t)
		got, _, err := s.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Version == "" {
			t.Fatal("GetUser returned an empty Version; the empty version means " +
				"'no record exists' and a write carrying it is a create, so an " +
				"existing record reporting it would turn every update into a " +
				"failed create")
		}
	})

	t.Run("StaleVersionIsRejected", func(t *testing.T) {
		s, u := seed(t)
		first, _, _ := s.GetUser(ctx, u.ID)
		second, _, _ := s.GetUser(ctx, u.ID)

		first.DisplayName = "winner"
		if err := s.PutUser(ctx, first); err != nil {
			t.Fatalf("first write: %v", err)
		}
		second.DisplayName = "loser"
		if err := s.PutUser(ctx, second); !errors.Is(err, lineupapi.ErrUserConflict) {
			t.Fatalf("stale write = %v, want ErrUserConflict", err)
		}
		got, _, _ := s.GetUser(ctx, u.ID)
		if got.DisplayName != "winner" {
			t.Fatalf("DisplayName = %q, want %q — the losing write overwrote the "+
				"winner, which is the blind last-writer-wins bug this version exists "+
				"to prevent", got.DisplayName, "winner")
		}
	})

	t.Run("PutAdvancesTheVersionInPlace", func(t *testing.T) {
		s, u := seed(t)
		got, _, _ := s.GetUser(ctx, u.ID)
		before := got.Version
		got.DisplayName = "one"
		if err := s.PutUser(ctx, got); err != nil {
			t.Fatal(err)
		}
		if got.Version == before {
			t.Fatal("PutUser did not advance Version in place; the same value must be " +
				"writable again without a re-read")
		}
		got.DisplayName = "two"
		if err := s.PutUser(ctx, got); err != nil {
			t.Fatalf("second write with the advanced version: %v", err)
		}
	})

	t.Run("TeamHasExactlyOneClaimant", func(t *testing.T) {
		s, alice := seed(t)
		bob := newUser("bob")
		if err := s.CreateUser(ctx, bob); err != nil {
			t.Fatal(err)
		}
		if err := s.ClaimTeam(ctx, alice.ID, "team-7"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if err := s.ClaimTeam(ctx, bob.ID, "team-7"); !errors.Is(err, lineupapi.ErrTeamTaken) {
			t.Fatalf("second claim on the same team = %v, want ErrTeamTaken — two "+
				"tenants on one roster means both hourly jobs apply to it and fight", err)
		}
		got, _, _ := s.GetUser(ctx, alice.ID)
		if got.TeamID != "team-7" {
			t.Fatalf("TeamID = %q, want team-7", got.TeamID)
		}
	})

	t.Run("ReclaimingYourOwnTeamIsFine", func(t *testing.T) {
		s, alice := seed(t)
		if err := s.ClaimTeam(ctx, alice.ID, "team-7"); err != nil {
			t.Fatal(err)
		}
		if err := s.ClaimTeam(ctx, alice.ID, "team-7"); err != nil {
			t.Fatalf("re-claiming own team = %v, want nil — a reconnect must not "+
				"lock the user out of the team they already hold", err)
		}
	})

	t.Run("CredentialsRoundTrip", func(t *testing.T) {
		s, u := seed(t)
		if got, err := s.Credentials(ctx, u.ID); err != nil || len(got) != 0 {
			t.Fatalf("new user credentials = (%v, %v), want empty", got, err)
		}
		if err := s.PutCredential(ctx, u.ID, cred("c1", "pk1")); err != nil {
			t.Fatal(err)
		}
		if err := s.PutCredential(ctx, u.ID, cred("c2", "pk2")); err != nil {
			t.Fatal(err)
		}
		got, err := s.Credentials(ctx, u.ID)
		if err != nil || len(got) != 2 {
			t.Fatalf("Credentials = (%d, %v), want 2", len(got), err)
		}
	})

	t.Run("CredentialWritesDoNotContend", func(t *testing.T) {
		// The point of per-key credentials. Registering a new passkey while
		// another passkey's sign counter is being updated must not lose either
		// change — and must not require a retry, because the writes touch
		// different keys.
		s, u := seed(t)
		if err := s.PutCredential(ctx, u.ID, cred("c1", "pk1")); err != nil {
			t.Fatal(err)
		}
		updated := cred("c1", "pk1")
		updated.Authenticator.SignCount = 42

		if err := s.PutCredential(ctx, u.ID, updated); err != nil {
			t.Fatal(err)
		}
		if err := s.PutCredential(ctx, u.ID, cred("c2", "pk2")); err != nil {
			t.Fatal(err)
		}
		got, err := s.Credentials(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d credentials, want 2 — a registration and a sign-counter "+
				"update lost one another", len(got))
		}
		for _, c := range got {
			if string(c.ID) == "c1" && c.Authenticator.SignCount != 42 {
				t.Errorf("c1 SignCount = %d, want 42", c.Authenticator.SignCount)
			}
		}
	})

	t.Run("DeleteCredentialIsIdempotent", func(t *testing.T) {
		s, u := seed(t)
		if err := s.PutCredential(ctx, u.ID, cred("c1", "pk1")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteCredential(ctx, u.ID, []byte("c1")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteCredential(ctx, u.ID, []byte("c1")); err != nil {
			t.Fatalf("deleting an absent credential = %v, want nil", err)
		}
		if got, _ := s.Credentials(ctx, u.ID); len(got) != 0 {
			t.Fatalf("got %d credentials after delete, want 0", len(got))
		}
	})

	t.Run("UserByCredential", func(t *testing.T) {
		s, u := seed(t)
		if err := s.PutCredential(ctx, u.ID, cred("c1", "pk1")); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.UserByCredential(ctx, []byte("c1"))
		if err != nil || !ok || got != u.ID {
			t.Fatalf("UserByCredential = (%q, %v, %v), want (%q, true, nil)", got, ok, err, u.ID)
		}
		if _, ok, err := s.UserByCredential(ctx, []byte("nope")); err != nil || ok {
			t.Fatalf("unknown credential = (%v, %v), want (false, nil)", ok, err)
		}
	})

	t.Run("DeletedCredentialIsUnresolvable", func(t *testing.T) {
		s, u := seed(t)
		if err := s.PutCredential(ctx, u.ID, cred("c1", "pk1")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteCredential(ctx, u.ID, []byte("c1")); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.UserByCredential(ctx, []byte("c1")); ok {
			t.Fatal("a revoked credential still resolves to its user; revocation " +
				"must drop the lookup index too or the credential stays usable")
		}
	})

	t.Run("ListActiveExcludesParked", func(t *testing.T) {
		s, alice := seed(t)
		bob := newUser("bob")
		bob.Status = lineupapi.UserParked
		if err := s.CreateUser(ctx, bob); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListActive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != alice.ID {
			ids := make([]lineupapi.UserID, len(got))
			for i, u := range got {
				ids[i] = u.ID
			}
			t.Fatalf("ListActive = %v, want [%s] — a parked tenant must cost nothing "+
				"in the fan-out", ids, alice.ID)
		}
	})
}

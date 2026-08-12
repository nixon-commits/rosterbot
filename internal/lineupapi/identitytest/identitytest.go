// Package identitytest is the shared conformance suite for
// lineupapi.IdentityStore implementations. It exists as its own package (rather
// than a _test.go helper) for the same reason s3blobtest does: two independent
// implementations — lineupapi.FileIdentityStore and s3lineup.IdentityStore —
// have to agree on one contract, and neither can import the other's tests.
//
// It matters more here than fidelity usually does. The optimistic-concurrency
// version rides on the Identity struct rather than the method signature, so an
// implementation that ignores Version entirely still satisfies the
// IdentityStore interface and still compiles. Nothing but this suite stops a
// future store from silently going back to a blind last-writer-wins overwrite,
// which is the bug (rosterbot-crq.2) the version exists to close.
package identitytest

import (
	"context"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Run exercises the whole IdentityStore contract. newStore must return a
// freshly-empty store on every call — the subtests do not share state.
func Run(t *testing.T, newStore func(t *testing.T) lineupapi.IdentityStore) {
	t.Helper()

	ctx := context.Background()

	seed := func(t *testing.T) (lineupapi.IdentityStore, *lineupapi.Identity) {
		t.Helper()
		s := newStore(t)
		id := &lineupapi.Identity{
			WebAuthnUserID: []byte("handle-123"),
			Credentials:    []webauthn.Credential{{ID: []byte("cred-1"), PublicKey: []byte("pk")}},
		}
		if err := s.PutIdentity(ctx, id); err != nil {
			t.Fatalf("seeding PutIdentity: %v", err)
		}
		return s, id
	}

	t.Run("EmptyStoreReportsNotFound", func(t *testing.T) {
		s := newStore(t)
		id, ok, err := s.GetIdentity(ctx)
		if err != nil || ok || id != nil {
			t.Fatalf("GetIdentity on empty store = (%v, %v, %v), want (nil, false, nil)", id, ok, err)
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		s, want := seed(t)
		got, ok, err := s.GetIdentity(ctx)
		if err != nil || !ok {
			t.Fatalf("GetIdentity after Put: ok=%v err=%v", ok, err)
		}
		if string(got.WebAuthnUserID) != string(want.WebAuthnUserID) {
			t.Fatalf("WebAuthnUserID = %q, want %q", got.WebAuthnUserID, want.WebAuthnUserID)
		}
		if len(got.Credentials) != 1 || string(got.Credentials[0].ID) != "cred-1" {
			t.Fatalf("Credentials = %+v, want one credential cred-1", got.Credentials)
		}
	})

	// A read has to hand back the token a later write conditions on. Without
	// this there is nothing to compare and every write is unconditional.
	t.Run("GetReportsAVersion", func(t *testing.T) {
		s, _ := seed(t)
		got, _, err := s.GetIdentity(ctx)
		if err != nil {
			t.Fatalf("GetIdentity: %v", err)
		}
		if got.Version == "" {
			t.Fatal("GetIdentity returned an empty Version — an existing record must carry a non-empty version token")
		}
	})

	// The empty version means "I believe no record exists", so a second such
	// write must lose. This is the create race two concurrent register/begin
	// requests run: each mints its own random WebAuthnUserID, and a blind
	// overwrite invalidates the ceremony already running against the first.
	t.Run("CreateIsConditionalOnAbsence", func(t *testing.T) {
		s, _ := seed(t)
		second := &lineupapi.Identity{WebAuthnUserID: []byte("handle-456")}
		err := s.PutIdentity(ctx, second)
		if !errors.Is(err, lineupapi.ErrIdentityConflict) {
			t.Fatalf("PutIdentity with an empty Version over an existing record = %v, want ErrIdentityConflict", err)
		}
		got, _, err := s.GetIdentity(ctx)
		if err != nil {
			t.Fatalf("GetIdentity: %v", err)
		}
		if string(got.WebAuthnUserID) != "handle-123" {
			t.Fatalf("WebAuthnUserID = %q, want the original handle-123 — the losing create overwrote the record", got.WebAuthnUserID)
		}
	})

	// The core of the bug: two readers at the same version, both writing.
	t.Run("StaleVersionIsRejected", func(t *testing.T) {
		s, _ := seed(t)

		reader1, _, err := s.GetIdentity(ctx)
		if err != nil {
			t.Fatalf("GetIdentity: %v", err)
		}
		reader2, _, err := s.GetIdentity(ctx)
		if err != nil {
			t.Fatalf("GetIdentity: %v", err)
		}

		reader1.Credentials = append(reader1.Credentials, webauthn.Credential{ID: []byte("from-reader-1")})
		if err := s.PutIdentity(ctx, reader1); err != nil {
			t.Fatalf("first writer PutIdentity: %v", err)
		}

		reader2.Credentials = append(reader2.Credentials, webauthn.Credential{ID: []byte("from-reader-2")})
		if err := s.PutIdentity(ctx, reader2); !errors.Is(err, lineupapi.ErrIdentityConflict) {
			t.Fatalf("second writer PutIdentity = %v, want ErrIdentityConflict", err)
		}

		got, _, err := s.GetIdentity(ctx)
		if err != nil {
			t.Fatalf("GetIdentity: %v", err)
		}
		if len(got.Credentials) != 2 || string(got.Credentials[1].ID) != "from-reader-1" {
			t.Fatalf("stored credentials = %+v, want the first writer's change intact", got.Credentials)
		}
	})

	// A successful write advances the in-memory token, so the same *Identity
	// can be written again without a re-read. A store that leaves the stale
	// token behind turns every second write in one request into a conflict.
	t.Run("PutAdvancesTheVersionInPlace", func(t *testing.T) {
		s, _ := seed(t)
		id, _, err := s.GetIdentity(ctx)
		if err != nil {
			t.Fatalf("GetIdentity: %v", err)
		}
		before := id.Version

		id.Credentials = append(id.Credentials, webauthn.Credential{ID: []byte("cred-2")})
		if err := s.PutIdentity(ctx, id); err != nil {
			t.Fatalf("PutIdentity: %v", err)
		}
		if id.Version == before {
			t.Fatalf("Version still %q after a successful write — want it advanced", id.Version)
		}
		if id.Version == "" {
			t.Fatal("Version cleared after a successful write — the next write would be treated as a create")
		}

		id.Credentials = append(id.Credentials, webauthn.Credential{ID: []byte("cred-3")})
		if err := s.PutIdentity(ctx, id); err != nil {
			t.Fatalf("second PutIdentity against the advanced version: %v", err)
		}
	})

	// A record that vanished under a holder of its version is a conflict, not
	// a silent re-create: the version says "I expected something there".
	t.Run("VersionedWriteOverAnAbsentRecordConflicts", func(t *testing.T) {
		s := newStore(t)
		orphan := &lineupapi.Identity{
			WebAuthnUserID: []byte("handle-123"),
			Version:        lineupapi.IdentityVersion("some-version-from-a-record-that-is-gone"),
		}
		if err := s.PutIdentity(ctx, orphan); !errors.Is(err, lineupapi.ErrIdentityConflict) {
			t.Fatalf("PutIdentity with a version against an empty store = %v, want ErrIdentityConflict", err)
		}
	})
}

package s3lineup

import (
	"context"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/identitytest"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

// TestIdentityStore_StoresAtIdentityJSONKey pins the one claim not already
// covered by TestIdentityStore_Conformance's shared identitytest.Run suite
// (EmptyStoreReportsNotFound, RoundTrip): where in the bucket the record
// lands.
func TestIdentityStore_StoresAtIdentityJSONKey(t *testing.T) {
	f := s3blobtest.New()
	s := &IdentityStore{blob: f.Blob("b", "webauthn/")}

	want := &lineupapi.Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}
	if err := s.PutIdentity(context.Background(), want); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if _, stored := f.Objects["webauthn/identity.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", f.Keys())
	}
}

// TestIdentityStore_Conformance runs the same optimistic-concurrency contract
// the local FileIdentityStore is held to. Here the precondition is enforced by
// S3 itself (If-None-Match / If-Match), which is why the fake in s3blobtest
// has to evaluate those conditions rather than accept every write.
func TestIdentityStore_Conformance(t *testing.T) {
	identitytest.Run(t, func(t *testing.T) lineupapi.IdentityStore {
		return &IdentityStore{blob: s3blobtest.New().Blob("b", "webauthn/")}
	})
}

// TestIdentityStore_VersionIsNotPersistedInTheBody pins the json:"-" on
// Identity.Version. The version describes which stored bytes a value came
// from, so writing it into those bytes would make it self-referential — and
// the DynamoDB store this record is headed for would then find a stale JSON
// copy shadowing its real version attribute.
func TestIdentityStore_VersionIsNotPersistedInTheBody(t *testing.T) {
	f := s3blobtest.New()
	s := &IdentityStore{blob: f.Blob("b", "webauthn/")}

	id := &lineupapi.Identity{WebAuthnUserID: []byte("handle-123")}
	if err := s.PutIdentity(context.Background(), id); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	body := string(f.Objects["webauthn/identity.json"])
	for _, unwanted := range []string{"Version", "version", "etag", "ETag"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("stored body %s contains %q — the version must not be part of the durable record", body, unwanted)
		}
	}
}

package s3lineup

import (
	"context"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func TestIdentityStoreRoundTrip(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &IdentityStore{client: f, bucket: "b", prefix: "webauthn/"}

	if _, ok, err := s.GetIdentity(context.Background()); err != nil || ok {
		t.Fatalf("GetIdentity on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	want := &lineupapi.Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}
	if err := s.PutIdentity(context.Background(), want); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if _, stored := f.objects["webauthn/identity.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", keys(f.objects))
	}

	got, ok, err := s.GetIdentity(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetIdentity after Put: ok=%v err=%v", ok, err)
	}
	if string(got.WebAuthnUserID) != "handle-123" || len(got.Credentials) != 1 {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
}

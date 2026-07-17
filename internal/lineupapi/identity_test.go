package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestIdentityUserAdapter(t *testing.T) {
	id := &Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}
	u := identityUser{id: id}

	if string(u.WebAuthnID()) != "handle-123" {
		t.Fatalf("WebAuthnID() = %q, want %q", u.WebAuthnID(), "handle-123")
	}
	if len(u.WebAuthnCredentials()) != 1 || string(u.WebAuthnCredentials()[0].ID) != "cred-1" {
		t.Fatalf("WebAuthnCredentials() = %+v, want one credential with ID cred-1", u.WebAuthnCredentials())
	}
	if u.WebAuthnName() == "" || u.WebAuthnDisplayName() == "" {
		t.Fatal("WebAuthnName()/WebAuthnDisplayName() must not be empty — go-webauthn rejects empty names")
	}
}

func TestNewWebAuthnUserIDIsRandomAnd64Bytes(t *testing.T) {
	a, err := newWebAuthnUserID()
	if err != nil {
		t.Fatalf("newWebAuthnUserID: %v", err)
	}
	if len(a) != 64 {
		t.Fatalf("len = %d, want 64", len(a))
	}
	b, err := newWebAuthnUserID()
	if err != nil {
		t.Fatalf("newWebAuthnUserID: %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two calls returned the same handle — want random")
	}
}

func TestFileIdentityStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileIdentityStore(dir)

	if _, ok, err := s.GetIdentity(context.Background()); err != nil || ok {
		t.Fatalf("GetIdentity on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	want := &Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1"), PublicKey: []byte("pk")}},
	}
	if err := s.PutIdentity(context.Background(), want); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}

	got, ok, err := s.GetIdentity(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetIdentity after Put: ok=%v err=%v", ok, err)
	}
	if string(got.WebAuthnUserID) != "handle-123" || len(got.Credentials) != 1 || string(got.Credentials[0].ID) != "cred-1" {
		t.Fatalf("got = %+v, want %+v", got, want)
	}

	if _, err := os.Stat(filepath.Join(dir, "webauthn-identity.json")); err != nil {
		t.Fatalf("expected file at webauthn-identity.json: %v", err)
	}
}

var _ IdentityStore = (*FileIdentityStore)(nil)

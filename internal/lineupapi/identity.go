package lineupapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Identity is the dashboard's single WebAuthn identity: a stable random user
// handle plus every registered passkey. There is exactly one Identity for the
// whole dashboard — one operator, multiple device-bound credentials.
type Identity struct {
	WebAuthnUserID []byte                `json:"webauthn_user_id"`
	Credentials    []webauthn.Credential `json:"credentials"`
}

// IdentityStore is the read/write side for the Identity record. Unlike
// ObjectStore/Publisher (write-once-per-key, read-many), this store is
// read-modify-written on every passkey registration, login (sign-counter
// update), and revocation.
type IdentityStore interface {
	GetIdentity(ctx context.Context) (*Identity, bool, error)
	PutIdentity(ctx context.Context, id *Identity) error
}

// identityUser adapts *Identity to webauthn.User.
type identityUser struct {
	id *Identity
}

func (u identityUser) WebAuthnID() []byte                         { return u.id.WebAuthnUserID }
func (u identityUser) WebAuthnName() string                       { return "rosterbot" }
func (u identityUser) WebAuthnDisplayName() string                { return "rosterbot" }
func (u identityUser) WebAuthnCredentials() []webauthn.Credential { return u.id.Credentials }

// newWebAuthnUserID generates a stable random 64-byte user handle for a brand
// new Identity. Called once, the first time register/begin runs against an
// empty store (see Task 4's loadOrCreateIdentity).
func newWebAuthnUserID() ([]byte, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// FileIdentityStore is a local-filesystem IdentityStore, one JSON file at
// <dir>/webauthn-identity.json. Used by `rosterbot serve`.
type FileIdentityStore struct {
	dir string
}

func NewFileIdentityStore(dir string) *FileIdentityStore { return &FileIdentityStore{dir: dir} }

func (s *FileIdentityStore) path() string {
	return filepath.Join(s.dir, "webauthn-identity.json")
}

func (s *FileIdentityStore) GetIdentity(_ context.Context) (*Identity, bool, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, false, err
	}
	return &id, true, nil
}

func (s *FileIdentityStore) PutIdentity(_ context.Context, id *Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(), data, 0o644)
}

var _ IdentityStore = (*FileIdentityStore)(nil)

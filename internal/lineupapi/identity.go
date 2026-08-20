package lineupapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
)

// IdentityVersion is an optimistic-concurrency token for one Identity record.
// It is opaque to callers: read it off a GetIdentity result, hand it back
// unexamined on the matching PutIdentity, and never construct or compare one
// yourself.
//
// It is opaque because the backends do not agree on what a version *is*, and
// pretending otherwise would make one of them fake. S3's only compare-and-swap
// primitive is the ETag, which the store does not choose; the local file store
// derives one from the stored bytes; DynamoDB (where this record is headed —
// epic rosterbot-crq) uses a monotonic version attribute of its own. A decimal
// counter serialized into the JSON body would look backend-neutral and be pure
// decoration on S3, where the real precondition would still have to be the
// ETag — two mechanisms, one of them theater.
//
// The empty version is meaningful and is not "unset": it asserts that no record
// exists yet, and a write carrying it is a create that must fail if one does.
type IdentityVersion string

// ErrIdentityConflict reports that a conditional write lost — the record moved
// between the read and the write, so the caller's Identity was built on a
// version that is no longer current. The fix is always the same: re-read,
// re-apply the change, write again (see Config.mutateIdentity).
var ErrIdentityConflict = errors.New("lineupapi: identity record changed concurrently")

// Identity is the dashboard's single WebAuthn identity: a stable random user
// handle plus every registered passkey. There is exactly one Identity for the
// whole dashboard — one operator, multiple device-bound credentials.
type Identity struct {
	WebAuthnUserID []byte                `json:"webauthn_user_id"`
	Credentials    []webauthn.Credential `json:"credentials"`

	// Version is the optimistic-concurrency token this record was read at, and
	// the precondition its next write is made under. It rides on the struct
	// rather than alongside it in the method signature because that is the
	// shape the DynamoDB migration lands on — an attribute of the item, read
	// with it and asserted on write — and because a version passed separately
	// is a version a call site can forget to thread through.
	//
	// json:"-" keeps it out of the durable body: it describes *which* stored
	// bytes this value came from, so storing it inside those bytes would make
	// it self-referential, and a DynamoDB store would then have a stale JSON
	// copy shadowing the real version attribute.
	Version IdentityVersion `json:"-"`
}

// IdentityStore is the read/write side for the Identity record. Unlike
// ObjectStore/Publisher (write-once-per-key, read-many), this store is
// read-modify-written on every passkey registration, login (sign-counter
// update), and revocation — concurrently, by independent HTTP requests.
//
// Implementations MUST make PutIdentity conditional on id.Version:
//
//   - Version == "": create only if no record exists. Otherwise
//     ErrIdentityConflict.
//   - Version != "": write only if the stored record is still at that version.
//     Otherwise (including if the record is gone) ErrIdentityConflict.
//
// On success PutIdentity advances id.Version in place to the newly written
// version, so the same value can be written again without a re-read.
//
// The interface deliberately offers no unconditional write. Every mutation here
// is a read-modify-write, and a blind overwrite silently drops whichever
// concurrent change lost the race — a passkey registration that appears to
// succeed and later simply is not there, with nothing logged anywhere
// (rosterbot-crq.2). New implementations must be run through
// internal/lineupapi/identitytest.Run, which is the only thing that catches an
// implementation that ignores Version: doing so still satisfies this interface
// and still compiles.
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
// empty store (see Config.loadOrCreateIdentity).
func newWebAuthnUserID() ([]byte, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// FileIdentityStore is a local-filesystem IdentityStore, one JSON file at
// <dir>/webauthn-identity.json. Used by `rosterbot serve`.
//
// Its version token is the SHA-256 of the stored bytes, and its conditional
// write is a compare-under-mutex followed by an atomic rename. That is honest
// about its scope: the mutex serializes the read-verify-write within one
// process, which is exactly the blast radius of a local dev server. Two
// `rosterbot serve` processes sharing a directory could still interleave
// between the compare and the rename — the file backend has no OS primitive
// for compare-and-swap-on-content, and inventing a lockfile protocol for a
// single-operator dev store would be more machinery than the risk. The backend
// that actually runs concurrently is S3 (internal/lineupapi/s3lineup), and its
// precondition is enforced server-side.
type FileIdentityStore struct {
	dir string

	// mu serializes read-verify-write so a concurrent pair inside one process
	// cannot both pass the version check.
	mu sync.Mutex
}

func NewFileIdentityStore(dir string) *FileIdentityStore { return &FileIdentityStore{dir: dir} }

func (s *FileIdentityStore) path() string {
	return filepath.Join(s.dir, "webauthn-identity.json")
}

// readIdentity loads the record and its version, reporting absence as
// (nil, "", false, nil).
func (s *FileIdentityStore) readIdentity() (*Identity, IdentityVersion, bool, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, "", false, err
	}
	id.Version = versionOf(data)
	return &id, id.Version, true, nil
}

func (s *FileIdentityStore) GetIdentity(_ context.Context) (*Identity, bool, error) {
	id, _, ok, err := s.readIdentity()
	return id, ok, err
}

// PutIdentity writes id only if the on-disk record is still at id.Version. See
// the IdentityStore doc comment for the exact contract.
func (s *FileIdentityStore) PutIdentity(_ context.Context, id *Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, current, exists, err := s.readIdentity()
	if err != nil {
		return err
	}
	switch {
	case id.Version == "" && exists:
		return ErrIdentityConflict
	case id.Version != "" && !exists:
		return ErrIdentityConflict
	case id.Version != "" && id.Version != current:
		return ErrIdentityConflict
	}

	if err := s.writeAtomic(data); err != nil {
		return err
	}
	id.Version = versionOf(data)
	return nil
}

// writeAtomic replaces the record via a same-directory temp file plus rename,
// so a concurrent reader never observes a half-written body.
func (s *FileIdentityStore) writeAtomic(data []byte) error {
	f, err := os.CreateTemp(s.dir, "webauthn-identity-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename succeeds

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

var _ IdentityStore = (*FileIdentityStore)(nil)

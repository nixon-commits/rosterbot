package s3lineup

import (
	"context"
	"encoding/json"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// IdentityStore reads/writes the single WebAuthn Identity record at
// <prefix>identity.json.
type IdentityStore struct{ blob *s3blob.Blob }

// NewIdentity builds an IdentityStore. prefix should end in "/", e.g. "webauthn/".
func NewIdentity(ctx context.Context, bucket, prefix string) (*IdentityStore, error) {
	b, err := s3blob.New(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	return &IdentityStore{blob: b}, nil
}

// identityKey is fixed: there is exactly one identity record per deployment.
const identityKey = "identity.json"

func (s *IdentityStore) GetIdentity(ctx context.Context) (*lineupapi.Identity, bool, error) {
	b, found, err := s.blob.Get(ctx, identityKey)
	if err != nil || !found {
		return nil, false, err
	}
	var id lineupapi.Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, false, err
	}
	return &id, true, nil
}

func (s *IdentityStore) PutIdentity(ctx context.Context, id *lineupapi.Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	return s.blob.PutJSON(ctx, identityKey, data)
}

var _ lineupapi.IdentityStore = (*IdentityStore)(nil)

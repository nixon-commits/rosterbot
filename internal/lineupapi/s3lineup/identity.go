package s3lineup

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/s3blob"
)

// IdentityStore reads/writes the single WebAuthn Identity record at
// <prefix>identity.json.
//
// This is the only store in s3lineup that is read-modify-written rather than
// written once per key, so it is the only one that needs optimistic
// concurrency. lineupapi.IdentityVersion is carried as the object's ETag: a
// read reports it, a write asserts it via If-Match, and a brand-new record
// asserts absence via If-None-Match. S3 evaluates both server-side, which is
// what makes this correct across the independent Lambda invocations that a
// concurrent login and registration actually run in.
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
	b, etag, found, err := s.blob.GetWithETag(ctx, identityKey)
	if err != nil || !found {
		return nil, false, err
	}
	var id lineupapi.Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, false, err
	}
	id.Version = lineupapi.IdentityVersion(etag)
	return &id, true, nil
}

// PutIdentity writes id conditional on id.Version. See lineupapi.IdentityStore
// for the contract; an unmet precondition becomes ErrIdentityConflict, which
// the caller resolves by re-reading and re-applying its change.
func (s *IdentityStore) PutIdentity(ctx context.Context, id *lineupapi.Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}

	var etag string
	if id.Version == "" {
		etag, err = s.blob.PutJSONIfAbsent(ctx, identityKey, data)
	} else {
		etag, err = s.blob.PutJSONIfMatch(ctx, identityKey, string(id.Version), data)
	}
	if errors.Is(err, s3blob.ErrPrecondition) {
		return lineupapi.ErrIdentityConflict
	}
	if err != nil {
		return err
	}
	id.Version = lineupapi.IdentityVersion(etag)
	return nil
}

var _ lineupapi.IdentityStore = (*IdentityStore)(nil)

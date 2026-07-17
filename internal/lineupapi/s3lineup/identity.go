package s3lineup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// IdentityStore reads/writes the single WebAuthn Identity record at
// <prefix>identity.json.
type IdentityStore struct {
	client api
	bucket string
	prefix string
}

// NewIdentity builds an IdentityStore. prefix should end in "/", e.g. "webauthn/".
func NewIdentity(ctx context.Context, bucket, prefix string) (*IdentityStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &IdentityStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *IdentityStore) objKey() string { return s.prefix + "identity.json" }

func (s *IdentityStore) GetIdentity(ctx context.Context) (*lineupapi.Identity, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: ptr(s.objKey())})
	if err != nil {
		var nsk *types.NoSuchKey
		var nf *types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
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
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey()),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

var _ lineupapi.IdentityStore = (*IdentityStore)(nil)

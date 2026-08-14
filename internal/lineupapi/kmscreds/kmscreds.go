// Package kmscreds seals and opens tenant Fantrax credentials with KMS.
//
// It is its own package for the same reason s3lineup and ddbuser are: the AWS
// SDK stays out of internal/lineupapi, which the API handler and `serve` both
// import.
//
// The Sealer and Opener are separate types here, not one type satisfying both
// interfaces. That is deliberate — see lineupapi.Sealer. A single struct with
// both methods would compile everywhere and quietly hand the API the ability to
// decrypt the moment someone passed it the wrong value, which is exactly the
// mistake the split IAM in infra.go exists to make impossible.
package kmscreds

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// API is the slice of the KMS client this package uses.
type API interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// encryptionContext binds a ciphertext to one tenant.
//
// KMS refuses a Decrypt whose context does not match the Encrypt's, so a
// ciphertext lifted from one user's record cannot be opened as another's even
// by a principal holding kms:Decrypt. It is also what makes CloudTrail useful:
// every operation records WHOSE credential it named, so "was anyone's password
// read, and whose" is answerable after the fact rather than a matter of trust.
func encryptionContext(uid lineupapi.UserID) map[string]string {
	return map[string]string{"user_id": string(uid)}
}

// Sealer holds kms:Encrypt only.
type Sealer struct {
	api   API
	keyID string
}

// Opener holds kms:Decrypt only.
type Opener struct{ api API }

var (
	_ lineupapi.Sealer = (*Sealer)(nil)
	_ lineupapi.Opener = (*Opener)(nil)
)

// NewSealer builds the encrypt-only half, for the API Lambda.
func NewSealer(ctx context.Context, keyID string) (*Sealer, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Sealer{api: kms.NewFromConfig(cfg), keyID: keyID}, nil
}

// NewOpener builds the decrypt-only half, for the connect task and job runner.
func NewOpener(ctx context.Context) (*Opener, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Opener{api: kms.NewFromConfig(cfg)}, nil
}

func NewSealerWithAPI(api API, keyID string) *Sealer { return &Sealer{api: api, keyID: keyID} }
func NewOpenerWithAPI(api API) *Opener               { return &Opener{api: api} }

// maxPlaintext is KMS's direct-encryption limit. A Fantrax credential pair is
// far under it, which is why there is no data key and no envelope here — the
// simpler primitive is the correct one, and it is also why infra.go grants
// kms:Encrypt WITHOUT kms:GenerateDataKey.
const maxPlaintext = 4096

func (s *Sealer) Seal(ctx context.Context, uid lineupapi.UserID, plaintext []byte) ([]byte, error) {
	if uid == "" {
		return nil, fmt.Errorf("kmscreds: refusing to seal without a tenant; the " +
			"encryption context is what binds a ciphertext to one user")
	}
	if len(plaintext) > maxPlaintext {
		return nil, fmt.Errorf("kmscreds: plaintext is %d bytes, over the %d KMS "+
			"direct-encrypt limit", len(plaintext), maxPlaintext)
	}
	out, err := s.api.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(s.keyID),
		Plaintext:         plaintext,
		EncryptionContext: encryptionContext(uid),
	})
	if err != nil {
		return nil, err
	}
	return out.CiphertextBlob, nil
}

func (o *Opener) Open(ctx context.Context, uid lineupapi.UserID, ciphertext []byte) ([]byte, error) {
	if uid == "" {
		return nil, fmt.Errorf("kmscreds: refusing to open without a tenant")
	}
	out, err := o.api.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    ciphertext,
		EncryptionContext: encryptionContext(uid),
	})
	if err != nil {
		// Deliberately not wrapped with the tenant id or the ciphertext: a
		// decrypt failure is most often a context mismatch, and echoing either
		// side of it into a log is how the interesting values end up in
		// CloudWatch.
		return nil, fmt.Errorf("kmscreds: decrypt failed: %w", err)
	}
	return out.Plaintext, nil
}

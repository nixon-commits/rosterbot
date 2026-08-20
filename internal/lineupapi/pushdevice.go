package lineupapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
)

// PushDevice is one iOS device registered to receive push notifications.
//
// Environment and BundleID are stored rather than inferred because APNs
// answers 400 BadDeviceToken both for a genuinely dead token and for a
// sandbox token presented to the production host. The sender deletes records
// on BadDeviceToken (see internal/apns), so guessing the environment would
// make it delete every development device and look correct doing it.
type PushDevice struct {
	ID          string `json:"id"`
	Token       string `json:"token"`
	Environment string `json:"environment"` // sandbox | production
	BundleID    string `json:"bundle_id"`
	Model       string `json:"model"`
	CreatedAt   string `json:"created_at"`   // RFC3339 UTC
	LastSeenAt  string `json:"last_seen_at"` // RFC3339 UTC

	// Preferences is reserved for per-kind muting and is unused in v1: an
	// empty map means every kind is delivered. It exists now so adding the
	// feature later does not require reshaping stored records.
	Preferences map[string]bool `json:"preferences,omitempty"`
}

// PushDeviceStore is the device registry. Implemented by the DynamoDB store
// (production) and by FileUserStore (local `serve` and tests); both must pass
// pushdevicetest.Run, which is the real contract — the two properties below
// are not expressible in these signatures.
type PushDeviceStore interface {
	// PutPushDevice registers a device, or updates it in place when a record
	// with the same Token already exists for this user. It returns the stored
	// record, whose ID the caller needs in order to revoke it later.
	//
	// Idempotency is on Token, not ID: the client re-registers on every
	// launch because APNs tokens rotate on restore and reinstall, and an
	// insert-only store would accumulate one dead row per launch.
	//
	// Token uniqueness is GLOBAL: when the same Token is held by a DIFFERENT
	// user, that user's record is deleted as part of this registration. The
	// previous owner's session on the device is already dead by the time a
	// new user signs in there, so their client can never issue the revocation
	// DELETE itself — this is the only moment the stale record can be reaped,
	// and skipping it leaks the new owner's device into the old owner's
	// notification fan-out indefinitely.
	PutPushDevice(ctx context.Context, uid UserID, d PushDevice) (PushDevice, error)

	PushDevices(ctx context.Context, uid UserID) ([]PushDevice, error)

	// DeletePushDevice revokes one device. Deleting an absent device is a
	// success: the caller wanted it gone and it is.
	DeletePushDevice(ctx context.Context, uid UserID, id string) error
}

var _ PushDeviceStore = (*FileUserStore)(nil)

// NewPushDeviceID returns an opaque, URL-safe device id. Exported because the
// DynamoDB store mints ids the same way. The APNs token is NOT used as the
// id: it rotates, and it would then appear in the DELETE route's path and
// therefore in access logs.
func NewPushDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

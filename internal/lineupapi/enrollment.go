package lineupapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// An enrollment link authorizes exactly one passkey registration for exactly
// one user, once, before an expiry.
//
// Invite and recovery are the same primitive, built once and used twice. An
// invite enrolls the first passkey for a new user; a recovery enrolls a
// replacement for someone who lost their device. The two differ only in whether
// the user already has credentials, which is not a distinction the token needs
// to carry.
//
// It replaces the global bootstrap token for enrollment (webauthn.go's
// canRegister). That token authorizes registration against an identity model
// with no notion of WHOSE — harmless with one operator, a god token the moment
// there are several, since it would let its holder enroll a passkey against any
// account. The bearer token remains an admin break-glass for the API itself.
//
// This is also, unavoidably, a second authentication path: whatever lets a user
// who lost their phone back in IS a way in, whatever it is called. Naming it
// honestly is better than pretending passkeys are the only credential.
type Enrollment struct {
	// UserID is the account this token may enroll a passkey for, and the only
	// one. Scoping is the whole difference from the bootstrap token.
	UserID UserID `json:"user_id"`

	// TeamID and Email are the admin's assertions at mint time: "this person
	// owns team 7 and is reachable at this address". The connect flow later
	// proves the team claim against Fantrax's own MyTeamIDs, so what is
	// asserted here is checked there rather than trusted.
	TeamID string `json:"team_id,omitempty"`
	Email  string `json:"email,omitempty"`

	ExpiresAt time.Time `json:"expires_at"`

	// UsedAt is zero until redeemed. Redemption must set it atomically, or the
	// token is single-use only in the sense that a well-behaved client uses it
	// once.
	UsedAt time.Time `json:"used_at,omitempty"`
}

// Expired reports whether the token is past its expiry at the given time.
//
// Expiry MUST be enforced in code and never left to storage-level cleanup.
// DynamoDB's TTL deletion is asynchronous and documented to lag — typically
// minutes, but up to 48 hours — so a token relied on to have been reaped can
// still be sitting there, live, long after it expired. TTL is housekeeping;
// this is the check.
func (e Enrollment) Expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt)
}

// Redeemed reports whether the token has already been used.
func (e Enrollment) Redeemed() bool { return !e.UsedAt.IsZero() }

// ErrEnrollmentInvalid is returned for every unusable token: unknown, expired,
// or already redeemed.
//
// One error for all three on purpose. Distinguishing them would answer "does
// this token exist?" for anyone who asks, turning a failed redemption into an
// oracle. Callers that need the specific reason for an operator-facing log get
// it from EnrollmentStore's own logging, not from the error a request sees.
var ErrEnrollmentInvalid = errors.New("lineupapi: enrollment link is not valid")

// EnrollmentToken is the secret handed to a user out-of-band. It is never
// stored — only its hash is — so a leak of the identity table does not yield
// usable enrollment links.
type EnrollmentToken string

// MintEnrollmentToken returns a fresh token and the hash to store against it.
//
// 32 bytes of entropy: an enrollment link is a bearer credential that grants
// account access, and unlike a password it is never rate-limited by a human
// typing it. There is no reason to be frugal here.
func MintEnrollmentToken() (EnrollmentToken, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	tok := EnrollmentToken(base64.RawURLEncoding.EncodeToString(b))
	return tok, HashEnrollmentToken(tok), nil
}

// HashEnrollmentToken derives the stored key for a token. SHA-256 with no salt
// and no stretching is correct here and would be wrong for a password: the
// input is 256 bits of uniform randomness, so there is no dictionary to attack
// and nothing for a work factor to buy.
func HashEnrollmentToken(t EnrollmentToken) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// EnrollmentStore persists enrollment links by token hash.
//
// It is a separate interface from UserStore because it has a separate
// lifecycle — write once, read once, then dead — even though both backends
// implement it over the same underlying table or directory.
type EnrollmentStore interface {
	// CreateEnrollment stores a new link. It fails with ErrUserConflict if the
	// hash already exists, which in practice means a repeated token — either a
	// broken random source or a replayed mint.
	CreateEnrollment(ctx context.Context, tokenHash string, e Enrollment) error

	// RedeemEnrollment atomically marks the link used and returns it.
	//
	// Atomicity is the requirement, not a nicety: check-then-mark lets two
	// concurrent redemptions of one link both succeed, and a recovery link that
	// can be used twice is a recovery link an attacker can use after the
	// legitimate owner already did.
	//
	// Returns ErrEnrollmentInvalid if the link is unknown, expired, or already
	// redeemed.
	RedeemEnrollment(ctx context.Context, tokenHash string, now time.Time) (Enrollment, error)

	// GetEnrollment reads a link without redeeming it, so a UI can show what an
	// invite is for before the user commits to spending it.
	GetEnrollment(ctx context.Context, tokenHash string) (Enrollment, bool, error)
}

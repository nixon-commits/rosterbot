package lineupapi

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// UserID identifies one tenant. It is the base64url encoding of that user's
// 64-byte WebAuthn user handle — the same bytes newWebAuthnUserID() already
// mints — and not a separate id that happens to sit beside one.
//
// They are the same value on purpose. Registration requires discoverable
// credentials (ResidentKeyRequirementRequired, see webauthn.go), so the
// authenticator returns the user handle on every assertion. Making the handle
// the primary key turns "who is this?" at login into a direct key-get with no
// index and no lookup table to fall out of sync. The CRED# index below exists
// only for the non-discoverable fallback, not for the everyday path.
//
// The consequence to know: this id can never be rotated. Rotating it would
// invalidate every passkey bound to it, since the handle is baked into the
// credential at registration time.
type UserID string

// NewUserID encodes a raw WebAuthn user handle as a UserID.
func NewUserID(handle []byte) UserID {
	return UserID(base64.RawURLEncoding.EncodeToString(handle))
}

// WebAuthnHandle decodes a UserID back to the raw handle the authenticator
// expects. An unparsable id yields nil, which callers must treat as "no such
// user" rather than "empty handle" — go-webauthn compares handles bytewise and
// an empty one would match nothing anyway, but failing explicitly is clearer.
func (u UserID) WebAuthnHandle() []byte {
	b, err := base64.RawURLEncoding.DecodeString(string(u))
	if err != nil {
		return nil
	}
	return b
}

// NewWebAuthnUserID mints a fresh 64-byte user handle — the exported form of
// the generator registration already uses. Exported so that a user created by
// an admin invite and one created by self-registration are the same shape:
// two generators would eventually differ, and the handle is the primary key.
func NewWebAuthnUserID() ([]byte, error) { return newWebAuthnUserID() }

// Role decides which routes a caller may reach. There are exactly two, and the
// distinction is not seniority: a member sees only their own tenant's data,
// while an admin can read any tenant's and reach the league-wide controls.
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
)

// UserStatus gates whether the per-tenant job fan-out includes this user.
// Parking a tenant must be a status change rather than "their jobs started
// failing", so that a deliberately idle account costs nothing and is
// distinguishable from a broken one.
type UserStatus string

const (
	UserActive UserStatus = "active"
	UserParked UserStatus = "parked"
)

// Errors a UserStore returns for the conditions callers must handle
// distinctly. Everything else is an ordinary error.
var (
	// ErrUserConflict reports that a conditional write lost: the record moved
	// between the read and the write. Recover by re-reading, re-applying the
	// change, and writing again — never by re-writing the stale snapshot.
	ErrUserConflict = errors.New("lineupapi: user record changed concurrently")

	// ErrEmailTaken and ErrTeamTaken report a uniqueness violation on create.
	//
	// ErrTeamTaken is the load-bearing one. Two users claiming the same Fantrax
	// team is not a cosmetic clash: both tenants' hourly jobs would then
	// optimize and apply to the same roster, each reading the other's changes
	// as drift to correct, fighting every hour. Neither side can detect it,
	// because from either tenant's view it is indistinguishable from the
	// manager making manual changes.
	ErrEmailTaken = errors.New("lineupapi: email already claimed")
	ErrTeamTaken  = errors.New("lineupapi: fantrax team already claimed")
)

// User is one tenant's profile. Credentials are deliberately NOT a field here —
// see UserStore.
type User struct {
	ID          UserID     `json:"id"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Role        Role       `json:"role"`
	Status      UserStatus `json:"status"`

	// AutoApply is the only field that decides whether this bot writes to
	// someone else's roster. It defaults false and is turned on by the user
	// deliberately, so that every documented failure in the apply path
	// (rosterbot-z3b, -sza, -48z) reaches only tenants who opted in.
	AutoApply bool `json:"auto_apply"`

	// TeamID is the Fantrax team this user manages. It is set at connect time
	// from the invite and proven against TeamRosterResponse.MyTeamIDs; it is
	// never typed by the user, and the store enforces one claimant per team.
	TeamID string `json:"team_id,omitempty"`

	// Memberships holds SLEEPER leagues only. The Fantrax membership is
	// projected from TeamID by AllMemberships rather than stored here, so the
	// proven-at-connect team and its ErrTeamTaken uniqueness claim keep exactly
	// one home. Two copies of the same fact is how they come to disagree.
	//
	// Absent on every record written before this field existed, which decodes
	// to nil and renders as "Fantrax only" — the correct answer for all of them.
	Memberships []Membership `json:"memberships,omitempty"`

	// TokenVersion invalidates sessions. Session cookies carry the value they
	// were minted at; incrementing this logs out every device for this user
	// without touching anyone else, which is what makes revocation possible at
	// all for a stateless HMAC session.
	TokenVersion int `json:"token_version"`

	// Email is attested rather than verified: there is no SES and no
	// challenge-response. The admin types the address when minting an invite
	// and delivers the link out-of-band, and at connect time Fantrax's own
	// UserInfo.Email corroborates it. Recording who vouched and when keeps that
	// honest — an unattested address in this field would be indistinguishable
	// from a verified one.
	EmailAttestedBy UserID    `json:"email_attested_by,omitempty"`
	EmailAttestedAt time.Time `json:"email_attested_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`

	// Version is the optimistic-concurrency token this record was read at and
	// the precondition its next write is made under, exactly as
	// Identity.Version (identity.go) — same rules, same opacity, same reason
	// for riding on the struct rather than the method signature.
	Version IdentityVersion `json:"-"`
}

// CredentialMeta is the user-facing decoration on one passkey: a name the
// user chose and when it was registered. It lives in its OWN store key, never
// inside the webauthn.Credential record, because the login ceremony overwrites
// that record (sign counter) and a rename racing a login must not be able to
// clobber either side — the same per-key no-contention rule that keeps
// registration and login from contending.
type CredentialMeta struct {
	Name      string    `json:"name,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// CredentialKey is how CredentialMetas maps are keyed: the base64url of the
// credential id, the same encoding the HTTP surface already uses for passkey
// ids. A []byte cannot be a map key, and re-encoding at every call site is how
// two spellings of one id drift apart.
func CredentialKey(credID []byte) string {
	return base64.RawURLEncoding.EncodeToString(credID)
}

// UserCredentials pairs a user with their passkeys for the WebAuthn ceremony,
// which needs both together even though the store keeps them apart.
type UserCredentials struct {
	User        *User
	Credentials []webauthn.Credential
}

func (u UserCredentials) WebAuthnID() []byte { return u.User.ID.WebAuthnHandle() }
func (u UserCredentials) WebAuthnName() string {
	if u.User.Email != "" {
		return u.User.Email
	}
	return string(u.User.ID)
}

// WebAuthnDisplayName is what the OS passkey picker shows. It falls back to the
// id rather than to a constant: with N users, a shared literal like "rosterbot"
// would make two accounts on one device indistinguishable in the picker.
func (u UserCredentials) WebAuthnDisplayName() string {
	if u.User.DisplayName != "" {
		return u.User.DisplayName
	}
	return string(u.User.ID)
}
func (u UserCredentials) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

// UserStore is the tenant directory: profiles, their passkeys, and the two
// uniqueness constraints that keep tenants from colliding.
//
// CREDENTIALS ARE PER-KEY, NOT PART OF THE PROFILE, and that is the whole
// concurrency design. rosterbot-crq.2 fixed a lost update where registering a
// passkey and logging in both rewrote one record, and whichever write lost
// silently dropped the other's change. Compare-and-set made that detectable and
// retryable. Splitting credentials into their own keys makes it impossible:
// PutCredential for a new passkey and PutCredential for another passkey's sign
// counter touch different keys and cannot contend at all. The profile keeps its
// version because profile fields genuinely are read-modify-written.
//
// Implementations MUST run through internal/lineupapi/usertest.Run. The
// contract is not expressible in the signatures — a store that ignores
// User.Version, or that lets two users claim one team, still compiles and still
// satisfies this interface.
type UserStore interface {
	// GetUser returns the profile. ok=false means no such user, which is an
	// ordinary outcome (an unknown session subject, a redeemed invite pointing
	// at a deleted account), not an error.
	GetUser(ctx context.Context, id UserID) (*User, bool, error)

	// CreateUser writes a brand new profile, claiming its email and (when set)
	// its team. It fails with ErrUserConflict if the id exists, ErrEmailTaken
	// or ErrTeamTaken if either claim is already held. All three checks and the
	// write must be atomic together: checking first and writing second is the
	// race this method exists to avoid.
	CreateUser(ctx context.Context, u *User) error

	// PutUser updates an existing profile, conditional on u.Version, advancing
	// it in place on success. It does NOT re-check the email claim — changing
	// an email is a separate operation, deliberately absent until something
	// needs it.
	PutUser(ctx context.Context, u *User) error

	// ClaimTeam binds a Fantrax team to a user, failing with ErrTeamTaken if
	// another user holds it. Separate from CreateUser because the claim happens
	// later: the invite names the team, but only the connect task can prove the
	// credentials actually control it.
	ClaimTeam(ctx context.Context, id UserID, teamID string) error

	// Credentials returns every passkey registered to this user, in no
	// guaranteed order.
	Credentials(ctx context.Context, id UserID) ([]webauthn.Credential, error)

	// PutCredential writes one passkey, creating or overwriting by credential
	// id. It is deliberately unconditional: registration writes a credential id
	// that did not exist, and login overwrites exactly one field (the sign
	// counter) of a credential only that ceremony could have produced. Neither
	// can lose another writer's data, because no other writer touches that key.
	PutCredential(ctx context.Context, id UserID, c webauthn.Credential) error

	// DeleteCredential removes one passkey. Removing an absent credential is
	// not an error — the caller's intent is satisfied either way, and the
	// alternative invites a check-then-act race. It removes the credential's
	// meta alongside it.
	DeleteCredential(ctx context.Context, id UserID, credID []byte) error

	// PutCredentialMeta writes one passkey's user-facing decoration (name,
	// created-at), whole. It is unconditional for the same reason
	// PutCredential is: no other writer touches this key.
	PutCredentialMeta(ctx context.Context, id UserID, credID []byte, meta CredentialMeta) error

	// CredentialMetas returns every annotated passkey's meta, keyed by
	// CredentialKey. A passkey registered before metadata existed simply has
	// no entry — absent, never an error and never an invented date.
	CredentialMetas(ctx context.Context, id UserID) (map[string]CredentialMeta, error)

	// UserByCredential resolves a credential id to its owner, for the
	// non-discoverable login fallback. The everyday path does not use it: a
	// discoverable credential returns the user handle directly, which IS the
	// UserID.
	UserByCredential(ctx context.Context, credID []byte) (UserID, bool, error)

	// ListActive returns every user the job fan-out should run for.
	//
	// It is a full scan by design. At a dozen tenants that is one request and
	// cheaper than maintaining an index; the cost of being wrong is a cheap
	// migration, and the cost of a premature GSI is a second thing to keep
	// consistent. Revisit at four digits.
	ListActive(ctx context.Context) ([]*User, error)

	// ListUsers returns every user regardless of status — the admin
	// directory. It exists because the two listings answer different
	// questions and conflating them broke one of them: the fan-out must skip
	// a parked tenant, while the admin page must keep showing them, since the
	// row it shows is the only reactivate control there is.
	ListUsers(ctx context.Context) ([]*User, error)

	// DeleteUser removes a tenant entirely: profile, every credential and its
	// lookup index, the Fantrax connection record where the backend holds one,
	// and the email/team uniqueness claims. RELEASING THE CLAIMS IS THE POINT:
	// a delete that left them behind would permanently poison that email and
	// that team — the person could never be re-invited and the team never
	// reassigned, the same trap that makes ClaimTeam refuse moves. Deleting an
	// absent user is not an error (the caller's intent is satisfied either
	// way; erroring invites a check-then-act race). The tenant's durable S3
	// artifacts are deliberately NOT touched — inert orphans, same class as
	// the crq.11 cutover copies.
	DeleteUser(ctx context.Context, id UserID) error
}

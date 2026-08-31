package lineupapi

import (
	"context"
	"errors"
	"time"
)

// ConnStatus is where a tenant's Fantrax connection stands.
//
// It is a field rather than something inferred from "did the last job fail",
// because the two are different questions. A job can fail for a dozen reasons
// that say nothing about the credential; a credential can be dead while no job
// has run yet. Only this field decides whether the bot may act for a tenant.
type ConnStatus string

const (
	// ConnPending: credentials accepted, not yet verified against Fantrax. The
	// connect task has been launched but has not reported back.
	ConnPending ConnStatus = "pending"

	// ConnVerified: a login succeeded and the team was proven to belong to
	// these credentials.
	ConnVerified ConnStatus = "verified"

	// ConnNeedsReconnect: the stored credentials no longer work. The bot must
	// STOP acting for this tenant and must NOT retry — see the ladder in
	// docs/superpowers/specs/2026-08-12-multiuser-passkey-tenancy-design.md.
	// Repeated failed logins are what trigger Fantrax lockout and Cloudflare
	// bot-blocking, so an hourly retry would lock the user out of their own
	// account using credentials they handed us.
	ConnNeedsReconnect ConnStatus = "needs_reconnect"

	// ConnInterrupted: Fantrax ACCEPTED the sign-in and a step after it did not
	// complete. The credentials are not implicated at all.
	//
	// It is not ConnNeedsReconnect because nothing about the password is in
	// question — telling someone to re-enter working credentials is the exact
	// failure ConnErrBotChallenge already exists to avoid, arriving by a
	// different route (rosterbot-ch0s). It is not ConnPending either: pending
	// means a verification is RUNNING, and settings.js renders it as "Checking
	// your credentials…" with no bound, so a failure parked there is a spinner
	// that never resolves.
	//
	// Usable() stays false, so AuthorizeRun and the tenant fan-out refuse it
	// with no edit: the connection genuinely was not confirmed. What differs is
	// what the tenant is told and what they are asked to do — retry, not
	// re-enter.
	ConnInterrupted ConnStatus = "interrupted"
)

// Connect failure classes. These are CLASSES, not messages: they are shown to
// the user and recorded in ops surfaces, and a raw error from a headless
// browser is neither actionable nor safe to echo back.
const (
	// ConnErrBadCredentials — Fantrax rejected the username/password. The user
	// must supply new ones; retrying cannot help and can cause lockout.
	ConnErrBadCredentials = "bad_credentials"

	// ConnErrLoginChallengeOrTimeout — the login produced no session and the
	// page said nothing that identifies why.
	//
	// This is the HONEST-IGNORANCE class, and keeping it that way is the point.
	// It used to absorb every failure — a wrong password, a 2FA prompt and a
	// Cloudflare interstitial all end with no FX_RM cookie, so inferring the
	// cause from the cookie's absence collapsed three different problems into
	// one unactionable label (rosterbot-43q). The two classes below are split
	// out of it on positive evidence from the page. What remains here is what
	// genuinely could not be told apart, and it must not be widened again by
	// guessing.
	ConnErrLoginChallengeOrTimeout = "login_challenge_or_timeout"

	// ConnErrTwoFactor — Fantrax asked for a second factor, which means the
	// username and password were ACCEPTED. Distinct from BadCredentials because
	// the remedy is the opposite: re-entering the password cannot help, and the
	// user must disable two-factor auth or the account cannot be automated.
	ConnErrTwoFactor = "two_factor_required"

	// ConnErrBotChallenge — a Cloudflare interstitial stood in front of the
	// login form. The credentials are not implicated at all.
	//
	// This is the one connect failure that is OPERATOR-actionable: our headless
	// browser is being treated as a bot, and nothing the user types changes
	// that. It must therefore never set ConnNeedsReconnect, which would tell
	// someone their working credentials are dead — the same distinction
	// runConnect already draws for a KMS decrypt failure.
	ConnErrBotChallenge = "bot_challenge"

	// ConnErrTeamNotOwned — the credentials work but do not control the team
	// the invite named. Fantrax's own MyTeamIDs is the authority here.
	ConnErrTeamNotOwned = "team_not_owned"

	// ConnErrNoTeam — the user's record names no team, so there is nothing for
	// MyTeamIDs to confirm.
	//
	// This is an ADMIN error surfaced as a connection failure: `invite` treats
	// --team as optional, and neither migrate-identity nor the invite bootstrap
	// sets one, so a perfectly valid Fantrax login can arrive with no team
	// attached. It is a distinct class from TeamNotOwned
	// because the remedy is different — the user's credentials are fine and
	// re-entering them cannot help; someone has to assign the team.
	//
	// Without this class the empty team was not an error at all: both ownership
	// checks were guarded on a non-empty TeamID and the ConnVerified assignment
	// after them was not, so the connection was marked proven having proven
	// nothing (rosterbot-crq.18).
	ConnErrNoTeam = "no_team"

	// ConnErrTeamClaimed — another tenant already holds that team.
	ConnErrTeamClaimed = "team_claimed"

	// ConnErrVerificationInterrupted — the sign-in reached Fantrax and worked,
	// and a step AFTER it did not finish: the identity call, the ownership
	// lookup, the team claim, or sealing the session.
	//
	// It exists because those failures used to be laundered into a verdict
	// about the tenant's password. A Fantrax 5xx inside the identity call
	// landed on ConnErrBadCredentials — the harshest class in this vocabulary —
	// and a blip fetching MyTeamIDs landed on ConnErrLoginChallengeOrTimeout;
	// both then wrote ConnNeedsReconnect, telling someone whose credentials
	// Fantrax had just accepted that those credentials no longer work
	// (rosterbot-ch0s, audit 2026-08-17).
	//
	// THE EVIDENCE IS FANTRAX'S OWN: an FX_RM cookie. Fantrax issues one only
	// once it has accepted a sign-in, so holding one is Fantrax stating the
	// credentials are fine. Nothing here is inferred from the shape of an
	// error, which is the guess ConnErrLoginChallengeOrTimeout's comment above
	// forbids.
	//
	// The remedy is to retry, not to re-enter a password, which is why this is
	// the one tenant-facing class whose connect task exits NON-ZERO: only the
	// operator can act on a Fantrax outage, and the run ledger is where they
	// hear about it.
	ConnErrVerificationInterrupted = "verification_interrupted"
)

// ErrNoConnection reports that a tenant has never connected Fantrax.
var ErrNoConnection = errors.New("lineupapi: no fantrax connection for user")

// FantraxConnection is one tenant's link to their Fantrax account.
//
// The two ciphertext fields are never decrypted by anything that serves HTTP —
// see Sealer/Opener below.
type FantraxConnection struct {
	UserID UserID `json:"user_id"`

	// TeamID is proven, not asserted: the connect task checks it against
	// TeamRosterResponse.MyTeamIDs before this record is written verified.
	TeamID string `json:"team_id,omitempty"`

	// FantraxUserID and FantraxEmail come from Fantrax's own UserInfo. The
	// email corroborates the address the admin attested at invite time; a
	// mismatch is recorded, not fatal, since a manager may legitimately use a
	// different address with Fantrax.
	FantraxUserID string `json:"fantrax_user_id,omitempty"`
	FantraxEmail  string `json:"fantrax_email,omitempty"`

	Status    ConnStatus `json:"status"`
	LastError string     `json:"last_error,omitempty"`

	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`

	// CredsCiphertext wraps the username/password pair; FXRMCiphertext wraps
	// the session cookie. Separate because they have different lifetimes: the
	// cookie is replaced on every re-login, the credentials only when the user
	// reconnects.
	CredsCiphertext []byte `json:"creds_ct,omitempty"`
	FXRMCiphertext  []byte `json:"fx_rm_ct,omitempty"`
}

// Usable reports whether the bot may act for this tenant.
func (c *FantraxConnection) Usable() bool { return c != nil && c.Status == ConnVerified }

// Sealer encrypts a tenant's credential. Opener decrypts it.
//
// THEY ARE SEPARATE INTERFACES SO THE SPLIT IS IN THE TYPE SYSTEM, not only in
// IAM. infra.go grants the API Lambda kms:Encrypt and the ECS task role
// kms:Decrypt, and nothing both; these two interfaces mirror that, so a handler
// that tried to read a credential back would not compile rather than failing at
// runtime with an AccessDenied nobody sees until it happens.
//
// The API holds a Sealer only. That is the whole reason connect is
// asynchronous: the API cannot verify a credential itself, because verifying
// requires reading it. Anyone tempted to "simplify" by handing the API an
// Opener has traded away the property that a compromise of the public Function
// URL yields ciphertext and no key.
type Sealer interface {
	// Seal encrypts plaintext under the tenant's encryption context, so a
	// ciphertext stolen from one user's record cannot be decrypted as another's
	// — KMS itself refuses the mismatch, and CloudTrail records which tenant
	// each operation named.
	Seal(ctx context.Context, uid UserID, plaintext []byte) ([]byte, error)
}

// Opener is held ONLY by the connect task and the job runner.
type Opener interface {
	Open(ctx context.Context, uid UserID, ciphertext []byte) ([]byte, error)
}

// ConnectionStore persists the record. It is deliberately not split the way
// Sealer/Opener are: the ciphertext is opaque to whoever holds it, so reading
// the record without an Opener discloses nothing beyond status and team.
type ConnectionStore interface {
	GetConnection(ctx context.Context, uid UserID) (*FantraxConnection, bool, error)
	PutConnection(ctx context.Context, c *FantraxConnection) error
}

// FantraxCreds is the plaintext pair, which exists only in memory: in the API
// handler for as long as it takes to seal, and in the connect task for as long
// as it takes to drive a login.
//
// It has no JSON tags on purpose. Marshalling it is never correct — the durable
// form is always the sealed ciphertext — and an absent tag makes an accidental
// json.Marshal produce field names that stand out in a log rather than a
// plausible-looking record.
type FantraxCreds struct {
	Username string
	Password string
}

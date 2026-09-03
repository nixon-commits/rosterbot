package cmd

import (
	"strings"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// loginEvidence is everything the connect task learned from one login attempt.
//
// It is rosterbot's own type rather than the auth_client outcome it is built
// from, for two reasons. The classification below is policy over a vocabulary
// (ConnErr*) that the fork has no business knowing, and keeping the seam here
// means the rule is unit-testable without a headless browser — which is the
// only way it gets tested at all, since everything around it needs DynamoDB,
// KMS and Chrome.
type loginEvidence struct {
	// FXRM and Info are the two halves of a proven login: a session cookie and
	// the account it belongs to.
	FXRM string
	Info *fantraxUserInfo

	// FinalURL and Title describe where the browser ended up. They are
	// load-bearing when every probe misses: Fantrax accepting a login navigates
	// away from the login page, and Cloudflare announces itself in the title
	// even when its DOM markers move.
	FinalURL string
	Title    string

	// Matched and Texts are the probe results — which selectors were present
	// after submit, and any message they carried.
	Matched map[string]bool
	Texts   map[string]string

	// Err is the browser script's own failure, if it had one. It is evidence,
	// not a verdict: a Cloudflare challenge shows up here as a WaitVisible
	// timeout, and classifying on the error alone is what produced the single
	// undifferentiated class in the first place.
	Err error

	// PersistErr and IdentityErr are the two failures that can happen AFTER
	// Fantrax has already accepted the sign-in. Separating them from Err is the
	// whole of rosterbot-ch0s: an error recorded in Err is "we never got a
	// session", and one recorded here is "we got a session and something later
	// broke", which are opposite answers to "are these credentials good?".
	//
	// PersistErr: the cookie was in hand and could not be written to the local
	// cache file. See defaultBrowserLogin for why this cannot yet be observed
	// in production.
	//
	// IdentityErr: auth_client.NewClient failed with a proven cookie installed.
	// NewClient returns a non-nil client only when Login() succeeded and
	// Login() only returns nil once UserInfo.UserID is non-empty (verified in
	// the pinned fork, auth_client/fantrax_client.go), so a non-nil value here
	// means Fantrax issued a cookie and then failed to answer who it belongs
	// to — an outage, not a rejection.
	PersistErr  error
	IdentityErr error
}

func (e loginEvidence) has(probe string) bool { return e.Matched[probe] }

func (e loginEvidence) text(probe string) string { return e.Texts[probe] }

// loginProof is Fantrax's own statement that a username and password were
// accepted: the FX_RM cookie it issued.
//
// It is a type rather than a bare string for the reason teamProof is one — the
// non-demoting failure route must be unreachable without evidence, and an
// ordering rule that lives only in statement order is one revert away from
// being gone with every test still green. classifyLogin is its only
// constructor. Go cannot stop an in-package literal from forging loginProof{},
// so the zero value is inert and connectRun.fail refuses it.
type loginProof struct{ fxrm string }

// failureRoute is what a connect failure DOES, derived from its class rather
// than chosen at the call site.
//
// Deriving it is the point: cmd/connect.go and cmd/session_ladder.go are two
// independent writers of a tenant's connection status (the second is why the
// 2026-08-26 attempt at this bead only half-fixed it), and a route each of them
// picked for itself is a route they can disagree about.
type failureRoute int

const (
	// routeTenant: the credentials themselves are implicated. Record
	// needs_reconnect, tell the tenant, exit 0 — a login that legitimately
	// failed is a RESULT, not a task crash.
	routeTenant failureRoute = iota

	// routeOperator: only the operator can act. The tenant's status must be
	// left alone and the task must exit non-zero so the ledger pages.
	routeOperator

	// routePostAuth: Fantrax accepted the sign-in and something after it did
	// not finish. Neither of the above: the tenant is told (it is their
	// connection that is unconfirmed) AND the operator is paged via the
	// non-zero exit (only they can fix a Fantrax outage), and the status says
	// interrupted rather than blaming a working password.
	routePostAuth

	// routeInternal: the task never got far enough to ask Fantrax anything —
	// a fault on OUR side (decrypting stored credentials, reading the
	// connection record, or the task's own KMS/DynamoDB wiring) before any
	// sign-in was attempted. Like routePostAuth it carries BOTH halves: the
	// tenant is told to retry (nothing here says anything about their
	// password) and the operator is paged via the non-zero exit (only they
	// can fix a KMS or DynamoDB fault). The status says check_failed —
	// never interrupted, which claims a proven Fantrax login this route
	// structurally cannot have (rosterbot-spb9).
	//
	// UNREACHABLE FROM cmd/session_ladder.go BY CONSTRUCTION, not by
	// omission. classifyLogin, the ladder's only source of a loginVerdict,
	// classifies evidence FROM an attempted login — a cookie, a probe match,
	// a browser error — and never returns ConnErrCheckFailed, because the
	// ladder's refresh() only calls classifyLogin after it has already
	// installed a session and driven a re-login; it never touches the
	// decrypt-and-read machinery this route exists to guard, which lives
	// entirely in cmd/connect.go's connectTenant/runConnect. If that ever
	// changed, sessionLadder.stop's switch treats an unrecognised route
	// exactly like routeOperator (never touches conn.Status) rather than
	// falling into its default case, which assumes routeTenant and would
	// wrongly demote the tenant to needs_reconnect for a fault that was
	// never theirs.
	routeInternal
)

// loginVerdict is a class plus what to do about it.
type loginVerdict struct {
	class string
	route failureRoute
}

// operatorActionable reports whether this failure must not touch the tenant's
// connection status. Kept as an accessor so callers read the routing decision
// from one place rather than re-deriving it from the class.
func (v loginVerdict) operatorActionable() bool { return v.route == routeOperator }

// cloudflareTitles are substrings Cloudflare's interstitials put in
// document.title. Matched case-insensitively.
var cloudflareTitles = []string{
	"just a moment",
	"attention required",
	"checking your browser",
	"cloudflare",
}

// classifyLogin turns observed evidence into a connect failure class, or "" if
// the login succeeded.
//
// The ordering is deliberate and each step is load-bearing:
//
//  1. Success first. A proven session outranks any stray probe match — we hold
//     a cookie AND the identity it belongs to, which is not something a
//     challenge page can produce.
//  2. Cloudflare before everything else, because it sits IN FRONT of the login
//     form: when it fires, nothing below it has been observed at all, and the
//     credentials are not implicated.
//  3. Two-factor before bad credentials, because a code prompt means the
//     password was accepted. Reading it as a rejection would tell the user to
//     change a password that is working.
//  4. Bad credentials only on POSITIVE evidence — still on the form AND the
//     form said something. Absence of a cookie is not evidence of a wrong
//     password; that inference is the bug this function replaces.
//  5. A named post-auth failure — Fantrax issued a cookie and a step after it
//     broke. Below the form probe on purpose; see the branch's own comment.
//  6. Everything else stays ambiguous on purpose.
//
// It returns the proof alongside the verdict so a caller cannot record a
// post-auth failure without holding the evidence for it.
func classifyLogin(e loginEvidence) (loginProof, loginVerdict) {
	if e.FXRM != "" && e.Info != nil && e.Info.UserID != "" {
		return loginProof{fxrm: e.FXRM}, loginVerdict{}
	}

	if e.has("cloudflare") || matchesAny(e.Title, cloudflareTitles) {
		return loginProof{}, verdictFor(lineupapi.ConnErrBotChallenge)
	}

	if e.has("otp") {
		return loginProof{}, verdictFor(lineupapi.ConnErrTwoFactor)
	}

	// The form is still on screen and told us why. This is the path that makes
	// ConnErrBadCredentials reachable: its previous condition — a cookie
	// present but no identity — describes a session that half-worked, not a
	// password Fantrax refused.
	if e.has("login_form") && (e.text("form_error") != "" || e.text("error_text") != "") {
		return loginProof{}, verdictFor(lineupapi.ConnErrBadCredentials)
	}

	// FANTRAX SAID YES AND SOMETHING AFTER IT SAID NO (rosterbot-ch0s).
	//
	// The condition requires BOTH a cookie and a named post-auth failure, and
	// requiring both is what keeps this safe. A bare `e.FXRM != ""` test would
	// be tempting — Fantrax is not supposed to issue FX_RM to a rejected
	// password — but nobody has measured that, and the fork records the
	// opposite risk in the same breath: LoginWithBrowser's own comment says "a
	// rejected password reaches this line too" immediately before it reads the
	// cookie jar. If FX_RM does linger there on a rejection, a bare test would
	// classify every wrong password as retryable, tell the tenant their
	// password is fine, never demote them, and invite unbounded retries — the
	// documented trigger for Fantrax lockout and Cloudflare bot-blocking. The
	// narrow condition fires only on the two failures we can actually name, so
	// it also does not need to outrank the form-error probe above it.
	if e.FXRM != "" && (e.IdentityErr != nil || e.PersistErr != nil) {
		return loginProof{fxrm: e.FXRM}, verdictFor(lineupapi.ConnErrVerificationInterrupted)
	}

	// A session that yielded no identity AND no error explaining why. Retained
	// from the original rule: it is rare and strange, but a cookie without an
	// account behind it is not a usable connection. The branch above takes
	// every case where we know what broke.
	if e.FXRM != "" {
		return loginProof{}, verdictFor(lineupapi.ConnErrBadCredentials)
	}

	return loginProof{}, verdictFor(lineupapi.ConnErrLoginChallengeOrTimeout)
}

// routeFor is the single definition of what each failure class does, so the
// connect task, the session ladder and any future ops surface cannot disagree
// about it.
func routeFor(class string) failureRoute {
	switch class {
	case lineupapi.ConnErrBotChallenge:
		return routeOperator
	case lineupapi.ConnErrVerificationInterrupted:
		return routePostAuth
	case lineupapi.ConnErrCheckFailed:
		return routeInternal
	default:
		return routeTenant
	}
}

// operatorActionableClass reports whether a class describes something only the
// operator can fix.
func operatorActionableClass(class string) bool {
	return routeFor(class) == routeOperator
}

func verdictFor(class string) loginVerdict {
	return loginVerdict{class: class, route: routeFor(class)}
}

func matchesAny(s string, needles []string) bool {
	s = strings.ToLower(s)
	for _, n := range needles {
		if s != "" && strings.Contains(s, n) {
			return true
		}
	}
	return false
}

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
}

func (e loginEvidence) has(probe string) bool { return e.Matched[probe] }

func (e loginEvidence) text(probe string) string { return e.Texts[probe] }

// loginVerdict is a class plus who can act on it.
type loginVerdict struct {
	class string

	// operatorActionable routes the failure. A tenant-actionable failure is
	// recorded against their connection and shown to them; an operator-
	// actionable one must not touch their connection status at all.
	operatorActionable bool
}

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
//  5. Everything else stays ambiguous on purpose.
func classifyLogin(e loginEvidence) loginVerdict {
	if e.FXRM != "" && e.Info != nil && e.Info.UserID != "" {
		return loginVerdict{}
	}

	if e.has("cloudflare") || matchesAny(e.Title, cloudflareTitles) {
		return verdictFor(lineupapi.ConnErrBotChallenge)
	}

	if e.has("otp") {
		return verdictFor(lineupapi.ConnErrTwoFactor)
	}

	// The form is still on screen and told us why. This is the path that makes
	// ConnErrBadCredentials reachable: its previous condition — a cookie
	// present but no identity — describes a session that half-worked, not a
	// password Fantrax refused.
	if e.has("login_form") && (e.text("form_error") != "" || e.text("error_text") != "") {
		return verdictFor(lineupapi.ConnErrBadCredentials)
	}

	// A session that yielded no identity. Retained from the original rule: it
	// is rare and strange, but a cookie without an account behind it is not a
	// usable connection.
	if e.FXRM != "" {
		return verdictFor(lineupapi.ConnErrBadCredentials)
	}

	return verdictFor(lineupapi.ConnErrLoginChallengeOrTimeout)
}

// operatorActionableClass reports whether a class describes something only the
// operator can fix. It is the single definition of that split, so the routing
// in runConnect and any future ops surface cannot disagree about it.
func operatorActionableClass(class string) bool {
	return class == lineupapi.ConnErrBotChallenge
}

func verdictFor(class string) loginVerdict {
	return loginVerdict{class: class, operatorActionable: operatorActionableClass(class)}
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

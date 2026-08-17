package cmd

import (
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestClassifyLogin pins the rule that rosterbot-43q exists to fix.
//
// Before it, every failing login collapsed into ConnErrLoginChallengeOrTimeout,
// because the only fact consulted was the ABSENCE of an FX_RM cookie — which a
// wrong password, a 2FA prompt and a Cloudflare interstitial all produce.
// ConnErrBadCredentials was unreachable: it required a cookie to be present
// while identity was missing, which is not what a rejected password looks like.
//
// The cases below are the four causes stated as evidence rather than as
// absence. The last two are the important ones: an outcome that genuinely does
// not distinguish itself must STAY ambiguous. Replacing one over-confident
// class with a differently over-confident one would be a regression dressed as
// a fix.
func TestClassifyLogin(t *testing.T) {
	const okUser = "fx-123"

	for _, tc := range []struct {
		name         string
		ev           loginEvidence
		wantClass    string
		wantOperator bool
	}{
		{
			name: "a session and an identity is a success",
			ev: loginEvidence{
				FXRM:     "cookie",
				Info:     &fantraxUserInfo{UserID: okUser},
				FinalURL: "https://www.fantrax.com/fantasy/league/x/home",
			},
			wantClass: "",
		},
		{
			name: "cloudflare probe is operator-actionable, not the user's problem",
			ev: loginEvidence{
				Matched:  map[string]bool{"login_form": true, "cloudflare": true},
				FinalURL: "https://www.fantrax.com/login",
				Title:    "Just a moment...",
			},
			wantClass:    lineupapi.ConnErrBotChallenge,
			wantOperator: true,
		},
		{
			name: "cloudflare recognised from the title alone when the DOM markers change",
			ev: loginEvidence{
				FinalURL: "https://www.fantrax.com/login",
				Title:    "Attention Required! | Cloudflare",
			},
			wantClass:    lineupapi.ConnErrBotChallenge,
			wantOperator: true,
		},
		{
			name: "a one-time-code field means the password was ACCEPTED",
			ev: loginEvidence{
				Matched:  map[string]bool{"otp": true},
				FinalURL: "https://www.fantrax.com/login",
			},
			wantClass: lineupapi.ConnErrTwoFactor,
		},
		{
			name: "still on the form with the form's own error is a rejected password",
			ev: loginEvidence{
				Matched:  map[string]bool{"login_form": true, "form_error": true},
				Texts:    map[string]string{"form_error": "Invalid username or password"},
				FinalURL: "https://www.fantrax.com/login",
			},
			wantClass: lineupapi.ConnErrBadCredentials,
		},
		{
			name: "a challenge that times out still classifies from evidence, not from the error",
			ev: loginEvidence{
				Err:      errors.New("context deadline exceeded"),
				Matched:  map[string]bool{"cloudflare": true},
				FinalURL: "https://www.fantrax.com/login",
			},
			wantClass:    lineupapi.ConnErrBotChallenge,
			wantOperator: true,
		},
		{
			name: "still on the form with NO error text stays ambiguous",
			ev: loginEvidence{
				Matched:  map[string]bool{"login_form": true},
				FinalURL: "https://www.fantrax.com/login",
			},
			wantClass: lineupapi.ConnErrLoginChallengeOrTimeout,
		},
		{
			name: "no evidence at all stays ambiguous",
			ev: loginEvidence{
				Err: errors.New("chrome exited"),
			},
			wantClass: lineupapi.ConnErrLoginChallengeOrTimeout,
		},
		{
			name: "a session without an identity is still bad credentials",
			ev: loginEvidence{
				FXRM:     "cookie",
				FinalURL: "https://www.fantrax.com/login",
			},
			wantClass: lineupapi.ConnErrBadCredentials,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLogin(tc.ev)
			if got.class != tc.wantClass {
				t.Errorf("class = %q, want %q", got.class, tc.wantClass)
			}
			if got.operatorActionable != tc.wantOperator {
				t.Errorf("operatorActionable = %v, want %v", got.operatorActionable, tc.wantOperator)
			}
		})
	}
}

// TestClassifyLogin_SuccessNeedsBothHalves guards the one case where being
// permissive would be actively unsafe: a verified connection is what
// AuthorizeRun consults before letting the bot write to somebody's roster, so
// "probably logged in" must never reach it.
func TestClassifyLogin_SuccessNeedsBothHalves(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   loginEvidence
	}{
		{"no session", loginEvidence{Info: &fantraxUserInfo{UserID: "fx-1"}}},
		{"no identity", loginEvidence{FXRM: "cookie"}},
		{"identity with an empty user id", loginEvidence{FXRM: "c", Info: &fantraxUserInfo{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLogin(tc.ev); got.class == "" {
				t.Fatalf("classified as success with %+v", tc.ev)
			}
		})
	}
}

// TestOperatorActionableFailuresDoNotBlameTheUser is the routing rule behind
// rosterbot-crq.14's failure taxonomy, asserted at the only place that can
// enforce it.
//
// Recording needs_reconnect tells a user their credentials are dead and asks
// them to re-enter them. For a Cloudflare block that is both false and useless:
// the credentials are fine, the headless browser is being treated as a bot, and
// nothing the user types can change that. It is the same distinction runConnect
// already draws for a KMS decrypt failure, which deliberately returns a hard
// error rather than blaming the credential.
func TestOperatorActionableFailuresDoNotBlameTheUser(t *testing.T) {
	tenantFacing := []string{
		lineupapi.ConnErrBadCredentials,
		lineupapi.ConnErrTwoFactor,
		lineupapi.ConnErrLoginChallengeOrTimeout,
	}
	for _, class := range tenantFacing {
		if operatorActionableClass(class) {
			t.Errorf("%s routed to the operator; the user can act on it", class)
		}
	}
	if !operatorActionableClass(lineupapi.ConnErrBotChallenge) {
		t.Errorf("%s routed to the user, who cannot act on it", lineupapi.ConnErrBotChallenge)
	}
}

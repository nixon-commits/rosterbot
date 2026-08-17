package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/pmurley/go-fantrax/auth_client"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// sessionState is what a probe learned about the session currently installed in
// the environment.
type sessionState int

const (
	// sessionLive: Fantrax answered as a logged-in user.
	sessionLive sessionState = iota

	// sessionExpired: Fantrax specifically said we are not logged in. This is
	// the ONLY state that may trigger a re-login.
	sessionExpired

	// sessionUnknown: something failed and it was not an authentication answer
	// — an outage, a timeout, a stale API version. Nothing is concluded and
	// nothing is changed.
	sessionUnknown
)

func (s sessionState) String() string {
	switch s {
	case sessionLive:
		return "live"
	case sessionExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// notLoggedInCode is Fantrax's own wire constant for an unauthenticated
// request. It arrives inside a pageError block on an HTTP 200, and reaches us
// only because auth_client.ReadBody puts the code in its error text — which is
// why TestClassifySessionProbe_MatchesTheForksActualError drives the real
// ReadBody rather than a handwritten string.
const notLoggedInCode = "WARNING_NOT_LOGGED_IN"

// classifySessionProbe decides whether a probe failure licenses a re-login.
//
// The default is deliberately sessionUnknown rather than sessionExpired, and
// that asymmetry is the whole safety property. Guessing "expired" during a
// Fantrax outage would drive a fresh headless login on every job of every
// tenant for as long as the outage lasted, and repeated failed logins are
// precisely what triggers Fantrax lockout and Cloudflare bot-blocking. Guessing
// "unknown" when the session really had expired costs one failed job, which the
// next scheduled run fixes.
func classifySessionProbe(err error) sessionState {
	if err == nil {
		return sessionLive
	}
	if strings.Contains(err.Error(), notLoggedInCode) {
		return sessionExpired
	}
	return sessionUnknown
}

// sessionProbe asks Fantrax whether the installed session is still good.
type sessionProbe func(leagueID string) error

// sessionMinter performs one fresh browser login for a tenant.
type sessionMinter func(lineupapi.FantraxCreds) loginEvidence

// sessionLadder is rosterbot-crq.17's credential ladder:
//
//	FX_RM rejected -> re-login with stored credentials -> that fails ->
//	  needs_reconnect, stop every run for that tenant, and DO NOT RETRY
//	  until the user reconnects.
//
// The no-retry rule has no timer and no attempt counter, because it needs
// neither: a failed re-login writes needs_reconnect, and AuthorizeRun then
// refuses every subsequent run on its own. Same shape as opsalert.Streak
// deriving its whole decision from the append-only ledger.
//
// Its collaborators are fields rather than package calls so the entire ladder
// is testable without Fantrax, KMS, DynamoDB or Chrome — none of which the rule
// it encodes actually depends on.
type sessionLadder struct {
	conns  lineupapi.ConnectionStore
	sealer lineupapi.Sealer
	probe  sessionProbe
	mint   sessionMinter
}

// refresh runs the ladder for one tenant. It is called after the sealed session
// has been installed, so the probe is testing the real thing a job will use.
func (l sessionLadder) refresh(ctx context.Context, uid lineupapi.UserID, cfg *config.Config) error {
	if l.probe == nil || l.mint == nil {
		return nil
	}

	state := classifySessionProbe(l.probe(cfg.LeagueID))
	if state == sessionLive {
		return nil
	}
	if state == sessionUnknown {
		// Reported, not acted on. The job's own Fantrax calls will fail with a
		// better message than anything this function could invent, and marking
		// the tenant here would blame them for someone else's downtime.
		fmt.Fprintf(os.Stderr, "tenant %s: session probe inconclusive; leaving the "+
			"session alone and letting the run proceed\n", uid)
		return nil
	}

	conn, ok, err := l.conns.GetConnection(ctx, uid)
	if err != nil {
		return fmt.Errorf("tenant %s: read connection: %w", uid, err)
	}
	if !ok || conn == nil {
		return fmt.Errorf("tenant %s: %w", uid, lineupapi.ErrNoConnection)
	}

	// FANTRAX_COOKIES MUST BE CLEARED BEFORE MINTING. auth_client.GetCookies
	// consults it first, so leaving the expired value in place would make the
	// post-login identity check read the very cookie we just proved dead, and
	// the re-login would fail for a reason that has nothing to do with the
	// credentials.
	os.Unsetenv("FANTRAX_COOKIES")

	ev := l.mint(lineupapi.FantraxCreds{Username: cfg.Username, Password: cfg.Password})
	if v := classifyLogin(ev); v.class != "" {
		printLoginEvidence(os.Stderr, ev)
		return l.stop(ctx, conn, uid, v)
	}

	sealed, err := l.sealer.Seal(ctx, uid, []byte(ev.FXRM))
	if err != nil {
		// The run can still proceed on the session just minted; only the
		// hand-off to the NEXT run is lost. Failing here would discard a
		// working login over a storage problem.
		fmt.Fprintf(os.Stderr, "tenant %s: could not seal the refreshed session (%v); "+
			"this run continues, the next will re-login again\n", uid, err)
		setFantraxEnv(cfg.Username, cfg.Password, fantraxCookieHeader(ev.FXRM))
		return nil
	}

	conn.FXRMCiphertext = sealed
	conn.LastError = ""
	if err := l.conns.PutConnection(ctx, conn); err != nil {
		fmt.Fprintf(os.Stderr, "tenant %s: could not store the refreshed session (%v); "+
			"this run continues, the next will re-login again\n", uid, err)
	}
	setFantraxEnv(cfg.Username, cfg.Password, fantraxCookieHeader(ev.FXRM))
	return nil
}

// stop records a failed re-login and halts the run.
//
// It splits on who can act, exactly as the connect task does. A tenant-
// actionable failure sets needs_reconnect, which is what makes AuthorizeRun
// refuse every later run without any retry bookkeeping. An operator-actionable
// one — a Cloudflare block — must NOT: the tenant's credentials are not
// implicated, and telling them to re-enter a working password would be both
// wrong and useless. Either way this run stops, because either way there is no
// session.
func (l sessionLadder) stop(ctx context.Context, conn *lineupapi.FantraxConnection,
	uid lineupapi.UserID, v loginVerdict) error {

	conn.LastError = v.class
	if !v.operatorActionable {
		conn.Status = lineupapi.ConnNeedsReconnect
	}
	if err := l.conns.PutConnection(ctx, conn); err != nil {
		return fmt.Errorf("tenant %s: re-login failed (%s) and the record could not be updated: %w",
			uid, v.class, err)
	}

	// Fail safe: nothing downstream should be able to authenticate as anyone
	// after this point.
	setFantraxEnv("", "", "")

	if v.operatorActionable {
		return fmt.Errorf("tenant %s: re-login blocked by %s (operator action required; "+
			"tenant credentials not implicated)", uid, v.class)
	}
	return fmt.Errorf("tenant %s: re-login failed (%s); the tenant must reconnect and no "+
		"further run will be attempted until they do", uid, v.class)
}

// liveSessionProbe is the production probe: the cheapest authenticated call
// auth_client makes. NewClient performs a `login` request whose response says
// whether the cookie is still good, so this costs one round trip.
func liveSessionProbe(leagueID string) error {
	_, err := auth_client.NewClient(leagueID, false)
	return err
}

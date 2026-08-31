package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pmurley/go-fantrax/auth_client"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/statestore"
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

	// feed is where a tenant-actionable failure is told to the tenant. A field
	// for the same reason the probe and minter are: "the tenant was told" must
	// be assertable without S3.
	//
	// out is where this ladder's diagnostics go. It exists because the
	// operator-actionable half of a failure is reported HERE rather than
	// pushed (see stop), so the console line is the only artifact a test can
	// hold, and a check whose absence nothing can detect is the failure being
	// fixed. Both are nil-safe, so local dev and existing tests keep working.
	feed feedWriter
	out  io.Writer
}

// errw is the ladder's diagnostic sink, defaulting to stderr.
func (l sessionLadder) errw() io.Writer {
	if l.out != nil {
		return l.out
	}
	return os.Stderr
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
		fmt.Fprintf(l.errw(), "tenant %s: session probe inconclusive; leaving the "+
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
	_ = os.Unsetenv("FANTRAX_COOKIES")

	ev := l.mint(lineupapi.FantraxCreds{Username: cfg.Username, Password: cfg.Password})
	if _, v := classifyLogin(ev); v.class != "" {
		printLoginEvidence(l.errw(), ev)
		return l.stop(ctx, conn, uid, v)
	}

	sealed, err := l.sealer.Seal(ctx, uid, []byte(ev.FXRM))
	if err != nil {
		// The run can still proceed on the session just minted; only the
		// hand-off to the NEXT run is lost. Failing here would discard a
		// working login over a storage problem.
		fmt.Fprintf(l.errw(), "tenant %s: could not seal the refreshed session (%v); "+
			"this run continues, the next will re-login again\n", uid, err)
		setFantraxEnv(cfg.Username, cfg.Password, fantraxCookieHeader(ev.FXRM))
		return nil
	}

	conn.FXRMCiphertext = sealed
	conn.LastError = ""
	if err := l.conns.PutConnection(ctx, conn); err != nil {
		fmt.Fprintf(l.errw(), "tenant %s: could not store the refreshed session (%v); "+
			"this run continues, the next will re-login again\n", uid, err)
	}
	setFantraxEnv(cfg.Username, cfg.Password, fantraxCookieHeader(ev.FXRM))
	return nil
}

// stop records a failed re-login and halts the run.
//
// It splits on who can act, exactly as the connect task does, over the same
// three routes. A tenant-actionable failure sets needs_reconnect, which is what
// makes AuthorizeRun refuse every later run without any retry bookkeeping. An
// operator-actionable one — a Cloudflare block — must NOT: the tenant's
// credentials are not implicated, and telling them to re-enter a working
// password would be both wrong and useless. A post-auth one — Fantrax took the
// login and a later step broke — must not either, and for the same reason.
// Every route stops THIS run, because either way there is no session.
func (l sessionLadder) stop(ctx context.Context, conn *lineupapi.FantraxConnection,
	uid lineupapi.UserID, v loginVerdict) error {

	// TELL WHOEVER CAN ACT — but only the tenant gets a notification here, and
	// the asymmetry is the point.
	//
	// TENANT HALF: until rosterbot-zi4u this function marked the connection
	// broken and stopped the run while notifying nobody, so a session that died
	// mid-week left the tenant's lineups silently unmanaged with the only record
	// of why on a row nobody reads. needs_reconnect makes AuthorizeRun refuse
	// every later run, so exactly one feed entry is written per break.
	//
	// OPERATOR HALF: reported on the console, NOT pushed. This runs inside every
	// scheduled job of every tenant, so a push here would restate a standing
	// condition ~24 times a day for as long as a Cloudflare block lasted — the
	// "30 pushes in 24 hours" failure CLAUDE.md records for the stale-cache
	// alert. It needs no marker either, because the operator is already told on
	// a correctly deduplicated channel: this function returns an error, the task
	// exits non-zero, the ledger records FAILED, and opsalert.Streak fires
	// Started/Escalated exactly once per outage, quoting log_tail's last
	// non-empty line — the error returned below, which names the same class. A
	// second, level-triggered channel could only mute the first.
	//
	// Deliberately does NOT stamp the run onto the connection record. This is an
	// ordinary scheduled run, not a connect, and attributing a connection
	// outcome to an optimize row is the conflation rosterbot-jg92 exists to
	// remove — relocating it here would not make it any less wrong.
	//
	// Either half runs BEFORE the write, like connect's fail(), so a store that
	// rejects the update still tells the person the run stopped.
	switch v.route {
	case routeOperator:
		// Names the session refresh, not "connect": an operator sent to the
		// connect task and the dashboard connect flow finds that nothing
		// happened there, because nothing did.
		fmt.Fprintf(l.errw(), "tenant %s: session re-login blocked by %s — operator action "+
			"required, the tenant's credentials are not implicated (not pushed; the run's "+
			"non-zero exit alerts once per outage via the run ledger)\n", uid, v.class)

	case routePostAuth:
		// THE LADDER'S THIRD ROUTE (rosterbot-ch0s). Fantrax accepted the
		// sign-in and a step after it did not finish, so the credentials are
		// not implicated and the status must not move — which here also means
		// leaving it VERIFIED, so the next scheduled run simply tries again.
		// Writing interrupted (as connect does) would be wrong in the other
		// direction: AuthorizeRun would then refuse every later run for a
		// tenant who has nothing to fix, and nothing would ever retry.
		//
		// Reported on the console and NOT to the tenant's feed, unlike the
		// tenant-actionable branch below. That branch writes exactly one entry
		// per break because needs_reconnect stops every later run; this one
		// leaves the runs going, so a feed entry here would restate a standing
		// Fantrax outage on all ~24 of the day's runs — the level-triggered
		// flood the operator half of this function already avoids. The operator
		// hears about it once per outage through the non-zero exit and
		// opsalert.Streak, and they are the only ones who can act anyway.
		fmt.Fprintf(l.errw(), "tenant %s: fantrax accepted the re-login and %s did not "+
			"finish; leaving the connection alone so the next run retries (not pushed; "+
			"the run's non-zero exit alerts once per outage via the run ledger)\n",
			uid, v.class)

	default:
		recordTenantConnectFailure(ctx, l.tenantFeed(uid), uid, v.class)
	}

	conn.LastError = v.class
	if v.route == routeTenant {
		conn.Status = lineupapi.ConnNeedsReconnect
	}
	if err := l.conns.PutConnection(ctx, conn); err != nil {
		return fmt.Errorf("tenant %s: re-login failed (%s) and the record could not be updated: %w",
			uid, v.class, err)
	}

	// Fail safe: nothing downstream should be able to authenticate as anyone
	// after this point.
	setFantraxEnv("", "", "")

	switch v.route {
	case routeOperator:
		return fmt.Errorf("tenant %s: re-login blocked by %s (operator action required; "+
			"tenant credentials not implicated)", uid, v.class)
	case routePostAuth:
		return fmt.Errorf("tenant %s: re-login reached fantrax and %s (operator action "+
			"required; tenant credentials not implicated, the next run retries)", uid, v.class)
	}
	return fmt.Errorf("tenant %s: re-login failed (%s); the tenant must reconnect and no "+
		"further run will be attempted until they do", uid, v.class)
}

// tenantFeed resolves the tenant's activity feed, defaulting to the real one
// when a caller left it nil.
//
// A nil-default rather than a required constructor argument for the reason
// lineuprun.withDefaults exists: a collaborator a call site can forget is a
// collaborator that reaches nobody, and reaching nobody is the exact defect
// being fixed here. Tests inject fakes; production gets the tenant's own feed
// without tenantcreds having to remember.
//
// Called only from stop's tenant-actionable branch, never unconditionally: it
// builds an S3 client and can print, and doing that on the branch that never
// writes a feed entry would warn about a channel that was never going to be
// used.
func (l sessionLadder) tenantFeed(uid lineupapi.UserID) feedWriter {
	if l.feed != nil {
		return l.feed
	}
	// Composed from the environment, which the fan-out set to THIS tenant, so
	// the store is already theirs — the same reasoning as runConnect's.
	feed, err := statestore.FromEnv().Notifications()
	if err != nil {
		fmt.Fprintf(l.errw(), "tenant %s: activity feed unavailable (%v); the failure is "+
			"recorded on the connection record only\n", uid, err)
		return nil
	}
	return feed
}

// liveSessionProbe is the production probe: the cheapest authenticated call
// auth_client makes. NewClient performs a `login` request whose response says
// whether the cookie is still good, so this costs one round trip.
func liveSessionProbe(leagueID string) error {
	_, err := auth_client.NewClient(leagueID, false)
	return err
}

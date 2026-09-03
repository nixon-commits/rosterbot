package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/kmscreds"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

var connectUser string

// connect verifies one tenant's Fantrax credentials and captures their session
// cookie (rosterbot-crq.12).
//
// It runs as its own ECS task rather than inside the API for a reason that is
// structural, not stylistic: the API Lambda holds a lineupapi.Sealer and no
// Opener, and the IAM behind it grants kms:Encrypt without kms:Decrypt. It
// CANNOT read a credential back, so it cannot verify one. This task holds the
// other half.
//
// It is also the only place a tenant's Fantrax password exists in plaintext, and
// only in memory, only for as long as a login takes.
var connectCmd = &cobra.Command{
	Use:    "connect",
	Short:  "Internal: verify a tenant's Fantrax credentials and capture their session",
	Hidden: true,
	RunE:   runConnect,
}

func init() {
	connectCmd.Flags().StringVar(&connectUser, "user", "", "tenant to connect (required)")
	_ = connectCmd.MarkFlagRequired("user")
	rootCmd.AddCommand(connectCmd)
}

func runConnect(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()
	uid := lineupapi.UserID(connectUser)

	// THE TWO PATHS BELOW CANNOT WRITE ANYTHING DURABLE, and that is a
	// structural fact, not an oversight left over from rosterbot-spb9's fix:
	// PutConnection lives on the store this code has not managed to build
	// yet, so there is nothing to write through. settings.js compensates by
	// bounding the pending copy at connectInFlightWindow (10 minutes,
	// internal/lineupapi/connect.go) instead of promising a resolution that
	// cannot arrive — a crashed task before this point leaves the record
	// pending, and that window is what stops it from reading that way
	// forever.
	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return fmt.Errorf("connect: IDENTITY_TABLE must be set")
	}
	store, err := ddbuser.New(ctx, table)
	if err != nil {
		return err
	}

	// The tenant's own activity feed, built as soon as the store exists so
	// every failure from here on has somewhere durable to land. Composed from
	// the environment, which the launcher set to THIS tenant, so the store is
	// already theirs. Soft: a feed that cannot be opened must not stop
	// connect recording the outcome.
	feed, feedErr := statestore.FromEnv().Notifications()
	if feedErr != nil {
		fmt.Fprintf(out, "note: activity feed unavailable (%v); the outcome is still "+
			"recorded on the connection\n", feedErr)
		feed = nil
	}
	// Only the fields failCheck needs (conns, feed, push, out) — opener,
	// sealer, login and teams are exactly what the two failures below could
	// not construct, so a connectDeps carrying them here would be a lie.
	failDeps := connectDeps{conns: store, feed: feed, push: notifyOperator, out: out}

	// FROM HERE THE STORE EXISTS, so a construction failure is OUR fault and
	// gets a durable record — rosterbot-spb9 extending "every route writes"
	// past the login itself. failCheck reads the tenant's record back on the
	// failure path only (the common path pays no extra round trip) so the
	// status lands beside their sealed credentials rather than replacing them.
	opener, err := kmscreds.NewOpener(ctx)
	if err != nil {
		return failCheck(ctx, failDeps, uid, nil, "constructing the credential opener", err)
	}
	sealer, err := kmscreds.NewSealer(ctx, os.Getenv("FANTRAX_CRED_KEY"))
	if err != nil {
		return failCheck(ctx, failDeps, uid, nil, "constructing the credential sealer", err)
	}

	return connectTenant(ctx, uid, connectDeps{
		conns:  store,
		opener: opener,
		sealer: sealer,
		login:  fantraxLogin,
		teams:  myTeamIDs,
		feed:   feed,
		push:   notifyOperator,
		out:    out,
		now:    time.Now,
	})
}

// connectStore is the slice of the identity store this task needs. An
// interface rather than *ddbuser.Store for the reason tenantDirectory is one:
// every decision below is about what gets WRITTEN to a tenant's record, and
// none of it is assertable against DynamoDB.
type connectStore interface {
	GetConnection(ctx context.Context, uid lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error)
	PutConnection(ctx context.Context, c *lineupapi.FantraxConnection) error
	ClaimTeam(ctx context.Context, id lineupapi.UserID, teamID string) error
	GetUser(ctx context.Context, id lineupapi.UserID) (*lineupapi.User, bool, error)
}

// connectDeps is everything connectTenant talks to. runConnect is the wiring
// adapter over it — the cmd/optimize.go-over-lineuprun.Run shape.
//
// The split exists because the rule this task encodes ("do not blame a tenant's
// credentials for a failure that happened after Fantrax accepted them") is a
// property of the CALL SITE, not of any helper. The previous attempt at
// rosterbot-ch0s tested the helper, so reverting the call site to its buggy
// one-liner left every new test green.
type connectDeps struct {
	conns  connectStore
	opener lineupapi.Opener
	sealer lineupapi.Sealer

	// login and teams are the two Fantrax round trips, seams because Chrome and
	// a real account are not available to a unit test.
	login func(lineupapi.FantraxCreds) loginEvidence
	teams func() ([]string, error)

	feed feedWriter
	push func(string)
	out  io.Writer
	now  func() time.Time
}

// connectRun is one tenant's connect attempt, bound to the record it will
// write.
type connectRun struct {
	connectDeps
	ctx  context.Context
	uid  lineupapi.UserID
	conn *lineupapi.FantraxConnection
}

func (c connectRun) w() io.Writer {
	if c.out != nil {
		return c.out
	}
	return os.Stdout
}

// record is the ONLY way this task writes a connection record.
//
// It stamps the run in the SAME write that sets the outcome, so the two cannot
// come apart. That is what lets GET /v1/runs say what a specific connect row
// concluded rather than showing the ledger's exit status, which reads SUCCESS
// on every tenant-actionable failure by design (rosterbot-jg92).
//
// THE VERDICT IS A PARAMETER, NOT conn.Status. On the operator-actionable route
// fail() deliberately leaves Status alone, so a re-verify of an already-
// verified tenant would carry "verified" into a run that failed and paged —
// this bead's own bug, mirrored. Only the call site knows which route it is on.
//
// The run id is read here rather than passed in: a parameter is something a
// call site can forget, and runConnect itself needs DynamoDB, KMS and a
// headless browser, so no test can drive its call sites.
//
// With no RUN_ID (a hand-run task) it leaves any existing stamp ALONE rather
// than clearing it. The older stamp still states truly what THAT run concluded,
// and the read side matches on id, so it can never be shown against this one.
func (c connectRun) record(verdict string) error {
	if id := os.Getenv("RUN_ID"); id != "" {
		c.conn.LastConnectRun = &lineupapi.ConnectRun{
			RunID:     id,
			Verdict:   verdict,
			LastError: c.conn.LastError,
		}
	}
	return c.conns.PutConnection(c.ctx, c.conn)
}

// connectVerdict is a failure class plus whatever the route it implies needs.
//
// It replaced a bare `class string` so that reverting a call site to
// `fail(lineupapi.ConnErrLoginChallengeOrTimeout)` stops compiling. Be precise
// about what that buys and what it does not: it closes the bare-literal revert,
// NOT the choice of a wrong class — fail(tenantFault(...)) still compiles
// anywhere. The guard against choosing the demoting route after a proven login
// is provenRun below, which has no general-purpose demote, plus fail's runtime
// refusal of a post-auth verdict with no proof.
type connectVerdict struct {
	class string

	// proof is carried by the post-auth route only — it is what fail refuses
	// to record without.
	proof loginProof
	// stage and cause are carried by the post-auth AND internal routes: both
	// describe a specific step that broke and the underlying error, unlike
	// tenantFault's classes, none of which come with either.
	stage string
	cause error
}

// tenantFault is a verdict about the tenant's own credentials or team.
func tenantFault(class string) connectVerdict { return connectVerdict{class: class} }

// postAuth is a verdict about a step that ran AFTER Fantrax accepted the
// sign-in. Its class is fixed rather than a parameter: there is exactly one,
// and a caller that could pass a class here could pass a demoting one.
func postAuth(p loginProof, stage string, cause error) connectVerdict {
	return connectVerdict{
		class: lineupapi.ConnErrVerificationInterrupted,
		proof: p, stage: stage, cause: degradeToNoise(cause),
	}
}

// internalFault is a verdict about a step that broke BEFORE Fantrax was ever
// asked anything — the routeInternal counterpart to postAuth, and built the
// same way for the same reason: its class is fixed rather than a parameter,
// because there is exactly one and a caller that could pass a class here could
// pass a demoting one. Unlike postAuth it carries no loginProof — there is
// none to carry, since nothing was submitted to Fantrax yet — which is exactly
// why this must never be recorded as ConnInterrupted (rosterbot-spb9).
func internalFault(stage string, cause error) connectVerdict {
	return connectVerdict{
		class: lineupapi.ConnErrCheckFailed,
		stage: stage, cause: degradeToNoise(cause),
	}
}

// degradeToNoise is the fallback both stage-carrying verdicts share: a stage
// with no error attached still has to say something, or the ledger's
// log_tail quotes "<nil>".
func degradeToNoise(cause error) error {
	if cause == nil {
		return errors.New("no underlying error was recorded")
	}
	return cause
}

// classified turns a login verdict into a connect verdict, carrying the proof
// through when the class is a post-auth one. It is the mapping the classify
// call site would otherwise have to improvise, and improvising it is how the
// identity-failure case ends up back on the demoting route.
func classified(p loginProof, v loginVerdict, ev loginEvidence) connectVerdict {
	if routeFor(v.class) != routePostAuth {
		return tenantFault(v.class)
	}
	if ev.IdentityErr != nil {
		return postAuth(p, "the Fantrax identity check", ev.IdentityErr)
	}
	return postAuth(p, "the session cookie cache write", ev.PersistErr)
}

// fail records the outcome of a failed connect on one of four routes.
//
// routeTenant returns nil: a connect that legitimately could not authenticate
// is a RESULT, not a task crash. Exiting non-zero would make the run ledger
// show a failed job and page the operator for something only the user can fix.
//
// routeOperator inverts both halves. The tenant's status is left alone (marking
// needs_reconnect would tell someone their working credentials are dead and ask
// them to re-enter them), and the task exits non-zero precisely so the ledger
// records a failure and opsalert pages.
//
// routePostAuth is neither, and is the whole of rosterbot-ch0s. The tenant IS
// told — it is their connection that is unconfirmed and only they can retry it
// — and the operator IS paged, because a Fantrax outage or a KMS blip is theirs
// to fix. The status says interrupted, which is not usable but does not accuse
// a password Fantrax has just accepted.
//
// routeInternal is routePostAuth's mirror image on the OTHER side of the
// login: a fault on our own side stopped the check before Fantrax was ever
// asked anything (rosterbot-spb9). It carries both halves the same way —
// tenant told to retry, operator paged via the non-zero exit — but the status
// says check_failed rather than interrupted, because interrupted's whole
// meaning is a proven Fantrax login and this route has no proof to offer.
//
// EVERY ROUTE WRITES. The 2026-08-26 attempt at this bead added a post-login
// path that wrote nothing at all, leaving the record at ConnPending and the
// dashboard rendering "Checking your credentials…" forever. Not demoting is not
// the same as saying nothing. rosterbot-spb9 found the same gap one step
// earlier: every failure BEFORE the login attempt also wrote nothing.
func (c connectRun) fail(v connectVerdict) error {
	route := routeFor(v.class)

	// Refused BEFORE anything is written or anyone is told. Go cannot stop an
	// in-package literal from forging loginProof{}, so the one route that must
	// never be reachable without evidence refuses the inert value here — the
	// same shape as markVerified's refusals below.
	if route == routePostAuth && v.proof.fxrm == "" {
		return fmt.Errorf("connect: refusing to record %s for %s without proof that "+
			"Fantrax accepted the sign-in", v.class, c.uid)
	}

	// Tell whoever can act, before the write, so a store that rejects the
	// update still tells the person the connect stopped. Tenant-actionable
	// failures go to that user's feed and NOT to the operator, which is the
	// whole point: becoming the help desk for problems users can fix themselves
	// is what this routing exists to prevent (rosterbot-crq.14).
	recordConnectFailure(c.ctx, c.feed, c.push, c.uid, v.class)

	c.conn.LastError = v.class
	switch route {
	case routeOperator:
		if err := c.record(lineupapi.ConnectVerdictFailed); err != nil {
			return err
		}
		return fmt.Errorf("connect: %s for %s (operator action required; "+
			"tenant credentials not implicated)", v.class, c.uid)

	case routePostAuth:
		c.conn.Status = lineupapi.ConnInterrupted
		if err := c.record(lineupapi.ConnectVerdictFailed); err != nil {
			return err
		}
		// Printed as well as returned, and they are not redundant. This line is
		// the diagnostic record inside log_tail's 50 lines; the RETURNED error
		// is what opsalert quotes, because cmd/root.go prints it last and
		// opsalert takes the last non-empty line.
		fmt.Fprintf(c.w(), "connect: fantrax accepted the sign-in for %s and %s did not "+
			"finish; recorded %s, the credentials are not implicated\n",
			c.uid, v.stage, v.class)
		return fmt.Errorf("connect: %s failed after a proven Fantrax login for %s: %w",
			v.stage, c.uid, v.cause)

	case routeInternal:
		// THE FOURTH ROUTE (rosterbot-spb9). Nothing was ever submitted to
		// Fantrax, so — unlike routePostAuth — the status must NOT say
		// interrupted, which claims a proven login this route cannot have.
		c.conn.Status = lineupapi.ConnCheckFailed
		if err := c.record(lineupapi.ConnectVerdictFailed); err != nil {
			return err
		}
		fmt.Fprintf(c.w(), "connect: %s failed for %s before fantrax was ever asked "+
			"anything; recorded %s, the credentials are not implicated\n",
			v.stage, c.uid, v.class)
		return fmt.Errorf("connect: %s failed for %s before the check ever reached "+
			"fantrax (operator action required; tenant credentials not implicated): %w",
			v.stage, c.uid, v.cause)

	default:
		c.conn.Status = lineupapi.ConnNeedsReconnect
		if err := c.record(lineupapi.ConnectVerdictFailed); err != nil {
			return err
		}
		// This is the exit-0 route: nothing here pages the operator, so the
		// only durable statement that THIS run left the tenant needing to act
		// is the one riding on the ledger record itself (rosterbot-cnn9).
		// Without it, opsalert.Streak reads the SUCCESS status at face value
		// and can report the tenant "recovered" while their connection is
		// needs_reconnect — or, under a flapping outage that alternates with
		// tenant-fault runs, never escalate at all.
		recordRunOutcome(lineupapi.RunOutcomeTenantActionable)
		fmt.Fprintf(c.w(), "connect failed for %s: %s\n", c.uid, v.class)
		return nil
	}
}

// failCheck records a connect run that never reached Fantrax because
// something on OUR side broke first, and returns the error the task exits
// with — the routeInternal counterpart to connectRun.fail for the sites that
// have not yet built a connectRun (rosterbot-spb9).
//
// It writes onto the record it can READ BACK, never blind. existing is the
// record a caller already holds (the two connectTenant sites past a successful
// GetConnection); when it is nil this reads the record itself, on the failure
// path only, so the common path pays no extra round trip. Three outcomes: the
// read succeeds and the status lands on the real record, ciphertext and all;
// the record genuinely does not exist and a bare one naming the tenant and
// the status is written, since there is nothing to lose; or the read FAILS,
// and then nothing durable is written. A store that could not be read must
// not be overwritten on a guess: the guess would replace the tenant's sealed
// credentials with an empty record for what may be a DynamoDB blip, a
// transient dressed as permanent, the shape rosterbot-ch0s removed from
// ClaimTeam. On that last path the tenant is still told through the feed, the
// operator through the non-zero exit, and the pending status the dashboard
// shows is bounded by connectInFlightWindow (settings.js states the bound),
// so "every route writes" holds for the two PEOPLE and degrades only for the
// record.
func failCheck(ctx context.Context, d connectDeps, uid lineupapi.UserID,
	existing *lineupapi.FantraxConnection, stage string, cause error) error {

	c := connectRun{connectDeps: d, ctx: ctx, uid: uid, conn: existing}
	if c.conn == nil {
		cur, ok, err := d.conns.GetConnection(ctx, uid)
		switch {
		case err != nil:
			recordConnectFailure(ctx, d.feed, d.push, uid, lineupapi.ConnErrCheckFailed)
			fmt.Fprintf(c.w(), "connect: %s failed for %s before fantrax was ever asked "+
				"anything, and the connection record could not be read back (%v); nothing "+
				"durable written, the credentials are not implicated\n", stage, uid, err)
			return fmt.Errorf("connect: %s failed for %s before the check ever reached "+
				"fantrax, and the connection record could not be read (operator action "+
				"required; tenant credentials not implicated): %w", stage, uid, degradeToNoise(cause))
		case ok:
			c.conn = cur
		default:
			c.conn = &lineupapi.FantraxConnection{UserID: uid}
		}
	}
	return c.fail(internalFault(stage, cause))
}

// connectTenant verifies one tenant's credentials and records the outcome.
func connectTenant(ctx context.Context, uid lineupapi.UserID, d connectDeps) error {
	// Fail safe: nothing downstream should be able to authenticate as this
	// tenant after connect returns. fantraxLogin installs FANTRAX_COOKIES for
	// the identity and ownership calls; this is where it comes back out, the
	// same shape as sessionLadder.stop's clear.
	defer setFantraxEnv("", "", "")

	conn, ok, err := d.conns.GetConnection(ctx, uid)
	if err != nil {
		// Was a bare `return err`, which left the record at ConnPending and
		// the tenant's settings page saying "Checking your credentials…"
		// forever — the same gap rosterbot-ch0s closed one step later in this
		// same function (rosterbot-spb9). There is no `conn` to carry here:
		// the read that would have produced one is exactly what failed.
		return failCheck(ctx, d, uid, nil, "reading the connection record", err)
	}
	if !ok || len(conn.CredsCiphertext) == 0 {
		return fmt.Errorf("connect: %w", lineupapi.ErrNoConnection)
	}
	c := connectRun{connectDeps: d, ctx: ctx, uid: uid, conn: conn}

	plain, err := d.opener.Open(ctx, uid, conn.CredsCiphertext)
	if err != nil {
		// A decrypt failure is infrastructure, not a user error — wrong key,
		// missing grant, or a ciphertext sealed for a different tenant. It must
		// NOT be recorded as needs_reconnect, which would tell the user to
		// re-enter perfectly good credentials. `conn` is already the real
		// record here, so nothing is lost recording through it.
		return failCheck(ctx, d, uid, conn, "decrypting the stored credentials", err)
	}
	var creds lineupapi.FantraxCreds
	if err := json.Unmarshal(plain, &creds); err != nil {
		return failCheck(ctx, d, uid, conn, "parsing the stored credential blob", err)
	}

	ev := d.login(creds)
	if ev.PersistErr != nil {
		// Noise, not a failure. The durable hand-off to the next run is the
		// KMS-sealed FX_RM, not this file, so a cache that could not be written
		// costs the next run one extra login and nothing else.
		fmt.Fprintf(c.w(), "note: the Fantrax cookie cache could not be written (%v); "+
			"the sealed session is unaffected\n", ev.PersistErr)
	}
	proof, v := classifyLogin(ev)
	if v.class != "" {
		// Printed on EVERY failure, including the ambiguous one, and especially
		// the ambiguous one. The selector probes behind this are a hypothesis
		// about markup we do not control, so the only way the classification
		// gets better is if a real failed attempt leaves behind what the page
		// actually looked like. A class that was guessed and never checked is
		// worth no more than the class it replaced.
		printLoginEvidence(c.w(), ev)
		return c.fail(classified(proof, v, ev))
	}
	return c.proven(proof).finish(ev.Info)
}

// provenRun is a connect attempt past the point where Fantrax accepted the
// credentials.
//
// It is a separate type because of what it does NOT have: there is no general
// "demote this tenant" method on it. The two ways to fail from here are
// interrupted (never touches the credentials verdict) and ownershipFault, which
// accepts only the three classes that genuinely describe the tenant's team.
// Anything else is refused at runtime rather than quietly demoting somebody
// whose password Fantrax has just accepted.
type provenRun struct {
	run   connectRun
	proof loginProof
}

func (c connectRun) proven(p loginProof) provenRun { return provenRun{run: c, proof: p} }

// interrupted records a step that failed after the login worked.
func (p provenRun) interrupted(stage string, cause error) error {
	return p.run.fail(postAuth(p.proof, stage, cause))
}

// ownershipFault records the one kind of post-login failure that IS about the
// tenant: Fantrax says these credentials do not control the team they were
// invited for, no team is assigned, or another account holds it. Those demote,
// and should — re-connecting with the right account is the remedy.
func (p provenRun) ownershipFault(class string) error {
	switch class {
	case lineupapi.ConnErrTeamNotOwned, lineupapi.ConnErrNoTeam, lineupapi.ConnErrTeamClaimed:
	default:
		return fmt.Errorf("connect: refusing to blame %s's credentials for %s, which is "+
			"not an ownership failure", p.run.uid, class)
	}
	return p.run.fail(tenantFault(class))
}

// finish runs everything downstream of a proven login.
func (p provenRun) finish(info *fantraxUserInfo) error {
	c := p.run

	// OWNERSHIP IS PROVEN, NOT ASSERTED. The invite carries the team an admin
	// BELIEVES this person manages; MyTeamIDs is Fantrax stating which teams
	// these credentials actually control.
	owned, err := c.teams()
	if err != nil {
		// THE BEAD'S HEADLINE SITE. This used to record
		// login_challenge_or_timeout and needs_reconnect: a network blip
		// fetching MyTeamIDs told the tenant that credentials Fantrax had just
		// accepted no longer work.
		return p.interrupted("the Fantrax team-ownership lookup", err)
	}
	proof, class := proveTeam(c.conn.TeamID, owned)
	if class != "" {
		if class == lineupapi.ConnErrTeamNotOwned {
			fmt.Fprintf(c.w(), "credentials control %v, not %s\n", owned, c.conn.TeamID)
		}
		return p.ownershipFault(class)
	}
	// Claimed from the PROOF, not from conn.TeamID. They hold the same value
	// here, but reading it off the proof means a future edit that drops
	// proveTeam stops compiling instead of silently claiming an unproven team —
	// which is exactly how the previous `if conn.TeamID != ""` guards managed to
	// look deliberate at both sites while nothing rejected an empty team at
	// either.
	if err := c.conns.ClaimTeam(c.ctx, c.uid, proof.teamID); err != nil {
		// THREE OUTCOMES, NOT ONE. ClaimTeam returns ErrTeamTaken for a real
		// conflict, ErrUserConflict when the profile it must update is missing
		// or moved, and a raw store error otherwise. Collapsing them told a
		// tenant "that team is already claimed by another account" for a
		// DynamoDB blip.
		switch {
		case errors.Is(err, lineupapi.ErrTeamTaken):
			return p.ownershipFault(lineupapi.ConnErrTeamClaimed)
		case errors.Is(err, lineupapi.ErrUserConflict):
			// Our bug, not a transient — and that is true now, not merely
			// asserted. ddbuser.ClaimTeam used to map ANY lost optimistic-lock
			// race on the profile update to this same error with no retry, so
			// a routine concurrent write (a passkey registration, a
			// token-version bump landing in the same window) looked identical
			// to a claim that succeeded against a profile that is not there.
			// ClaimTeam now re-reads and re-applies the claim itself, bounded,
			// the same shape as Config.mutateUser — so every retryable race is
			// absorbed before this ever sees it, and reaching here means five
			// consecutive losses against the same profile or a profile that
			// genuinely is not there (rosterbot-spb9). Retrying HERE cannot
			// help, so it must not be dressed as something that can.
			return fmt.Errorf("connect: claimed team %s for %s but its profile could "+
				"not be updated: %w", proof.teamID, c.uid, err)
		default:
			return p.interrupted("the team claim", err)
		}
	}

	// The email is CORROBORATION, not a gate. A manager may legitimately use a
	// different address with Fantrax than the one the admin was given, so a
	// mismatch is recorded for a human to look at rather than failing a
	// connection that is otherwise proven.
	if u, ok, _ := c.conns.GetUser(c.ctx, c.uid); ok && u.Email != "" &&
		!strings.EqualFold(u.Email, info.Email) {
		fmt.Fprintf(c.w(), "note: fantrax email %q differs from the invited address %q\n",
			info.Email, u.Email)
	}

	// Sealed from the PROOF rather than from the evidence, for the reason the
	// team is claimed from its own proof: the session that gets stored is the
	// one Fantrax confirmed, or none.
	sealed, err := c.sealer.Seal(c.ctx, c.uid, []byte(p.proof.fxrm))
	if err != nil {
		// Was a bare `return err`, which left the record at ConnPending and the
		// tenant's settings page saying "Checking your credentials…" with
		// nothing ever to resolve it.
		return p.interrupted("sealing the Fantrax session", err)
	}
	if err := markVerified(c.conn, proof, sealed, info, c.now()); err != nil {
		return err
	}
	// Kept as a raw error deliberately: the store itself is unwritable, so
	// there is nothing to write the outcome ONTO and the non-zero exit is the
	// only honest channel left.
	if err := c.record(lineupapi.ConnectVerdictVerified); err != nil {
		return err
	}

	fmt.Fprintf(c.w(), "connected %s: fantrax user %s, team %s\n", c.uid, info.UserID, c.conn.TeamID)
	return nil
}

// fantraxLogin drives the headless login and returns the FX_RM cookie plus the
// account it belongs to.
//
// The credentials go in via environment variables because that is the only
// input auth_client.GetCookiesWithBrowser accepts — it reads FANTRAX_USERNAME
// and FANTRAX_PASSWORD from the process environment. That is safe here for
// exactly one reason: this task runs for ONE tenant. It is also why the fan-out
// is one ECS task per tenant per job and not a loop in a single process; two
// tenants sharing a process would share these variables and the package-level
// cookie cache with them.
func fantraxLogin(creds lineupapi.FantraxCreds) loginEvidence {
	_ = os.Setenv("FANTRAX_USERNAME", creds.Username)
	_ = os.Setenv("FANTRAX_PASSWORD", creds.Password)
	// Cleared as soon as the browser has them; the process still holds the
	// values in memory, but they stop being visible to anything that reads the
	// environment of this process later in the run.
	defer func() {
		_ = os.Unsetenv("FANTRAX_USERNAME")
		_ = os.Unsetenv("FANTRAX_PASSWORD")
	}()

	ev := runBrowserLogin()
	// A PROVEN COOKIE MEANS PROCEED, even if something after the login failed.
	// This used to also bail on `ev.Err != nil`, which conflated "no session"
	// with "a session plus a later problem" — and the later problem is the one
	// this whole path exists to tell apart. The durable hand-off to the next
	// run is the KMS-sealed FX_RM, not the local cache file.
	if ev.FXRM == "" {
		return ev
	}

	// INSTALLING THE COOKIE IS LOAD-BEARING, NOT HYGIENE. auth_client.GetCookies
	// consults FANTRAX_COOKIES first, then its cache file, and then FALLS BACK
	// TO A FULL SECOND HEADLESS LOGIN. Without this line, both NewClient calls
	// in this flow depend on a cache file that may not have been written — and
	// repeated logins are precisely what credentials.go names as the trigger
	// for Fantrax lockout and Cloudflare bot-blocking. connectTenant clears it
	// again on the way out; the session ladder already does the same thing a
	// few lines after its own mint.
	_ = os.Setenv("FANTRAX_COOKIES", fantraxCookieHeader(ev.FXRM))

	info, err := fantraxIdentity(os.Getenv("FANTRAX_LEAGUE_ID"))
	if err != nil {
		// NOT ev.Err. Fantrax has already issued a cookie, so this is an outage
		// between us and Fantrax, not a verdict on the password — recording it
		// as a browser failure is how a 5xx here became "Fantrax rejected that
		// username or password" (rosterbot-ch0s).
		ev.IdentityErr = err
		return ev
	}
	ev.Info = info
	return ev
}

// runBrowserLogin is the seam over the headless browser.
//
// It exists as a var because everything above it — the classification, the
// routing, the status write — is worth testing and none of it is testable with
// Chrome in the path.
var runBrowserLogin = defaultBrowserLogin

// browserLoginFn is the fork call itself, split out so the triple-to-evidence
// mapping in defaultBrowserLogin is testable without Chrome.
var browserLoginFn = auth_client.LoginWithBrowser

// fantraxIdentity asks Fantrax who the installed session belongs to.
//
// A seam for the same reason: this round trip is the one the bead's second
// named case fails in, and asserting what a failure here does requires being
// able to make it fail.
var fantraxIdentity = defaultFantraxIdentity

func defaultFantraxIdentity(leagueID string) (*fantraxUserInfo, error) {
	client, err := auth_client.NewClient(leagueID, false)
	if err != nil {
		return nil, err
	}
	if client.UserInfo == nil {
		// Unreachable against the pinned fork — NewClient returns a non-nil
		// client only once Login() has assigned UserInfo and checked its UserID
		// — but an error is the honest reading if that ever changes. Leaving
		// Info nil with no error would silently reclassify a Fantrax change as
		// a rejected password.
		return nil, fmt.Errorf("fantrax returned no user info for league %s", leagueID)
	}
	return &fantraxUserInfo{UserID: client.UserInfo.UserID, Email: client.UserInfo.Email}, nil
}

// mkdirCacheDir creates the directory the fork writes its cookie cache into.
//
// A seam because auth_client.CacheDir is a const relative to the working
// directory, so a test cannot redirect it. BELT AND BRACES, not the fix: the
// 2026-08-17 incident's proximate cause was that this directory did not exist
// when the fork called os.Create, and internal/statesync/statesync.go already
// MkdirAlls the local sync directories for exactly that reason. This mirrors
// internal/fantrax/client.go, which has done the same before every auth_client
// call since long before that incident — connect was the one path that did not.
var mkdirCacheDir = func() error { return os.MkdirAll(auth_client.CacheDir, 0o755) }

func defaultBrowserLogin() loginEvidence {
	if err := mkdirCacheDir(); err != nil {
		// Noise, not a verdict, and deliberately not an early return. We have
		// no proof of anything yet, so failing here would classify a broken
		// local filesystem as a login failure and demote the tenant — the exact
		// mis-blame this bead is about. Let the login run; the fork's own
		// os.Create error then arrives with whatever evidence there is.
		fmt.Fprintf(os.Stderr, "warning: could not create %s (%v); the Fantrax cookie "+
			"cache write will fail\n", auth_client.CacheDir, err)
	}

	cookies, outcome, err := browserLoginFn(auth_client.CacheFile, auth_client.DefaultLoginProbes)
	ev := loginEvidence{}
	// The outcome is read even when err is non-nil, because that is the case
	// that most needs describing: a login challenge makes the form never
	// render, which surfaces as a timeout, and the evidence is the only thing
	// that says which challenge it was.
	if outcome != nil {
		ev.FinalURL = outcome.FinalURL
		ev.Title = outcome.Title
		ev.Matched = make(map[string]bool, len(outcome.Matched))
		for _, m := range outcome.Matched {
			ev.Matched[m] = true
		}
		ev.Texts = outcome.Texts
	}
	for _, c := range cookies {
		if c.Name == "FX_RM" {
			ev.FXRM = c.Value
		}
	}
	// WHICH FIELD THE ERROR LANDS IN IS THE DECISION. A cookie in hand means
	// Fantrax accepted the sign-in and the failure came after it; no cookie
	// means we never got a session and the existing evidence probes decide why.
	//
	// HONEST LIMIT: against the pinned fork this cannot yet fire in production.
	// LoginWithBrowser returns `nil, outcome, err` on all three of its
	// post-persistence error paths (os.Create, json.Marshal, f.Write), so the
	// cookie it proved it had is discarded before we see it — which is why the
	// filed 2026-08-17 incident classified as a challenge/timeout. This is the
	// seam the evidence will arrive at once the fork stops discarding it; it is
	// NOT a claim that the filed incident is fixed end to end.
	if err != nil {
		if ev.FXRM != "" {
			ev.PersistErr = err
		} else {
			ev.Err = err
		}
	}
	return ev
}

// printLoginEvidence dumps what the page looked like after a failed attempt.
//
// It prints the ambiguous case too. That case is the one carrying the open
// question — whether Fantrax ever presents 2FA (rosterbot-crq.18) — and it can
// only be answered by a real attempt leaving its evidence somewhere readable.
func printLoginEvidence(out io.Writer, ev loginEvidence) {
	fmt.Fprintf(out, "login evidence: url=%q title=%q\n", ev.FinalURL, ev.Title)
	if ev.Err != nil {
		fmt.Fprintf(out, "login evidence: browser error: %v\n", ev.Err)
	}
	// Printed separately from Err because the distinction is the whole point:
	// these two only exist when Fantrax had already issued a cookie.
	if ev.PersistErr != nil {
		fmt.Fprintf(out, "login evidence: cookie cache write error (after a proven "+
			"login): %v\n", ev.PersistErr)
	}
	if ev.IdentityErr != nil {
		fmt.Fprintf(out, "login evidence: identity check error (after a proven "+
			"login): %v\n", ev.IdentityErr)
	}
	matched := make([]string, 0, len(ev.Matched))
	for name, ok := range ev.Matched {
		if ok {
			matched = append(matched, name)
		}
	}
	sort.Strings(matched)
	fmt.Fprintf(out, "login evidence: probes matched: %v\n", matched)
	texts := make([]string, 0, len(ev.Texts))
	for name := range ev.Texts {
		texts = append(texts, name)
	}
	sort.Strings(texts)
	for _, name := range texts {
		fmt.Fprintf(out, "login evidence: %s: %q\n", name, ev.Texts[name])
	}
}

type fantraxUserInfo struct{ UserID, Email string }

// myTeamIDs asks Fantrax which teams the current session controls.
func myTeamIDs() ([]string, error) {
	client, err := auth_client.NewClient(os.Getenv("FANTRAX_LEAGUE_ID"), false)
	if err != nil {
		return nil, err
	}
	resp, err := client.GetMyTeamRosterInfoRaw("")
	if err != nil {
		return nil, err
	}
	// MyTeamIDs lives on the nested response data, not the envelope. An empty
	// Responses slice means Fantrax answered without the payload — treated as
	// "could not determine ownership" rather than "owns nothing", because the
	// latter would fail a legitimate connection.
	if len(resp.Responses) == 0 {
		return nil, fmt.Errorf("fantrax returned no roster payload")
	}
	return resp.Responses[0].Data.MyTeamIDs, nil
}

// teamProof is evidence that Fantrax itself confirmed this tenant controls a
// specific team. proveTeam is its only constructor, and markVerified is the
// only thing that consumes it, so the verified write cannot be reached without
// the check having run — the same shape as applyAuthorization in
// internal/lineuprun, and for the same reason: an ordering rule that lives only
// in statement order is one revert away from being gone with every test still
// green.
//
// Go cannot stop an in-package literal from forging teamProof{}, so the zero
// value is inert and markVerified refuses it.
type teamProof struct{ teamID string }

// proveTeam decides whether Fantrax has confirmed these credentials control the
// team named on the tenant's record. It returns a usable proof and "" when they
// do, and a zero proof plus a failure class when they do not.
//
// An empty team is a failure, never a pass. Binding the sole owned team instead
// would be defensible, but it discards the admin's invite as an independent
// cross-check on a misdirected invitation — a trade to take deliberately rather
// than a default to fall into (rosterbot-crq.18).
func proveTeam(connTeamID string, owned []string) (teamProof, string) {
	if connTeamID == "" {
		return teamProof{}, lineupapi.ConnErrNoTeam
	}
	if !slices.Contains(owned, connTeamID) {
		return teamProof{}, lineupapi.ConnErrTeamNotOwned
	}
	return teamProof{teamID: connTeamID}, ""
}

// markVerified moves a connection to ConnVerified. It is the single place that
// status is written, and it requires a proof to get there.
//
// Both refusals are programmer errors rather than user ones — a caller reached
// this with no ownership check or no session — so they return an error the task
// surfaces as a failure, rather than recording needs_reconnect and telling a
// user to re-enter credentials that were never the problem.
func markVerified(conn *lineupapi.FantraxConnection, p teamProof, fxrmSealed []byte,
	info *fantraxUserInfo, now time.Time) error {
	if p.teamID == "" {
		return fmt.Errorf("connect: refusing to verify %s without proof of team ownership", conn.UserID)
	}
	if len(fxrmSealed) == 0 {
		return fmt.Errorf("connect: refusing to verify %s with no sealed session", conn.UserID)
	}
	conn.TeamID = p.teamID
	conn.FXRMCiphertext = fxrmSealed
	conn.FantraxUserID = info.UserID
	conn.FantraxEmail = info.Email
	conn.Status = lineupapi.ConnVerified
	conn.LastError = ""
	conn.LastVerifiedAt = now.UTC()
	return nil
}

package cmd

import (
	"context"
	"encoding/json"
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

	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return fmt.Errorf("connect: IDENTITY_TABLE must be set")
	}
	store, err := ddbuser.New(ctx, table)
	if err != nil {
		return err
	}
	opener, err := kmscreds.NewOpener(ctx)
	if err != nil {
		return err
	}
	sealer, err := kmscreds.NewSealer(ctx, os.Getenv("FANTRAX_CRED_KEY"))
	if err != nil {
		return err
	}

	conn, ok, err := store.GetConnection(ctx, uid)
	if err != nil {
		return err
	}
	if !ok || len(conn.CredsCiphertext) == 0 {
		return fmt.Errorf("connect: %w", lineupapi.ErrNoConnection)
	}

	// The tenant's own activity feed. Composed from the environment, which the
	// launcher set to THIS tenant, so the store is already theirs. Soft: a feed
	// that cannot be opened must not stop connect recording the outcome.
	feed, feedErr := statestore.FromEnv().Notifications()
	if feedErr != nil {
		fmt.Fprintf(out, "note: activity feed unavailable (%v); the outcome is still "+
			"recorded on the connection\n", feedErr)
		feed = nil
	}

	// fail records the outcome and returns nil: a connect that legitimately
	// could not authenticate is a RESULT, not a task crash. Exiting non-zero
	// would make the run ledger show a failed job and page the operator for
	// something only the user can fix.
	//
	// The exception is a class only the OPERATOR can act on. There, both halves
	// invert: the tenant's status must be left alone (marking needs_reconnect
	// would tell someone their working credentials are dead and ask them to
	// re-enter them), and the task must exit non-zero precisely so the ledger
	// records a failure and opsalert pages. This is the same split runConnect
	// already draws for a KMS decrypt failure below.
	fail := func(class string) error {
		// Tell whoever can act. Tenant-actionable failures go to that user's
		// feed and NOT to the operator, which is the whole point: becoming the
		// help desk for problems users can fix themselves is what this routing
		// exists to prevent (rosterbot-crq.14).
		recordConnectFailure(ctx, feed, notifyOperator, uid, class)

		conn.LastError = class
		if operatorActionableClass(class) {
			if err := store.PutConnection(ctx, conn); err != nil {
				return err
			}
			return fmt.Errorf("connect: %s for %s (operator action required; "+
				"tenant credentials not implicated)", class, uid)
		}
		conn.Status = lineupapi.ConnNeedsReconnect
		if err := store.PutConnection(ctx, conn); err != nil {
			return err
		}
		fmt.Fprintf(out, "connect failed for %s: %s\n", uid, class)
		return nil
	}

	plain, err := opener.Open(ctx, uid, conn.CredsCiphertext)
	if err != nil {
		// A decrypt failure is infrastructure, not a user error — wrong key,
		// missing grant, or a ciphertext sealed for a different tenant. It must
		// NOT be recorded as needs_reconnect, which would tell the user to
		// re-enter perfectly good credentials.
		return fmt.Errorf("connect: open credentials: %w", err)
	}
	var creds lineupapi.FantraxCreds
	if err := json.Unmarshal(plain, &creds); err != nil {
		return fmt.Errorf("connect: malformed credential blob")
	}

	ev := fantraxLogin(creds)
	if v := classifyLogin(ev); v.class != "" {
		// Printed on EVERY failure, including the ambiguous one, and especially
		// the ambiguous one. The selector probes behind this are a hypothesis
		// about markup we do not control, so the only way the classification
		// gets better is if a real failed attempt leaves behind what the page
		// actually looked like. A class that was guessed and never checked is
		// worth no more than the class it replaced.
		printLoginEvidence(out, ev)
		return fail(v.class)
	}
	fxrm, info := ev.FXRM, ev.Info

	// OWNERSHIP IS PROVEN, NOT ASSERTED. The invite carries the team an admin
	// BELIEVES this person manages; MyTeamIDs is Fantrax stating which teams
	// these credentials actually control.
	owned, err := myTeamIDs()
	if err != nil {
		return fail(lineupapi.ConnErrLoginChallengeOrTimeout)
	}
	proof, class := proveTeam(conn.TeamID, owned)
	if class != "" {
		if class == lineupapi.ConnErrTeamNotOwned {
			fmt.Fprintf(out, "credentials control %v, not %s\n", owned, conn.TeamID)
		}
		return fail(class)
	}
	// Claimed from the PROOF, not from conn.TeamID. They hold the same value
	// here, but reading it off the proof means a future edit that drops
	// proveTeam stops compiling instead of silently claiming an unproven team —
	// which is exactly how the previous `if conn.TeamID != ""` guards managed to
	// look deliberate at both sites while nothing rejected an empty team at
	// either.
	if err := store.ClaimTeam(ctx, uid, proof.teamID); err != nil {
		return fail(lineupapi.ConnErrTeamClaimed)
	}

	// The email is CORROBORATION, not a gate. A manager may legitimately use a
	// different address with Fantrax than the one the admin was given, so a
	// mismatch is recorded for a human to look at rather than failing a
	// connection that is otherwise proven.
	if u, ok, _ := store.GetUser(ctx, uid); ok && u.Email != "" &&
		!strings.EqualFold(u.Email, info.Email) {
		fmt.Fprintf(out, "note: fantrax email %q differs from the invited address %q\n",
			info.Email, u.Email)
	}

	sealed, err := sealer.Seal(ctx, uid, []byte(fxrm))
	if err != nil {
		return err
	}
	if err := markVerified(conn, proof, sealed, info, time.Now()); err != nil {
		return err
	}
	if err := store.PutConnection(ctx, conn); err != nil {
		return err
	}

	fmt.Fprintf(out, "connected %s: fantrax user %s, team %s\n", uid, info.UserID, conn.TeamID)
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
	os.Setenv("FANTRAX_USERNAME", creds.Username)
	os.Setenv("FANTRAX_PASSWORD", creds.Password)
	// Cleared as soon as the browser has them; the process still holds the
	// values in memory, but they stop being visible to anything that reads the
	// environment of this process later in the run.
	defer func() {
		os.Unsetenv("FANTRAX_USERNAME")
		os.Unsetenv("FANTRAX_PASSWORD")
	}()

	ev := runBrowserLogin()
	if ev.Err != nil || ev.FXRM == "" {
		return ev
	}

	client, err := auth_client.NewClient(os.Getenv("FANTRAX_LEAGUE_ID"), false)
	if err != nil {
		ev.Err = err
		return ev
	}
	if client.UserInfo != nil {
		ev.Info = &fantraxUserInfo{UserID: client.UserInfo.UserID, Email: client.UserInfo.Email}
	}
	return ev
}

// runBrowserLogin is the seam over the headless browser.
//
// It exists as a var because everything above it — the classification, the
// routing, the status write — is worth testing and none of it is testable with
// Chrome in the path.
var runBrowserLogin = defaultBrowserLogin

func defaultBrowserLogin() loginEvidence {
	cookies, outcome, err := auth_client.LoginWithBrowser(auth_client.CacheFile, auth_client.DefaultLoginProbes)
	ev := loginEvidence{Err: err}
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

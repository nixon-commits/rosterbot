package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/kmscreds"
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

	// fail records the outcome and returns nil: a connect that legitimately
	// could not authenticate is a RESULT, not a task crash. Exiting non-zero
	// would make the run ledger show a failed job and page the operator for
	// something only the user can fix.
	fail := func(class string) error {
		conn.Status = lineupapi.ConnNeedsReconnect
		conn.LastError = class
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

	fxrm, info, err := fantraxLogin(creds)
	if err != nil {
		return fail(lineupapi.ConnErrLoginChallengeOrTimeout)
	}
	if fxrm == "" {
		// The flow completed but produced no session cookie. Most likely a 2FA
		// prompt or a Cloudflare interstitial the headless browser cannot
		// answer — named separately so that user is diagnosable rather than a
		// mystery.
		return fail(lineupapi.ConnErrLoginChallengeOrTimeout)
	}
	if info == nil || info.UserID == "" {
		return fail(lineupapi.ConnErrBadCredentials)
	}

	// OWNERSHIP IS PROVEN, NOT ASSERTED. The invite carries the team an admin
	// BELIEVES this person manages; MyTeamIDs is Fantrax stating which teams
	// these credentials actually control.
	owned, err := myTeamIDs()
	if err != nil {
		return fail(lineupapi.ConnErrLoginChallengeOrTimeout)
	}
	if conn.TeamID != "" && !contains(owned, conn.TeamID) {
		fmt.Fprintf(out, "credentials control %v, not %s\n", owned, conn.TeamID)
		return fail(lineupapi.ConnErrTeamNotOwned)
	}
	if conn.TeamID != "" {
		if err := store.ClaimTeam(ctx, uid, conn.TeamID); err != nil {
			return fail(lineupapi.ConnErrTeamClaimed)
		}
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
	conn.FXRMCiphertext = sealed
	conn.FantraxUserID = info.UserID
	conn.FantraxEmail = info.Email
	conn.Status = lineupapi.ConnVerified
	conn.LastError = ""
	conn.LastVerifiedAt = time.Now().UTC()
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
func fantraxLogin(creds lineupapi.FantraxCreds) (string, *fantraxUserInfo, error) {
	os.Setenv("FANTRAX_USERNAME", creds.Username)
	os.Setenv("FANTRAX_PASSWORD", creds.Password)
	// Cleared as soon as the browser has them; the process still holds the
	// values in memory, but they stop being visible to anything that reads the
	// environment of this process later in the run.
	defer func() {
		os.Unsetenv("FANTRAX_USERNAME")
		os.Unsetenv("FANTRAX_PASSWORD")
	}()

	cookies, err := auth_client.GetCookiesWithBrowser(auth_client.CacheFile)
	if err != nil {
		return "", nil, err
	}
	var fxrm string
	for _, c := range cookies {
		if c.Name == "FX_RM" {
			fxrm = c.Value
		}
	}
	if fxrm == "" {
		return "", nil, nil
	}

	client, err := auth_client.NewClient(os.Getenv("FANTRAX_LEAGUE_ID"), false)
	if err != nil {
		return fxrm, nil, err
	}
	if client.UserInfo == nil {
		return fxrm, nil, nil
	}
	return fxrm, &fantraxUserInfo{UserID: client.UserInfo.UserID, Email: client.UserInfo.Email}, nil
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

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

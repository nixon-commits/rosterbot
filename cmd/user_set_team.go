package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

var (
	setTeamUser     string
	setTeamTeamID   string
	setTeamLocalDir string
	setTeamDryRun   bool
)

// userCmd groups the admin commands that operate on an existing user. `invite`
// stays top-level because it is the onboarding entry point; everything that
// repairs a record afterwards lives here.
var userCmd = &cobra.Command{
	Use:    "user",
	Short:  "Admin: operate on existing user records",
	Hidden: true,
}

// userSetTeamCmd binds a Fantrax team to a user who already exists.
//
// It exists because rosterbot-crq.18 closed a hole and left no way out of it.
// connect now refuses a user whose record names no team (ConnErrNoTeam) —
// correctly, since an empty team gave the MyTeamIDs ownership check nothing to
// compare and the connection reached verified having proven nothing. But
// `invite` sets TeamID only at creation, and UserStore.ClaimTeam's only caller
// was connect itself, which can no longer reach that line. A teamless record
// was therefore unrepairable without hand-editing DynamoDB.
//
// The operator's own record is the case that matters: neither migrate-identity
// nor the bootstrap profile in ensureUserForIdentity sets a TeamID.
var userSetTeamCmd = &cobra.Command{
	Use:   "set-team",
	Short: "Admin: bind a Fantrax team to an existing user",
	Long: `Bind a Fantrax team to a user that already exists.

The team is still PROVEN at connect time against Fantrax's own MyTeamIDs — this
only records which team to prove. Assigning the wrong one here does not grant
access to it; it makes that user's next connect fail with team_not_owned.`,
	RunE: runUserSetTeam,
}

func init() {
	f := userSetTeamCmd.Flags()
	f.StringVar(&setTeamUser, "user", "", "user id to assign (required)")
	f.StringVar(&setTeamTeamID, "team", "", "Fantrax team id (required)")
	f.StringVar(&setTeamLocalDir, "local-dir", ".lineup", "local directory when STATE_BUCKET is unset")
	f.BoolVar(&setTeamDryRun, "dry-run", false, "report what would change without writing")
	_ = userSetTeamCmd.MarkFlagRequired("user")
	_ = userSetTeamCmd.MarkFlagRequired("team")
	userCmd.AddCommand(userSetTeamCmd)
	rootCmd.AddCommand(userCmd)
}

// openUserStore mirrors openEnrollmentStore: S3/DynamoDB in the deployment,
// a local directory for `serve`-style dev.
func openUserStore(ctx context.Context) (lineupapi.UserStore, error) {
	if statestore.Bucket() == "" {
		return lineupapi.NewFileUserStore(setTeamLocalDir), nil
	}
	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return nil, fmt.Errorf("user set-team: STATE_BUCKET is set but IDENTITY_TABLE is not")
	}
	return ddbuser.New(ctx, table)
}

// setTeam binds teamID to uid, refusing every case that would leave the claim
// index inconsistent.
//
// The reassignment refusal guards a leak in ClaimTeam rather than in this
// command: ClaimTeam writes the new team's claim and updates the profile but
// never releases the OLD team's claim, so moving a user between teams would
// leave the previous team recorded as theirs in the index while their profile
// says otherwise — permanently unassignable to anyone, with nothing pointing at
// why. Refusing keeps that impossible without pushing a release path into
// ClaimTeam, whose other implementation (ddbuser) would need the same change
// and whose only other caller is connect.
func setTeam(ctx context.Context, users lineupapi.UserStore, uid lineupapi.UserID, teamID string) error {
	if uid == "" {
		return fmt.Errorf("user set-team: --user is required")
	}
	// An empty team is precisely the state connect refuses with no_team, so
	// writing one would be a command whose only effect is recreating the bug it
	// exists to fix.
	if teamID == "" {
		return fmt.Errorf("user set-team: --team is required (an empty team is what connect refuses as no_team)")
	}

	u, ok, err := users.GetUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("user set-team: read %s: %w", uid, err)
	}
	if !ok {
		return fmt.Errorf("user set-team: no user %s (invite creates the record; this only repairs one)", uid)
	}
	if u.TeamID != "" && u.TeamID != teamID {
		return fmt.Errorf("user set-team: %s already holds team %s; reassigning would orphan that "+
			"claim because ClaimTeam does not release it. Release it deliberately before reassigning",
			uid, u.TeamID)
	}

	if err := users.ClaimTeam(ctx, uid, teamID); err != nil {
		return fmt.Errorf("user set-team: claim %s for %s: %w", teamID, uid, err)
	}
	return nil
}

func runUserSetTeam(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()
	uid := lineupapi.UserID(setTeamUser)

	users, err := openUserStore(ctx)
	if err != nil {
		return err
	}

	// The dry run reads the record and reports the transition, which is enough
	// to catch the two mistakes that matter — wrong user id, or a user who
	// already holds a different team — before anything is written.
	if setTeamDryRun {
		u, ok, err := users.GetUser(ctx, uid)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("user set-team: no user %s", uid)
		}
		switch {
		case u.TeamID == setTeamTeamID:
			fmt.Fprintf(out, "%s already holds team %s — no change\n", uid, setTeamTeamID)
		case u.TeamID != "":
			fmt.Fprintf(out, "%s holds team %s; reassignment to %s would be REFUSED\n",
				uid, u.TeamID, setTeamTeamID)
		default:
			fmt.Fprintf(out, "would assign team %s to %s (%s)\n", setTeamTeamID, uid, u.Email)
		}
		fmt.Fprintln(out, "dry run — nothing written.")
		return nil
	}

	if err := setTeam(ctx, users, uid, setTeamTeamID); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s now holds team %s (proven against Fantrax at connect time)\n", uid, setTeamTeamID)
	return nil
}

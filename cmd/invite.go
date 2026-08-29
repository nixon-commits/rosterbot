package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

var (
	inviteEmail    string
	inviteName     string
	inviteTeamID   string
	inviteTTL      time.Duration
	inviteLocalDir string
	inviteDryRun   bool
)

// invite mints a single-use enrollment link for one person.
//
// This is the admin half of onboarding, and it is a CLI command rather than a
// dashboard button on purpose: minting is rare, deliberate, and done by one
// person who already has shell access. The link is then delivered out-of-band —
// which is also what attests the email address, since there is no verification
// mail (see the design doc's decision 15).
var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Mint a single-use enrollment link for a new user",
	Long: `Mint a single-use enrollment link.

The printed token is shown ONCE and is not recoverable: only its hash is
stored, so a leak of the identity table yields no usable links. If it is lost,
mint another and let the first expire.

Deliver it out-of-band (text, DM) to the person named by --email. That delivery
is what attests the address — there is no verification mail.`,
	RunE: runInvite,
}

func init() {
	f := inviteCmd.Flags()
	f.StringVar(&inviteEmail, "email", "", "email of the person being invited (required, unique)")
	f.StringVar(&inviteName, "name", "", "display name for the new user")
	f.StringVar(&inviteTeamID, "team", "",
		"Fantrax team id this person manages (proven at connect time; without it they can sign in but cannot connect Fantrax)")
	f.DurationVar(&inviteTTL, "ttl", 72*time.Hour, "how long the link stays valid")
	f.StringVar(&inviteLocalDir, "local-dir", ".lineup", "local directory when STATE_BUCKET is unset")
	f.BoolVar(&inviteDryRun, "dry-run", false, "show what would be created without writing or minting a usable token")
	_ = inviteCmd.MarkFlagRequired("email")
	rootCmd.AddCommand(inviteCmd)
}

func openEnrollmentStore(ctx context.Context) (lineupapi.UserStore, lineupapi.EnrollmentStore, error) {
	if statestore.Bucket() == "" {
		s := lineupapi.NewFileUserStore(inviteLocalDir)
		return s, s, nil
	}
	table := os.Getenv("IDENTITY_TABLE")
	if table == "" {
		return nil, nil, fmt.Errorf("invite: STATE_BUCKET is set but IDENTITY_TABLE is not")
	}
	s, err := ddbuser.New(ctx, table)
	if err != nil {
		return nil, nil, err
	}
	return s, s, nil
}

// enrollmentBackend names the store an invite will be minted against, read from
// the same two environment variables openEnrollmentStore branches on.
//
// The branch is otherwise invisible: a link minted against local files looks
// identical on the terminal, and in the message the admin then sends, to a
// production one — and only fails when the invitee clicks it days later. It is
// derived rather than reported by openEnrollmentStore so the dry run, which
// deliberately constructs no store, can print it too.
func enrollmentBackend() string {
	if statestore.Bucket() == "" {
		return fmt.Sprintf("local files in %s (STATE_BUCKET unset — links minted here "+
			"do not work in production)", inviteLocalDir)
	}
	if table := os.Getenv("IDENTITY_TABLE"); table != "" {
		return fmt.Sprintf("DynamoDB table %s", table)
	}
	return "DynamoDB — but IDENTITY_TABLE is unset, so this will fail"
}

func runInvite(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()

	// Printed FIRST, before anything is created and on the dry run too, because
	// it is the one fact the rest of the output cannot imply: the token, the
	// user id and the expiry look identical whichever store they came from.
	fmt.Fprintf(out, "backend %s\n", enrollmentBackend())

	// A dry run answers "who would this create, and is the email free?" without
	// consuming a user id or leaving a live token behind. It is also what lets
	// `invite` appear in the read-only run-all smoke target at all.
	if inviteDryRun {
		fmt.Fprintf(out, "would create user for %s", inviteEmail)
		if inviteTeamID != "" {
			fmt.Fprintf(out, " on team %s", inviteTeamID)
		}
		fmt.Fprintf(out, ", link valid %s\n", inviteTTL)
		fmt.Fprintln(out, "dry run — nothing written, no token minted.")
		return nil
	}

	users, enrollments, err := openEnrollmentStore(ctx)
	if err != nil {
		return err
	}

	// Create the user up front rather than at redemption. Two reasons: the
	// email and team uniqueness constraints are enforced HERE, so a duplicate
	// is caught while the admin is still looking at the terminal instead of
	// when the invitee clicks a link; and the enrollment token can then be
	// scoped to a real user id, which is the whole point of replacing the
	// global bootstrap token.
	uid, err := newUserID()
	if err != nil {
		return err
	}
	name := inviteName
	if name == "" {
		name = inviteEmail
	}
	u := &lineupapi.User{
		ID: uid, DisplayName: name, Email: inviteEmail,
		Role: lineupapi.RoleMember, Status: lineupapi.UserActive,
		AutoApply: false,
		TeamID:    inviteTeamID,
		CreatedAt: time.Now().UTC(),
	}
	if err := users.CreateUser(ctx, u); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	token, hash, err := lineupapi.MintEnrollmentToken()
	if err != nil {
		return err
	}
	expires := time.Now().UTC().Add(inviteTTL)
	err = enrollments.CreateEnrollment(ctx, hash, lineupapi.Enrollment{
		UserID: uid, TeamID: inviteTeamID, Email: inviteEmail, ExpiresAt: expires,
	})
	if err != nil {
		return fmt.Errorf("create enrollment: %w", err)
	}

	fmt.Fprintf(out, "user    %s\n", uid)
	fmt.Fprintf(out, "email   %s\n", inviteEmail)
	if inviteTeamID != "" {
		fmt.Fprintf(out, "team    %s (proven against Fantrax at connect time)\n", inviteTeamID)
	}
	fmt.Fprintf(out, "expires %s\n\n", expires.Format(time.RFC3339))
	fmt.Fprintf(out, "token   %s\n\n", token)
	fmt.Fprintln(out, "Shown once — only its hash is stored. Deliver it out-of-band.")
	return nil
}

// newUserID mints the 64-byte WebAuthn handle that IS the user id, using the
// same generator registration uses, so an invited user and a self-registered
// one are indistinguishable in shape.
func newUserID() (lineupapi.UserID, error) {
	handle, err := lineupapi.NewWebAuthnUserID()
	if err != nil {
		return "", err
	}
	return lineupapi.NewUserID(handle), nil
}

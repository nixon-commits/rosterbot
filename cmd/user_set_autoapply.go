package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

var (
	setAutoApplyUser    string
	setAutoApplyEnabled bool
	setAutoApplyDisable bool
)

// userSetAutoApplyCmd turns the roster-write switch on or off for one user.
//
// auto_apply is documented as the only field that decides whether this bot
// writes to a roster, and every creation path sets it false deliberately —
// migrate-identity says "turned on by a person, not inherited from a
// migration". This is that person's tool.
//
// Its absence is why the flag went unenforced for so long: with no way to turn
// it on short of hand-editing DynamoDB, nothing could afford to depend on it,
// so the apply path never consulted it and propose-only was a claim rather than
// a behaviour (rosterbot-18wq).
//
// Granting and revoking are the same command because revocation is the case
// that must never be hard: a tenant asking the bot to stop touching their team
// deserves an answer faster than a database edit.
var userSetAutoApplyCmd = &cobra.Command{
	Use:   "set-auto-apply",
	Short: "Admin: allow or forbid the bot to write lineups for a user",
	Long: `Turn the roster-write switch on or off for one user.

auto_apply is what separates a bot that PROPOSES a lineup from one that APPLIES
it to somebody's team. It is off for every new user by design, and turning it on
is an act of consent that should be recorded deliberately rather than inherited.

  --enabled   the bot may apply lineups for this user
  --disabled  the bot may only propose (the default for every new user)`,
	RunE: runUserSetAutoApply,
}

func init() {
	f := userSetAutoApplyCmd.Flags()
	f.StringVar(&setAutoApplyUser, "user", "", "user id to change (required)")
	f.BoolVar(&setAutoApplyEnabled, "enabled", false, "allow the bot to apply lineups")
	f.BoolVar(&setAutoApplyDisable, "disabled", false, "restrict the bot to proposing")
	f.StringVar(&setTeamLocalDir, "local-dir", ".lineup", "local directory when STATE_BUCKET is unset")
	_ = userSetAutoApplyCmd.MarkFlagRequired("user")
	userCmd.AddCommand(userSetAutoApplyCmd)
}

func runUserSetAutoApply(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()

	// Requiring one of the two explicitly, rather than defaulting to "enable",
	// means a mistyped invocation cannot silently grant write access to
	// somebody's roster.
	if setAutoApplyEnabled == setAutoApplyDisable {
		return fmt.Errorf("user set-auto-apply: pass exactly one of --enabled or --disabled")
	}

	users, err := openUserStore(ctx)
	if err != nil {
		return err
	}
	if err := setAutoApply(ctx, users, lineupapi.UserID(setAutoApplyUser), setAutoApplyEnabled); err != nil {
		return err
	}

	if setAutoApplyEnabled {
		fmt.Fprintf(out, "%s: the bot MAY now apply lineups to their roster\n", setAutoApplyUser)
	} else {
		fmt.Fprintf(out, "%s: the bot may now only propose; it will not write to their roster\n", setAutoApplyUser)
	}
	return nil
}

// userGetPutter is the slice of lineupapi.UserStore this operation needs.
//
// Narrower than the full interface on purpose: the whole store carries
// CreateUser and ClaimTeam, and a function that cannot reach them cannot mint a
// profile or move a team binding by accident. It also makes the test double two
// methods instead of seven.
type userGetPutter interface {
	GetUser(ctx context.Context, id lineupapi.UserID) (*lineupapi.User, bool, error)
	PutUser(ctx context.Context, u *lineupapi.User) error
}

// setAutoApply is the decision, extracted so it is testable without DynamoDB.
//
// READ-MODIFY-WRITE, never construct-and-overwrite: PutUser stores a whole
// record, so building a fresh one here would silently erase the team binding
// connect proved, the attested email and the role.
func setAutoApply(ctx context.Context, users userGetPutter, uid lineupapi.UserID, enabled bool) error {
	if uid == "" {
		return fmt.Errorf("user set-auto-apply: --user is required")
	}
	u, ok, err := users.GetUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("user set-auto-apply: read %s: %w", uid, err)
	}
	if !ok {
		// Deliberately not a create. Minting a profile here would produce one
		// with no role, no status and no team that nonetheless has the write
		// switch ON — the worst record to bring into existence by typo.
		return fmt.Errorf("user set-auto-apply: no such user %s", uid)
	}
	u.AutoApply = enabled
	if err := users.PutUser(ctx, u); err != nil {
		return fmt.Errorf("user set-auto-apply: write %s: %w", uid, err)
	}
	return nil
}

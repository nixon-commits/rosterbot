package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

var (
	migrateIdentityApply       bool
	migrateIdentityDisplayName string
	migrateIdentityEmail       string
	migrateIdentityLocalDir    string
)

// migrate-identity promotes the dashboard's single WebAuthn Identity — one
// operator, many device-bound credentials — into the multi-tenant user model
// (rosterbot-crq.7), as user #1 with the admin role.
//
// The flag is --apply rather than --dry-run, inverting migrate-run-ledger's
// default on purpose. That migration copies objects and a mistake costs a
// re-run; this one decides whether the operator can still log in. The
// destructive direction should be the one you have to ask for by name.
//
// It is idempotent: re-running reconciles whatever is missing and leaves the
// rest alone, so a partial run is recovered by running it again.
var migrateIdentityCmd = &cobra.Command{
	Use:    "migrate-identity",
	Short:  "Internal: promote the single WebAuthn identity to user #1 (rosterbot-crq.7)",
	Hidden: true,
	RunE:   runMigrateIdentity,
}

func init() {
	f := migrateIdentityCmd.Flags()
	f.BoolVar(&migrateIdentityApply, "apply", false, "actually write (default is a dry run that only prints the plan)")
	f.StringVar(&migrateIdentityDisplayName, "display-name", "", "display name for the migrated user")
	f.StringVar(&migrateIdentityEmail, "email", "", "email for the migrated user (unique; required on first run)")
	f.StringVar(&migrateIdentityLocalDir, "local-dir", ".lineup", "local directory when STATE_BUCKET is unset")
	rootCmd.AddCommand(migrateIdentityCmd)
}

// openStores picks source and destination the same way the composition roots
// do: S3 + DynamoDB on AWS, local files otherwise. The local path is not a
// convenience — it is how this migration gets exercised against a real-shaped
// identity record before it is ever pointed at production.
func openStores(ctx context.Context) (lineupapi.IdentityStore, lineupapi.UserStore, error) {
	bucket := statestore.Bucket()
	table := os.Getenv("IDENTITY_TABLE")

	if bucket == "" {
		return lineupapi.NewFileIdentityStore(migrateIdentityLocalDir),
			lineupapi.NewFileUserStore(migrateIdentityLocalDir), nil
	}
	if table == "" {
		return nil, nil, fmt.Errorf("migrate-identity: STATE_BUCKET is set but IDENTITY_TABLE is not; " +
			"refusing to guess where the users live")
	}
	src, err := s3lineup.NewIdentity(ctx, bucket, "webauthn/")
	if err != nil {
		return nil, nil, err
	}
	dst, err := ddbuser.New(ctx, table)
	if err != nil {
		return nil, nil, err
	}
	return src, dst, nil
}

func runMigrateIdentity(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()

	src, dst, err := openStores(ctx)
	if err != nil {
		return err
	}

	identity, ok, err := src.GetIdentity(ctx)
	if err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	if !ok {
		fmt.Fprintln(out, "no existing identity record; nothing to migrate")
		return nil
	}

	// The user id IS the WebAuthn handle. Preserving it byte-for-byte is the
	// entire safety requirement: every registered passkey is cryptographically
	// bound to this handle, so a migration that mints a fresh one would leave
	// the operator holding credentials that authenticate to nobody.
	uid := lineupapi.NewUserID(identity.WebAuthnUserID)

	fmt.Fprintf(out, "source identity: handle %d bytes, %d credential(s)\n",
		len(identity.WebAuthnUserID), len(identity.Credentials))
	fmt.Fprintf(out, "target user id : %s\n", uid)

	existing, userExists, err := dst.GetUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("read destination user: %w", err)
	}

	// Plan the profile.
	var createProfile bool
	switch {
	case userExists:
		fmt.Fprintf(out, "profile        : already present (%s, role=%s) — leaving alone\n",
			existing.DisplayName, existing.Role)
	default:
		if migrateIdentityEmail == "" {
			return fmt.Errorf("migrate-identity: --email is required to create the user " +
				"(it is unique-constrained, so it cannot be filled in later without a second migration)")
		}
		createProfile = true
		name := migrateIdentityDisplayName
		if name == "" {
			name = "operator"
		}
		fmt.Fprintf(out, "profile        : CREATE %s <%s> role=admin status=active auto_apply=false\n",
			name, migrateIdentityEmail)
	}

	// Plan the credentials. Compare by credential id, never by count: two
	// different passkeys and one duplicated passkey both count as two.
	have := map[string]bool{}
	if userExists {
		creds, err := dst.Credentials(ctx, uid)
		if err != nil {
			return fmt.Errorf("read destination credentials: %w", err)
		}
		for _, c := range creds {
			have[string(c.ID)] = true
		}
	}
	var missing []int
	for i, c := range identity.Credentials {
		if have[string(c.ID)] {
			fmt.Fprintf(out, "credential %-2d  : already present\n", i)
			continue
		}
		missing = append(missing, i)
		fmt.Fprintf(out, "credential %-2d  : COPY (sign count %d)\n", i, c.Authenticator.SignCount)
	}

	if !migrateIdentityApply {
		fmt.Fprintf(out, "\ndry run — nothing written. %d profile, %d credential(s) would change.\n",
			boolToInt(createProfile), len(missing))
		fmt.Fprintln(out, "re-run with --apply to write.")
		return nil
	}

	if createProfile {
		name := migrateIdentityDisplayName
		if name == "" {
			name = "operator"
		}
		u := &lineupapi.User{
			ID:          uid,
			DisplayName: name,
			Email:       migrateIdentityEmail,
			Role:        lineupapi.RoleAdmin,
			Status:      lineupapi.UserActive,
			// Deliberately false even for the operator. auto_apply is the switch
			// that lets the bot write to a roster, and it should be turned on by
			// a person, not inherited from a migration.
			AutoApply: false,
		}
		if err := dst.CreateUser(ctx, u); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		fmt.Fprintln(out, "created profile")
	}
	for _, i := range missing {
		if err := dst.PutCredential(ctx, uid, identity.Credentials[i]); err != nil {
			return fmt.Errorf("copy credential %d: %w", i, err)
		}
		fmt.Fprintf(out, "copied credential %d\n", i)
	}

	return verifyMigration(ctx, out, dst, uid, identity)
}

// verifyMigration re-reads the destination and proves the migration landed,
// rather than trusting that the writes returned nil.
//
// It checks credential IDS, not a count. A count matches just as happily when
// one credential was written twice and another dropped — and the symptom of
// that is a passkey which silently stops working on one device, discovered by
// the operator at the moment they need it.
func verifyMigration(ctx context.Context, out interface{ Write([]byte) (int, error) },
	dst lineupapi.UserStore, uid lineupapi.UserID, identity *lineupapi.Identity) error {

	u, ok, err := dst.GetUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("verify: read user: %w", err)
	}
	if !ok {
		return fmt.Errorf("verify: user %s is absent after migration", uid)
	}
	if !bytes.Equal(u.ID.WebAuthnHandle(), identity.WebAuthnUserID) {
		return fmt.Errorf("verify: stored handle does not round-trip to the source handle; " +
			"every existing passkey is bound to the source handle and would stop authenticating")
	}

	got, err := dst.Credentials(ctx, uid)
	if err != nil {
		return fmt.Errorf("verify: read credentials: %w", err)
	}
	found := map[string]bool{}
	for _, c := range got {
		found[string(c.ID)] = true
	}
	for i, want := range identity.Credentials {
		if !found[string(want.ID)] {
			return fmt.Errorf("verify: credential %d is missing from the destination; "+
				"that passkey would no longer log in", i)
		}
		// The reverse index is what the non-discoverable fallback resolves
		// through. A credential present but unindexed still works on the
		// everyday discoverable path, so this failure would hide until a
		// device that needs the fallback tried to log in.
		owner, ok, err := dst.UserByCredential(ctx, want.ID)
		if err != nil {
			return fmt.Errorf("verify: credential index lookup: %w", err)
		}
		if !ok || owner != uid {
			return fmt.Errorf("verify: credential %d resolves to %q (found=%v), want %q",
				i, owner, ok, uid)
		}
	}

	fmt.Fprintf(out, "\nverified: user %s holds %d credential(s), all indexed\n", uid, len(got))
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

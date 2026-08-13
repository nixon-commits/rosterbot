package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// seedIdentity writes a singleton identity record in the shape
// FileIdentityStore reads, with a real 64-byte handle and n credentials.
func seedIdentity(t *testing.T, dir string, n int) *lineupapi.Identity {
	t.Helper()
	handle := make([]byte, 64)
	if _, err := rand.Read(handle); err != nil {
		t.Fatal(err)
	}
	id := &lineupapi.Identity{WebAuthnUserID: handle}
	for i := 0; i < n; i++ {
		id.Credentials = append(id.Credentials, webauthn.Credential{
			ID:        []byte{byte('a' + i), 0x01, 0x02},
			PublicKey: []byte{byte('k' + i)},
		})
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "webauthn-identity.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

// runMigrate drives the command the way an operator would, through its flags.
func runMigrate(t *testing.T, dir string, apply bool) string {
	t.Helper()
	migrateIdentityLocalDir = dir
	migrateIdentityEmail = "op@example.test"
	migrateIdentityDisplayName = "Operator"
	migrateIdentityApply = apply
	t.Cleanup(func() { migrateIdentityApply = false })

	var out bytes.Buffer
	migrateIdentityCmd.SetOut(&out)
	if err := runMigrateIdentity(migrateIdentityCmd, nil); err != nil {
		t.Fatalf("migrate (apply=%v): %v", apply, err)
	}
	return out.String()
}

// TestMigrateIdentity_PreservesTheHandleAndEveryCredential is the whole point
// of the migration. Every registered passkey is cryptographically bound to the
// WebAuthn user handle, so a migration that mints a fresh id — or drops a
// credential — leaves the operator holding keys that authenticate to nobody,
// with the dashboard's only other way in being the bearer token.
func TestMigrateIdentity_PreservesTheHandleAndEveryCredential(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	dir := t.TempDir()
	src := seedIdentity(t, dir, 3)

	runMigrate(t, dir, true)

	ctx := context.Background()
	store := lineupapi.NewFileUserStore(dir)
	uid := lineupapi.NewUserID(src.WebAuthnUserID)

	u, ok, err := store.GetUser(ctx, uid)
	if err != nil || !ok {
		t.Fatalf("GetUser(%s) after migration: ok=%v err=%v", uid, ok, err)
	}
	if !bytes.Equal(u.ID.WebAuthnHandle(), src.WebAuthnUserID) {
		t.Fatal("migrated user id does not decode back to the source handle; " +
			"every existing passkey would stop authenticating")
	}
	if u.Role != lineupapi.RoleAdmin {
		t.Errorf("Role = %q, want admin — the migrated operator is the only account "+
			"that can mint invites, so a member role locks onboarding out", u.Role)
	}
	if u.AutoApply {
		t.Error("AutoApply = true; the switch that lets the bot write to a roster " +
			"must be turned on by a person, not inherited from a migration")
	}

	creds, err := store.Credentials(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != len(src.Credentials) {
		t.Fatalf("got %d credentials, want %d", len(creds), len(src.Credentials))
	}
	for _, want := range src.Credentials {
		var found bool
		for _, got := range creds {
			if bytes.Equal(got.ID, want.ID) && bytes.Equal(got.PublicKey, want.PublicKey) {
				found = true
			}
		}
		if !found {
			t.Errorf("credential %x missing or altered; that passkey would no longer log in", want.ID)
		}
		// The reverse index backs the non-discoverable login path. A credential
		// present but unindexed still works for discoverable logins, so this
		// gap would stay hidden until a device that needs the fallback tried.
		owner, ok, err := store.UserByCredential(ctx, want.ID)
		if err != nil || !ok || owner != uid {
			t.Errorf("UserByCredential(%x) = (%q, %v, %v), want (%q, true, nil)",
				want.ID, owner, ok, err, uid)
		}
	}
}

// TestMigrateIdentity_DryRunWritesNothing guards the default. The command is
// most likely to be run for the first time against production, by someone
// checking what it would do.
func TestMigrateIdentity_DryRunWritesNothing(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	dir := t.TempDir()
	src := seedIdentity(t, dir, 1)

	out := runMigrate(t, dir, false)
	if !bytes.Contains([]byte(out), []byte("dry run")) {
		t.Errorf("dry run output did not say so:\n%s", out)
	}

	_, ok, err := lineupapi.NewFileUserStore(dir).GetUser(context.Background(),
		lineupapi.NewUserID(src.WebAuthnUserID))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a dry run created the user; --apply must be the only thing that writes")
	}
}

// TestMigrateIdentity_IsIdempotent covers the realistic failure: a first run
// that dies partway. Recovery has to be "run it again", not manual repair of a
// half-populated table.
func TestMigrateIdentity_IsIdempotent(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	dir := t.TempDir()
	src := seedIdentity(t, dir, 2)

	runMigrate(t, dir, true)
	out := runMigrate(t, dir, true)

	if !bytes.Contains([]byte(out), []byte("already present")) {
		t.Errorf("second run did not recognise existing state:\n%s", out)
	}
	creds, err := lineupapi.NewFileUserStore(dir).Credentials(context.Background(),
		lineupapi.NewUserID(src.WebAuthnUserID))
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("got %d credentials after two runs, want 2 — the migration duplicated "+
			"or dropped on re-run", len(creds))
	}
}

// TestMigrateIdentity_NoIdentityIsNotAnError covers a fresh deployment, where
// there is simply nothing to promote. Failing there would make the migration a
// blocker on a clean install.
func TestMigrateIdentity_NoIdentityIsNotAnError(t *testing.T) {
	t.Setenv("STATE_BUCKET", "")
	out := runMigrate(t, t.TempDir(), true)
	if !bytes.Contains([]byte(out), []byte("nothing to migrate")) {
		t.Errorf("empty source did not report cleanly:\n%s", out)
	}
}

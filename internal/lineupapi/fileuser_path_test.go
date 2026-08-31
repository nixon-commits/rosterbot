package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileUserStore_ContainsPathTraversal is the CodeQL go/path-injection
// finding on readProfile (alert #16).
//
// userDir built filepath.Join(dir, "users", string(id)) straight from the id,
// and Join calls Clean — so "../../x" does not stay a literal directory name,
// it escapes. Every path in this store funnels through userDir, so the profile
// read, the profile WRITE and the credential listing were all affected.
//
// A real UserID is base64url (NewUserID encodes the WebAuthn handle), which
// contains no dot or slash, so the store's own callers cannot produce this. The
// exposure is anything that supplies an id from outside that constructor — the
// --user admin flags, a hand-edited store, a future caller. That is exactly the
// kind of "cannot happen today" that a path-injection finding exists to stop
// from becoming "happened once".
func TestFileUserStore_ContainsPathTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "profile.json")
	if err := os.WriteFile(secret, []byte(`{"id":"victim","role":"admin"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewFileUserStore(filepath.Join(root, "store"))

	// Aimed squarely at the file planted above.
	evil := UserID("../../outside")
	if _, ok, err := s.GetUser(context.Background(), evil); err != nil || ok {
		t.Errorf("GetUser(%q) reached a profile outside the store (ok=%v, err=%v)", evil, ok, err)
	}
}

// TestFileUserStore_TraversalWriteStaysInside is the write half, which matters
// more: a read leaks, a write DESTROYS. CreateUser must not be able to place a
// profile.json outside the store's directory.
func TestFileUserStore_TraversalWriteStaysInside(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	s := NewFileUserStore(store)

	u := &User{ID: UserID("../../escaped"), Email: "e@example.test", Status: UserActive}
	_ = s.CreateUser(context.Background(), u)

	// Whatever happened, nothing may exist above the store directory.
	strays, err := scanStrays(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(strays) != 0 {
		t.Errorf("wrote outside the store: %v", strays)
	}
}

// scanStrays walks root and returns every profile.json that is not inside
// store. It exists as a helper rather than an inline walk so its failure mode
// is assertable: the walk error is RETURNED, not swallowed.
//
// The swallow it replaces was latent hardening rather than a live hole — every
// directory under root is created by these tests under t.TempDir() with
// ordinary permissions, so today's fixture cannot make the walk go blind. But
// the assertion above it is written as an absolute ("nothing may exist above
// the store directory"), and an unreadable directory makes filepath.Walk skip
// its whole subtree, so a stray inside one would be reported as zero strays.
// A security assertion that cannot look must say so instead of reporting clean.
func scanStrays(root, store string) ([]string, error) {
	var strays []string
	err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, "profile.json") && !strings.HasPrefix(p, store+string(os.PathSeparator)) {
			strays = append(strays, p)
		}
		return nil
	})
	return strays, err
}

// TestFileUserStore_TraversalScanSurfacesUnreadableDirs is the mutation guard
// on scanStrays: it plants a profile.json inside a directory the walk cannot
// read and requires the scan to fail rather than report clean.
//
// It drives the helper rather than TestFileUserStore_TraversalWriteStaysInside,
// because that test's response to the condition is t.Fatal, which no other test
// can observe.
//
// The root skip does not quietly disable this in CI: .github/workflows/ci.yml
// runs on ubuntu-latest as a non-root user, so the chmod bites there. It is a
// skip rather than a silent pass because root ignores permission bits entirely,
// which would make this test pass whether or not the helper returns the error.
// If this ever moves to a root container the skip becomes visible in the test
// output — noise, not silence — and the guard must be rebuilt on something
// other than file modes.
func TestFileUserStore_TraversalScanSurfacesUnreadableDirs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0o000 does not block the walk, so this guard cannot fail")
	}
	root := t.TempDir()
	hidden := filepath.Join(root, "hidden")
	if err := os.Mkdir(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "profile.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hidden, 0o000); err != nil {
		t.Fatal(err)
	}
	// Registered after t.TempDir, so LIFO cleanup restores the mode before
	// TempDir's RemoveAll runs — otherwise the removal fails and errors the
	// test independently of the assertion below.
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })

	if _, err := scanStrays(root, filepath.Join(root, "store")); err == nil {
		t.Fatal("scanStrays reported success over a directory it could not read: a stray profile.json inside it would be invisible, and the caller would read that as \"nothing above the store\"")
	}
}

// TestFileUserStore_OrdinaryIDsStillRoundTrip guards against fixing traversal
// by breaking the store: a real base64url id must still create, read back and
// keep its fields.
func TestFileUserStore_OrdinaryIDsStillRoundTrip(t *testing.T) {
	s := NewFileUserStore(t.TempDir())
	id := NewUserID([]byte("a-real-webauthn-handle"))
	want := &User{ID: id, DisplayName: "Alice", Email: "alice@example.test",
		Role: RoleAdmin, Status: UserActive}

	if err := s.CreateUser(context.Background(), want); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	got, ok, err := s.GetUser(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if got.DisplayName != "Alice" || got.Role != RoleAdmin || got.Email != "alice@example.test" {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

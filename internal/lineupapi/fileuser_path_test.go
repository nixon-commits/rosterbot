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
	var strays []string
	_ = filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(p, "profile.json") && !strings.HasPrefix(p, store+string(os.PathSeparator)) {
			strays = append(strays, p)
		}
		return nil
	})
	if len(strays) != 0 {
		t.Errorf("wrote outside the store: %v", strays)
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

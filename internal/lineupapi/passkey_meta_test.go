package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// passkeyRow mirrors what the settings page reads off GET /v1/auth/passkeys.
type passkeyRow struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func listPasskeys(t *testing.T, h http.Handler, secret []byte, uid string) []passkeyRow {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/auth/passkeys", UserID(uid), 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("list passkeys = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Passkeys []passkeyRow `json:"passkeys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return body.Passkeys
}

// TestListPasskeys_CarriesNameAndCreatedAt: the settings page can only show
// what this endpoint returns, and a passkey registered before metadata existed
// must read as unannotated — no name, no invented date — rather than erroring.
func TestListPasskeys_CarriesNameAndCreatedAt(t *testing.T) {
	h, secret, users := adminFixture(t)
	t0 := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	named := webauthn.Credential{ID: []byte("named-cred")}
	bare := webauthn.Credential{ID: []byte("bare-cred")}
	for _, c := range []webauthn.Credential{named, bare} {
		if err := users.PutCredential(context.Background(), "bob", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := users.PutCredentialMeta(context.Background(), "bob", named.ID,
		CredentialMeta{Name: "Bob's phone", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}

	rows := listPasskeys(t, h, secret, "bob")
	byID := map[string]passkeyRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	got := byID[CredentialKey(named.ID)]
	if got.Name != "Bob's phone" || !got.CreatedAt.Equal(t0) {
		t.Errorf("annotated passkey = %+v, want name and created_at", got)
	}
	pre := byID[CredentialKey(bare.ID)]
	if pre.Name != "" || !pre.CreatedAt.IsZero() {
		t.Errorf("pre-metadata passkey = %+v, want no name and no invented date", pre)
	}
}

// TestRenamePasskey_SetsTheNameAndPreservesCreatedAt: renaming must touch the
// name alone — the registration date is a fact, not a preference.
func TestRenamePasskey_SetsTheNameAndPreservesCreatedAt(t *testing.T) {
	h, secret, users := adminFixture(t)
	t0 := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	c := webauthn.Credential{ID: []byte("cred-rn")}
	if err := users.PutCredential(context.Background(), "bob", c); err != nil {
		t.Fatal(err)
	}
	if err := users.PutCredentialMeta(context.Background(), "bob", c.ID,
		CredentialMeta{Name: "old", CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/auth/passkeys/"+CredentialKey(c.ID)+"/name",
		"bob", `{"name":"MacBook"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d, want 200: %s", rec.Code, rec.Body)
	}

	rows := listPasskeys(t, h, secret, "bob")
	if len(rows) != 1 || rows[0].Name != "MacBook" || !rows[0].CreatedAt.Equal(t0) {
		t.Errorf("after rename = %+v, want name MacBook with created_at preserved", rows)
	}
}

// TestRenamePasskey_RefusesACredentialTheCallerDoesNotOwn: naming an id that
// is not among the caller's own credentials must 404 rather than write meta
// for a passkey that does not exist under their account — orphan meta is how
// a later registration inherits a stranger's label.
func TestRenamePasskey_RefusesACredentialTheCallerDoesNotOwn(t *testing.T) {
	h, secret, users := adminFixture(t)
	c := webauthn.Credential{ID: []byte("admins-cred")}
	if err := users.PutCredential(context.Background(), "admin1", c); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/auth/passkeys/"+CredentialKey(c.ID)+"/name",
		"bob", `{"name":"mine now"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("renaming another user's credential = %d, want 404: %s", rec.Code, rec.Body)
	}
	metas, err := users.CredentialMetas(context.Background(), "bob")
	if err != nil || len(metas) != 0 {
		t.Errorf("orphan meta written under the caller: %v (%v)", metas, err)
	}
}

// TestRenamePasskey_RejectsAnOverlongName: the name renders in a table cell
// and the OS passkey picker analogy caps out far below essay length.
func TestRenamePasskey_RejectsAnOverlongName(t *testing.T) {
	h, secret, users := adminFixture(t)
	c := webauthn.Credential{ID: []byte("cred-long")}
	if err := users.PutCredential(context.Background(), "bob", c); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/auth/passkeys/"+CredentialKey(c.ID)+"/name",
		"bob", `{"name":"`+strings.Repeat("x", 100)+`"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("overlong name = %d, want 400", rec.Code)
	}
}

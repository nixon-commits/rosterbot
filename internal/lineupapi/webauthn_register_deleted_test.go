package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// deletedMidCeremony is a user store in which the account disappears at the
// worst moment: after register/finish has read it, immediately before the
// credential is written. That is what POST /v1/signup's cleanup or
// replace-on-retry does to a passkey-less signup account from another request.
type deletedMidCeremony struct{ *FileUserStore }

func (s deletedMidCeremony) PutCredential(ctx context.Context, id UserID, c webauthn.Credential) error {
	if err := s.DeleteUser(ctx, id); err != nil {
		return err
	}
	return s.FileUserStore.PutCredential(ctx, id, c)
}

// TestRegisterFinish_RefusesAnAccountDeletedDuringTheCeremony: without the
// re-read, finish answered "registered" with a session cookie for an account
// that no longer existed, and left a credential nothing lists or sweeps.
func TestRegisterFinish_RefusesAnAccountDeletedDuringTheCeremony(t *testing.T) {
	alice := testUserID("alice")
	files := NewFileUserStore(t.TempDir())
	if err := files.CreateUser(context.Background(), &User{
		ID: alice, Email: "a@example.test", Role: RoleMember, Status: UserActive,
		Origin: UserOriginSignup,
	}); err != nil {
		t.Fatal(err)
	}
	tok, hash, err := MintEnrollmentToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := files.CreateEnrollment(context.Background(), hash,
		Enrollment{UserID: alice, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Users: deletedMidCeremony{files}, Enrollments: files,
		WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	extra := map[string]any{"token": string(tok)}

	beginRec := httptest.NewRecorder()
	h.ServeHTTP(beginRec, jsonPost(t, "/v1/auth/register/begin", extra, nil))
	if beginRec.Code != http.StatusOK {
		t.Fatalf("register/begin = %d: %s", beginRec.Code, beginRec.Body)
	}
	var begin struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	body := fabricateAttestation(t, "localhost", "http://localhost:8080",
		begin.PublicKey.Challenge, []byte("cred-for-a-ghost"))
	body["token"] = string(tok)

	finishRec := httptest.NewRecorder()
	h.ServeHTTP(finishRec, jsonPost(t, "/v1/auth/register/finish", body, beginRec.Result().Cookies()))

	if finishRec.Code != http.StatusForbidden {
		t.Fatalf("register/finish = %d (%s), want 403 for an account that is gone",
			finishRec.Code, finishRec.Body)
	}
	for _, c := range finishRec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Error("a session cookie was issued for a deleted account")
		}
	}
	creds, err := files.Credentials(context.Background(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Errorf("%d credential(s) left behind for a deleted account", len(creds))
	}
	if _, ok, _ := files.UserByCredential(context.Background(), []byte("cred-for-a-ghost")); ok {
		t.Error("the credential index still resolves the ghost's passkey")
	}
}

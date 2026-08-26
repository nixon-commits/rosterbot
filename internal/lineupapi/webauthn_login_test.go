package lineupapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

// testUserHandle is a UserID whose handle actually round-trips.
//
// UserID IS the base64url of the raw handle (see NewUserID / WebAuthnHandle),
// so a discoverable login can only resolve a user whose id decodes. The older
// fixtures use the literal "alice", which is five characters — length mod 4 ==
// 1, which RawURLEncoding rejects — so WebAuthnHandle() returns nil and
// register/begin emits "user":{"id":null}. That is harmless for registration,
// which never reads the handle back, and fatal for login, which resolves the
// user FROM it. Anything whose length mod 4 != 1 works; 32 bytes is what a real
// authenticator would see.
func testUserHandle() (UserID, []byte) {
	raw := []byte("rosterbot-test-handle-0123456789")
	return NewUserID(raw), raw
}

// fabricateAssertion builds a signed WebAuthn assertion — the login-ceremony
// counterpart to fabricateAttestation, and the first thing in this package able
// to drive FinishPasskeyLogin at all.
//
// The signature is ASN.1 DER (SignASN1), which is correct HERE and is worth
// stating because the opposite is true elsewhere in this repo: internal/apns
// must use raw r||s via FillBytes, since APNs rejects a DER provider token as
// InvalidProviderToken. Both are ES256 over P-256; only the envelope differs,
// and each rejects the other's with an error that does not mention encoding.
//
// signCount is a parameter rather than a constant because the counter is a
// clone detector: go-webauthn compares the asserted value against the stored
// one, so a test that always sent the same number could not tell an advancing
// counter from an ignored one.
func fabricateAssertion(t *testing.T, rpID, origin, challenge string,
	credID, userHandle []byte, signCount uint32, key *ecdsa.PrivateKey) map[string]any {
	t.Helper()

	// Assertion authenticator data is the SHORT form: no attested credential
	// data and no AT flag, unlike registration's. Appending the credential here
	// (the easy copy-paste error) shifts every following byte and the signature
	// verifies over data the server never reconstructs.
	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, byte(protocol.FlagUserPresent|protocol.FlagUserVerified))
	authData = binary.BigEndian.AppendUint32(authData, signCount)

	clientData, err := json.Marshal(map[string]any{
		"type":        "webauthn.get", // NOT webauthn.create; the server checks it
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The signed message is authenticatorData || SHA256(clientDataJSON), and
	// ECDSA then hashes that. Signing the concatenation UNhashed, or hashing
	// the clientDataJSON twice, both produce a well-formed signature that
	// simply never verifies.
	clientDataHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), clientDataHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	b64 := base64.RawURLEncoding.EncodeToString
	return map[string]any{
		"id":    b64(credID),
		"rawId": b64(credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"authenticatorData": b64(authData),
			"signature":         b64(sig),
			// Discoverable login resolves the user FROM this, so an assertion
			// without it authenticates nobody.
			"userHandle": b64(userHandle),
		},
	}
}

// registeredCredential drives a full registration and returns what a later
// assertion needs: the credential id and the key the authenticator kept.
func registeredCredential(t *testing.T, h http.Handler, secret []byte, uid UserID,
	rpID, origin string) (credID []byte, key *ecdsa.PrivateKey) {
	t.Helper()

	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, uid, 0, time.Now())
	authCookies := rec.Result().Cookies()

	beginRec := httptest.NewRecorder()
	h.ServeHTTP(beginRec, jsonPost(t, "/v1/auth/register/begin", nil, authCookies))
	if beginRec.Code != http.StatusOK {
		t.Fatalf("register/begin = %d: %s", beginRec.Code, beginRec.Body.String())
	}
	var begin struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	// If the handle did not survive the round trip there is no point going on:
	// the login below would fail for that reason and look like a signature bug.
	if begin.PublicKey.User.ID == "" {
		t.Fatalf("register/begin returned no user handle; a discoverable login "+
			"can never resolve this user. body=%s", beginRec.Body.String())
	}

	credID = []byte("login-test-credential-id")
	body, key := fabricateAttestationWithKey(t, rpID, origin, begin.PublicKey.Challenge, credID)
	finishRec := httptest.NewRecorder()
	h.ServeHTTP(finishRec, jsonPost(t, "/v1/auth/register/finish", body,
		append(authCookies, beginRec.Result().Cookies()...)))
	if finishRec.Code != http.StatusOK {
		t.Fatalf("register/finish = %d: %s", finishRec.Code, finishRec.Body.String())
	}
	return credID, key
}

// TestLoginFinish_SucceedsEndToEnd is the first test in this package to reach
// FinishPasskeyLogin.
//
// Every other login test asserts a REFUSAL — no ceremony cookie, no registered
// user — and each is turned away before the ceremony is verified, which is the
// same shape as the registration gap that let the passkey epic ship with
// register/finish unable to complete at all (rosterbot-xzvt). A bug of that
// class on this path — a drained body, a mis-scoped cookie, a counter check
// rejecting every returning user — would ship green.
//
// It runs the ceremony from each production origin, which also settles by test
// what rosterbot-jloe.6 could previously only settle by inference: that the
// two-entry RPOrigins list governs LOGIN and not merely registration.
func TestLoginFinish_SucceedsEndToEnd(t *testing.T) {
	origins, dropped := ParseRPOrigins(productionRPOrigins)
	if len(dropped) != 0 || len(origins) != 2 {
		t.Fatalf("production origins parsed as %q (dropped %q)", origins, dropped)
	}

	for _, origin := range origins {
		t.Run(origin, func(t *testing.T) {
			const rpID = "rosterbot.dev"
			wa, err := NewWebAuthn(rpID, origins, "rosterbot")
			if err != nil {
				t.Fatal(err)
			}
			secret := []byte("s")
			users := NewFileUserStore(t.TempDir())
			uid, handle := testUserHandle()
			ctx := context.Background()
			if err := users.CreateUser(ctx, &User{
				ID: uid, Email: "a@example.test", Role: RoleMember, Status: UserActive,
			}); err != nil {
				t.Fatal(err)
			}
			h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
				WebAuthn: wa, SessionSecret: secret})

			credID, key := registeredCredential(t, h, secret, uid, rpID, origin)

			// --- login: begin -------------------------------------------------
			beginRec := httptest.NewRecorder()
			h.ServeHTTP(beginRec, jsonPost(t, "/v1/auth/login/begin", nil, nil))
			if beginRec.Code != http.StatusOK {
				t.Fatalf("login/begin = %d: %s", beginRec.Code, beginRec.Body.String())
			}
			var lb struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
					RPID      string `json:"rpId"`
				} `json:"publicKey"`
			}
			if err := json.Unmarshal(beginRec.Body.Bytes(), &lb); err != nil {
				t.Fatal(err)
			}
			if lb.PublicKey.RPID != rpID {
				t.Errorf("login/begin rpId = %q, want %q", lb.PublicKey.RPID, rpID)
			}

			// --- login: finish ------------------------------------------------
			assertion := fabricateAssertion(t, rpID, origin, lb.PublicKey.Challenge,
				credID, handle, 1, key)
			finishRec := httptest.NewRecorder()
			h.ServeHTTP(finishRec, jsonPost(t, "/v1/auth/login/finish", assertion,
				beginRec.Result().Cookies()))
			if finishRec.Code != http.StatusOK {
				t.Fatalf("login/finish = %d from %s, want 200 — a passkey that cannot "+
					"log in from this origin. body=%s",
					finishRec.Code, origin, finishRec.Body.String())
			}

			// --- what the ceremony was FOR ------------------------------------
			// A 200 with no usable session is a login that leaves the user on the
			// sign-in page, so assert the cookie by USING it rather than by
			// looking at it.
			var session []*http.Cookie
			for _, c := range finishRec.Result().Cookies() {
				if c.Name == sessionCookieName && c.Value != "" && c.MaxAge >= 0 {
					session = append(session, c)
				}
			}
			if len(session) == 0 {
				t.Fatal("login/finish set no session cookie")
			}
			listRec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
			for _, c := range session {
				req.AddCookie(c)
			}
			h.ServeHTTP(listRec, req)
			if listRec.Code != http.StatusOK {
				t.Errorf("the session minted by login is not accepted on an authenticated "+
					"route: GET /v1/auth/passkeys = %d", listRec.Code)
			}

			// The sign counter is the clone detector, and it is only useful if the
			// asserted value is actually persisted.
			creds, err := users.Credentials(ctx, uid)
			if err != nil || len(creds) != 1 {
				t.Fatalf("Credentials = %d creds, err=%v", len(creds), err)
			}
			if got := creds[0].Authenticator.SignCount; got != 1 {
				t.Errorf("stored sign counter = %d, want 1 — the asserted counter was "+
					"not persisted, so a cloned authenticator could never be detected", got)
			}
		})
	}
}

// TestLoginFinish_RejectsForgedAssertions pins that the positive test above is
// verifying something. Each case is a well-formed ceremony differing in exactly
// one respect, so a handler that accepted any of them would also accept the
// happy path — and the happy-path test alone cannot tell the two apart.
func TestLoginFinish_RejectsForgedAssertions(t *testing.T) {
	const rpID = "rosterbot.dev"
	origins, _ := ParseRPOrigins(productionRPOrigins)

	for _, tc := range []struct {
		name string
		// tamper mutates the ceremony inputs just before the assertion is built.
		tamper func(t *testing.T, origin *string, handle *[]byte, key **ecdsa.PrivateKey)
	}{{
		name: "a signature from the wrong key",
		tamper: func(t *testing.T, _ *string, _ *[]byte, key **ecdsa.PrivateKey) {
			other, err := ecdsa.GenerateKey(elliptic256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			*key = other
		},
	}, {
		name: "an origin that is not in the RPOrigins list",
		tamper: func(_ *testing.T, origin *string, _ *[]byte, _ **ecdsa.PrivateKey) {
			// A subdomain of the RP ID: RP-ID scoping alone would admit it, so
			// only the exact-match origin list can refuse it.
			*origin = "https://evil.rosterbot.dev"
		},
	}, {
		name: "a user handle naming nobody",
		tamper: func(_ *testing.T, _ *string, handle *[]byte, _ **ecdsa.PrivateKey) {
			*handle = []byte("this-handle-belongs-to-no-user!!")
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			wa, err := NewWebAuthn(rpID, origins, "rosterbot")
			if err != nil {
				t.Fatal(err)
			}
			secret := []byte("s")
			users := NewFileUserStore(t.TempDir())
			uid, handle := testUserHandle()
			if err := users.CreateUser(context.Background(), &User{
				ID: uid, Email: "a@example.test", Role: RoleMember, Status: UserActive,
			}); err != nil {
				t.Fatal(err)
			}
			h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
				WebAuthn: wa, SessionSecret: secret})

			origin := "https://rosterbot.dev"
			credID, key := registeredCredential(t, h, secret, uid, rpID, origin)

			beginRec := httptest.NewRecorder()
			h.ServeHTTP(beginRec, jsonPost(t, "/v1/auth/login/begin", nil, nil))
			var lb struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
				} `json:"publicKey"`
			}
			if err := json.Unmarshal(beginRec.Body.Bytes(), &lb); err != nil {
				t.Fatal(err)
			}

			tc.tamper(t, &origin, &handle, &key)

			assertion := fabricateAssertion(t, rpID, origin, lb.PublicKey.Challenge,
				credID, handle, 1, key)
			finishRec := httptest.NewRecorder()
			h.ServeHTTP(finishRec, jsonPost(t, "/v1/auth/login/finish", assertion,
				beginRec.Result().Cookies()))

			if finishRec.Code == http.StatusOK {
				t.Fatalf("login/finish ACCEPTED %s — this ceremony should not "+
					"authenticate anyone", tc.name)
			}
			for _, c := range finishRec.Result().Cookies() {
				if c.Name == sessionCookieName && c.Value != "" && c.MaxAge >= 0 {
					t.Errorf("a refused login still minted a session cookie")
				}
			}
		})
	}
}

// elliptic256 exists so this file does not import crypto/elliptic solely for
// one call inside a table entry.
func elliptic256() elliptic.Curve { return elliptic.P256() }

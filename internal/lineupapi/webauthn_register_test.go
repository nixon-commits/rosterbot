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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

// TestRegisterFinish_SucceedsEndToEnd drives a registration ceremony to
// completion: begin -> a fabricated but spec-correct attestation -> finish ->
// a credential the login path can find.
//
// It exists because the passkey epic SHIPPED WITH REGISTRATION UNABLE TO
// COMPLETE AT ALL (rosterbot-xzvt, fixed in 9848e63) and no Go test noticed.
// Every register/finish test before this one asserts a REJECTION — no ceremony
// cookie, no session — and each is turned away by registrationSubject or
// ceremonySessionFromRequest several lines before FinishRegistration is
// reached. A route whose only tests exercise the paths that refuse it is
// untested in the sense that matters: the bug was that the body was drained
// before FinishRegistration parsed it, and a test posting "{}" and expecting
// failure is satisfied by an empty body just as well as by a full one.
//
// Both subtests post the enrollment token (or no token) IN THE BODY rather
// than in ?token=, because that is what web/dashboard/api.js does — and it is
// load-bearing: enrollmentToken returns early on a query parameter without
// touching the body, so a ?token= test would sail past the exact defect this
// pins. The no-token subtest matters for the same reason the doc comment on
// enrollmentToken says it does: the body was drained even when there was
// nothing to find, so adding a second passkey from a live session broke
// identically.
func TestRegisterFinish_SucceedsEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		// authorize returns the cookies and the extra body fields that stand in
		// for "this caller may register a passkey" on each of the two routes in,
		// plus whatever that route owes beyond the credential once the ceremony
		// has completed.
		authorize func(t *testing.T, users *FileUserStore, secret []byte) (
			cookies []*http.Cookie, extra map[string]any, after func(*testing.T))
	}{{
		name: "enrollment link, the first passkey on a new account",
		authorize: func(t *testing.T, users *FileUserStore, _ []byte) (
			[]*http.Cookie, map[string]any, func(*testing.T)) {
			tok, hash, err := MintEnrollmentToken()
			if err != nil {
				t.Fatal(err)
			}
			if err := users.CreateEnrollment(context.Background(), hash,
				Enrollment{UserID: "alice", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			return nil, map[string]any{"token": string(tok)}, func(t *testing.T) {
				// Redemption is the half of the flow that begin deliberately does
				// NOT do (see TestRegisterBegin_AcceptsEnrollmentToken); finish
				// owes it, and owes it only once the ceremony has produced a
				// credential. An unredeemed link after a successful registration
				// is a single-use recovery link that can be replayed.
				e, ok, err := users.GetEnrollment(context.Background(), hash)
				if err != nil || !ok {
					t.Fatalf("GetEnrollment: ok=%v err=%v", ok, err)
				}
				if !e.Redeemed() {
					t.Error("the enrollment link is still unspent after a completed " +
						"registration; a single-use link that survives its own ceremony " +
						"can be replayed into a second passkey")
				}
			}
		},
	}, {
		name: "live session, adding a second device",
		authorize: func(t *testing.T, users *FileUserStore, secret []byte) (
			[]*http.Cookie, map[string]any, func(*testing.T)) {
			// Seed a passkey so BeginRegistration builds a non-empty exclusion
			// list — the shape "add another device" actually has, and the one
			// the drained-body bug broke without any enrollment link involved.
			if err := users.PutCredential(context.Background(), "alice",
				webauthn.Credential{ID: []byte("existing-cred"), PublicKey: []byte("pk")}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			setSessionCookie(rec, secret, "alice", 0, time.Now())
			return rec.Result().Cookies(), nil, nil
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			secret := []byte("s")
			users := NewFileUserStore(t.TempDir())
			if err := users.CreateUser(context.Background(), &User{
				ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive,
			}); err != nil {
				t.Fatal(err)
			}
			h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
				WebAuthn: testWebAuthn(t), SessionSecret: secret})
			authCookies, extra, after := tc.authorize(t, users, secret)

			// --- begin -------------------------------------------------------
			beginRec := httptest.NewRecorder()
			h.ServeHTTP(beginRec, jsonPost(t, "/v1/auth/register/begin", extra, authCookies))
			if beginRec.Code != http.StatusOK {
				t.Fatalf("register/begin = %d, want 200, body=%s", beginRec.Code, beginRec.Body.String())
			}
			var begin struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
				} `json:"publicKey"`
			}
			if err := json.Unmarshal(beginRec.Body.Bytes(), &begin); err != nil {
				t.Fatalf("decoding register/begin response: %v", err)
			}
			if begin.PublicKey.Challenge == "" {
				t.Fatalf("register/begin returned no challenge: %s", beginRec.Body.String())
			}

			// --- the authenticator -------------------------------------------
			credID := []byte("fabricated-credential-id")
			body := fabricateAttestation(t, "localhost", "http://localhost:8080",
				begin.PublicKey.Challenge, credID)
			for k, v := range extra {
				// The real client merges the enrollment token INTO the attestation
				// object rather than sending it alongside; see api.js
				// authRegisterFinish.
				body[k] = v
			}

			// --- finish ------------------------------------------------------
			// Carry the ceremony cookie exactly as a browser would: it is the
			// server's only memory of the challenge, so anything that shrinks it
			// (CredParams is the bulky part, and dropping it costs the algorithm
			// check its input) fails here rather than in production.
			finishRec := httptest.NewRecorder()
			h.ServeHTTP(finishRec, jsonPost(t, "/v1/auth/register/finish", body,
				slices.Concat(authCookies, beginRec.Result().Cookies())))
			if finishRec.Code != http.StatusOK {
				t.Fatalf("register/finish = %d, want 200, body=%s", finishRec.Code, finishRec.Body.String())
			}
			if !strings.Contains(finishRec.Body.String(), `"status":"registered"`) {
				t.Errorf("register/finish body = %s, want it to report the registration",
					finishRec.Body.String())
			}

			// --- what the ceremony was FOR -----------------------------------
			ctx := context.Background()
			creds, err := users.Credentials(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			var stored bool
			for _, c := range creds {
				if string(c.ID) == string(credID) {
					stored = true
					if len(c.PublicKey) == 0 {
						t.Error("the stored credential has no public key; it can never verify an assertion")
					}
				}
			}
			if !stored {
				t.Fatalf("the new credential is not in the store; got %d credential(s)", len(creds))
			}
			// The reverse index is what makes the passkey usable, so assert it
			// through the store rather than trusting that PutCredential wrote it.
			// TestRevokePasskey_RemovesMatchingCredential pins the mirror image:
			// a credential still resolvable after revocation is still usable.
			if owner, ok, err := users.UserByCredential(ctx, credID); err != nil || !ok || owner != "alice" {
				t.Errorf("UserByCredential(new credential) = (%q, %v, %v), want (alice, true, nil) — "+
					"a registered passkey the lookup index cannot resolve is a passkey that cannot log in",
					owner, ok, err)
			}

			// Registration signs you in. Without this the user completes Touch ID
			// and lands back on the login screen with a working passkey and no
			// session, which reads as a failed registration.
			var gotSession, clearedCeremony bool
			for _, c := range finishRec.Result().Cookies() {
				if c.Name == sessionCookieName && c.Value != "" && c.MaxAge >= 0 {
					gotSession = true
				}
				if c.Name == ceremonyCookieName && c.MaxAge < 0 {
					clearedCeremony = true
				}
			}
			if !gotSession {
				t.Error("no session cookie after a successful registration")
			}
			if !clearedCeremony {
				t.Error("the ceremony cookie survived the ceremony; a spent challenge must not linger")
			}
			if after != nil {
				after(t)
			}
		})
	}
}

// jsonPost builds the request the dashboard's fetch() would: a JSON body (or
// none at all when there is nothing to send, which is how api.js calls
// register/begin for a logged-in user) plus cookies.
func jsonPost(t *testing.T, path string, body map[string]any, cookies []*http.Cookie) *http.Request {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(http.MethodPost, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

// fabricateAttestation is a software authenticator: it produces the
// navigator.credentials.create() response a real one would, for challenge.
//
// It is hand-assembled because go-webauthn ships no fabricator — its
// testing/ package holds metadata-provider mocks and nothing else — and
// because the alternative, a canned fixture, would have to carry a canned
// challenge and so could only be replayed against a session built by the test
// rather than against the one register/begin actually minted. Driving the real
// begin is the whole point: it is what makes the ceremony cookie, the
// challenge and the credential parameters the server's own.
//
// "none" attestation with an EMPTY attStmt is not a shortcut around the
// signature check — it is what a platform authenticator sends when the RP asks
// for no attestation, which is this RP's configuration. go-webauthn REJECTS a
// none-format object carrying any attStmt at all, so the empty map is required
// rather than lazy.
//
// The byte layout below is fixed by the spec and every field of it is checked
// downstream, so a "simplification" here fails opaquely: the RP ID hash must be
// SHA-256 of the RP ID (a mismatch reports a hash comparison, not a field
// name), the 16-byte AAGUID must be present or the credential-id length is read
// from the wrong offset and everything after it is garbage, and the COSE key
// must be CTAP2-canonical CBOR because go-webauthn re-marshals it and compares
// lengths — a non-canonical encoding surfaces as "Leftover bytes decoding
// AuthenticatorData" with nothing pointing at the key.
func fabricateAttestation(t *testing.T, rpID, origin, challenge string, credID []byte) map[string]any {
	t.Helper()
	body, _ := fabricateAttestationWithKey(t, rpID, origin, challenge, credID)
	return body
}

// fabricateAttestationWithKey is fabricateAttestation plus the private key the
// authenticator kept, so a later assertion can actually be signed.
//
// The key is ECDSA rather than ECDH — the same P-256 point either way, but only
// ecdsa can sign, and an assertion the server can verify is the whole point of
// having the key at all. Registration alone never needed it, which is why the
// original generated an ecdh key and threw it away.
func fabricateAttestationWithKey(t *testing.T, rpID, origin, challenge string, credID []byte) (map[string]any, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// COSE wants each coordinate as a fixed-width 32-byte big-endian integer.
	// Bytes() returns the uncompressed point 0x04 || x(32) || y(32), which is
	// already fixed-width, so slicing it needs no padding step. Reading
	// PublicKey.X/.Y instead is both deprecated (Go 1.26) and a trap:
	// big.Int.Bytes() left-trims a leading zero byte and yields 31, which is a
	// valid-looking key that verifies nothing.
	point, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	coseKey, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType: int64(webauthncose.EllipticKey),
			// ES256 must be one of the algorithms register/begin offered, and it
			// reaches FinishRegistration through the ceremony cookie's CredParams.
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: point[1:33],
		YCoord: point[33:],
	})
	if err != nil {
		t.Fatal(err)
	}

	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37+16+2+len(credID)+len(coseKey))
	authData = append(authData, rpIDHash[:]...)
	// UP and AT are the minimum the registration path checks; UV is set because
	// a platform authenticator that just took a biometric sets it, and a
	// UserVerification: required policy would otherwise reject this ceremony.
	authData = append(authData, byte(protocol.FlagUserPresent|
		protocol.FlagUserVerified|protocol.FlagAttestedCredentialData))
	authData = binary.BigEndian.AppendUint32(authData, 0) // sign counter
	authData = append(authData, make([]byte, 16)...)      // AAGUID: zero is legal and means "not disclosed"
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(credID)))
	authData = append(authData, credID...)
	authData = append(authData, coseKey...)

	attObj, err := webauthncbor.Marshal(struct {
		Format   string         `cbor:"fmt"`
		AttStmt  map[string]any `cbor:"attStmt"`
		AuthData []byte         `cbor:"authData"`
	}{Format: "none", AttStmt: map[string]any{}, AuthData: authData})
	if err != nil {
		t.Fatal(err)
	}

	clientData, err := json.Marshal(map[string]any{
		"type": "webauthn.create",
		// Verbatim, as the browser echoes it: the comparison is constant-time
		// over the base64url string, not over the decoded challenge bytes.
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
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
			"attestationObject": b64(attObj),
		},
	}, key
}

// productionRPOrigins is the exact value infra.go publishes into
// /rosterbot/DASHBOARD_RP_ORIGIN (see RpOriginParam; the infra side pins the
// synthesized parameter against its own apexHost/dashHost constants in
// infra/domain_test.go). It is spelled once on this side of the module
// boundary so the two tests that need it cannot drift from each other.
const productionRPOrigins = "https://rosterbot.dev,https://dash.rosterbot.dev"

// TestRegisterFinish_CompletesFromEitherProductionOrigin drives a full
// ceremony from EACH origin in the production list, and refuses one from an
// origin that is not on it.
//
// TestNewWebAuthn_AcceptsBothProductionOrigins is not this test. It asserts
// that the config holds two strings; it never hands go-webauthn a
// clientDataJSON to judge, so it passes just as happily if the origin
// validator is never reached. The distinction is the whole point of
// rosterbot-jloe.6: RP ID is the APEX while the origin list holds BOTH, and
// the surfaces really do report different origins for that one RP ID — a
// browser on the dashboard reports https://dash.rosterbot.dev, an iOS native
// ceremony reports https://rosterbot.dev. So rpID is the apex in every subtest
// below and only the clientDataJSON origin moves. That asymmetry is the thing
// under test, not an artifact of the fixture.
//
// It exists because the alternative was a MANUAL check. The cutover was
// verified in production by a real iOS registration (2026-08-21), which
// exercised the apex entry and only the apex entry; proving the dash entry
// meant asking a human to go and log in once, and nothing would ever re-check
// it. That is the failure ParseRPOrigins' own doc comment describes: one bad
// entry in a two-origin list leaves the OTHER surface working perfectly, so
// the symptom is passkeys mysteriously failing on one surface with a generic
// ceremony error and nothing connecting it to an SSM parameter.
//
// The negative subtest is load-bearing and its choice of origin is deliberate.
// A stranger origin like https://example.com would be refused by a validator
// that only checked the registrable domain, so it cannot tell "the origin list
// is enforced" from "the RP ID check happens to catch this". An unlisted
// SUBDOMAIN OF THE RP ID can: rosterbot.dev is its registrable domain, so RP
// ID scoping alone would admit it, and only the exact-match origin list turns
// it away. Without that case, a build with origin validation disabled entirely
// would pass the two positive subtests.
func TestRegisterFinish_CompletesFromEitherProductionOrigin(t *testing.T) {
	origins, dropped := ParseRPOrigins(productionRPOrigins)
	if len(dropped) != 0 {
		t.Fatalf("the production origin parameter dropped %q", dropped)
	}
	if len(origins) != 2 {
		t.Fatalf("parsed %d origin(s) from %q, want 2 — a one-entry list silently "+
			"strands whichever surface is missing", len(origins), productionRPOrigins)
	}

	for _, tc := range []struct {
		name    string
		origin  string
		wantOK  bool
		surface string
	}{{
		name:    "apex, as an iOS native ceremony reports it",
		origin:  "https://rosterbot.dev",
		wantOK:  true,
		surface: "the iOS app",
	}, {
		name:    "dash, as the dashboard browser reports it",
		origin:  "https://dash.rosterbot.dev",
		wantOK:  true,
		surface: "the web dashboard",
	}, {
		name:   "an unlisted subdomain of the RP ID is refused",
		origin: "https://evil.rosterbot.dev",
		wantOK: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			wa, err := NewWebAuthn("rosterbot.dev", origins, "rosterbot")
			if err != nil {
				t.Fatalf("NewWebAuthn rejected the production RP config: %v", err)
			}

			secret := []byte("s")
			users := NewFileUserStore(t.TempDir())
			if err := users.CreateUser(context.Background(), &User{
				ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive,
			}); err != nil {
				t.Fatal(err)
			}
			h := Handler(Config{Token: "secret-token", Users: users, Enrollments: users,
				WebAuthn: wa, SessionSecret: secret})

			rec := httptest.NewRecorder()
			setSessionCookie(rec, secret, "alice", 0, time.Now())
			authCookies := rec.Result().Cookies()

			beginRec := httptest.NewRecorder()
			h.ServeHTTP(beginRec, jsonPost(t, "/v1/auth/register/begin", nil, authCookies))
			if beginRec.Code != http.StatusOK {
				t.Fatalf("register/begin = %d, want 200, body=%s", beginRec.Code, beginRec.Body.String())
			}
			var begin struct {
				PublicKey struct {
					Challenge string `json:"challenge"`
					// Creation options nest the RP as rp:{id,name}, unlike the
					// request options login/begin returns, which carry a flat
					// rpId. Reading the wrong one yields "" and an assertion
					// that fails for a reason unrelated to the RP ID.
					RP struct {
						ID string `json:"id"`
					} `json:"rp"`
				} `json:"publicKey"`
			}
			if err := json.Unmarshal(beginRec.Body.Bytes(), &begin); err != nil {
				t.Fatalf("decoding register/begin response: %v", err)
			}
			// The credential the ceremony mints is bound to whatever rpId begin
			// advertised, forever. Asserting it here means a regression that moved
			// the RP ID off the apex would fail as a wrong-identity bug rather
			// than sailing through as a passing origin test.
			if begin.PublicKey.RP.ID != "rosterbot.dev" {
				t.Errorf("register/begin advertised rpId %q, want the apex — a credential "+
					"minted here would be bound to the wrong identity", begin.PublicKey.RP.ID)
			}

			// rpID is the APEX in every subtest; only the origin moves.
			credID := []byte("fabricated-credential-id")
			body := fabricateAttestation(t, "rosterbot.dev", tc.origin,
				begin.PublicKey.Challenge, credID)

			finishRec := httptest.NewRecorder()
			h.ServeHTTP(finishRec, jsonPost(t, "/v1/auth/register/finish", body,
				slices.Concat(authCookies, beginRec.Result().Cookies())))

			if !tc.wantOK {
				if finishRec.Code == http.StatusOK {
					t.Fatalf("register/finish accepted a ceremony from %s, which is not in "+
						"the origin list — the list is not being enforced", tc.origin)
				}
				return
			}
			if finishRec.Code != http.StatusOK {
				t.Fatalf("register/finish = %d from %s, want 200: %s is locked out "+
					"of passkeys entirely. body=%s",
					finishRec.Code, tc.origin, tc.surface, finishRec.Body.String())
			}
			// A 200 that stored nothing would be a ceremony that "succeeded" into
			// a user with no passkey, so assert the credential the ceremony was for.
			if owner, ok, err := users.UserByCredential(context.Background(), credID); err != nil || !ok || owner != "alice" {
				t.Errorf("UserByCredential after a ceremony from %s = (%q, %v, %v), want (alice, true, nil)",
					tc.origin, owner, ok, err)
			}
		})
	}
}

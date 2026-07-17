# Dashboard Passkey Auth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard's shared-token login with WebAuthn passkeys as the real authentication mechanism, while keeping the token as a break-glass/bootstrap credential and adding zero new AWS services.

**Architecture:** `internal/lineupapi` gains an `IdentityStore` (one JSON record: a stable WebAuthn user handle + every registered passkey credential, stored in the existing state bucket / a local file) and seven `/v1/auth/*` endpoints built on `github.com/go-webauthn/webauthn`. Post-login sessions are a stateless HMAC-signed cookie (no new datastore); the in-flight WebAuthn ceremony challenge rides its own short-lived cookie between each ceremony's begin/finish call, per the library's documented pattern. The existing bearer-token check stays wired into the router as an OR-condition alongside the session cookie, and is the only way to register the very first passkey.

**Tech Stack:** Go 1.26 (backend, `github.com/go-webauthn/webauthn`), vanilla ES modules (frontend, no build step), AWS CDK Go (`infra/`).

## Global Constraints

- No new AWS services (no Cognito, no DynamoDB, no API Gateway) — every new piece of state rides the existing S3 state bucket, SSM Parameter Store, or a browser cookie.
- `ROSTERBOT_API_TOKEN` is never removed from the code path; it demotes to a break-glass/bootstrap credential, never surfaced in the normal UI after rollout.
- Session cookies: `httpOnly`, `Secure`, `SameSite=Strict`, 30-day expiry, stateless (HMAC-signed, no server-side lookup).
- Ceremony cookies (the WebAuthn challenge in flight between a ceremony's begin/finish calls): `httpOnly`, `Secure`, `SameSite=Strict`, 5-minute expiry.
- New passkeys are always created with `WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired)` — a true discoverable passkey, not a bare security key.
- Follow the spec at `docs/superpowers/specs/2026-07-17-dashboard-passkey-auth-design.md` for anything not covered by a task below.

---

## Task 1: Session cookie helpers

**Files:**
- Create: `internal/lineupapi/session.go`
- Test: `internal/lineupapi/session_test.go`

**Interfaces:**
- Consumes: nothing (pure `crypto/hmac`/`net/http`, no dependency on the WebAuthn library).
- Produces: `signSession(secret []byte, now time.Time) string`, `verifySession(secret []byte, value string, now time.Time) error`, `setSessionCookie(w http.ResponseWriter, secret []byte, now time.Time)`, `clearSessionCookie(w http.ResponseWriter)`, `hasValidSession(r *http.Request, secret []byte) bool`, `sessionCookieName` (const `"rosterbot_session"`), `sessionTTL` (const `30 * 24 * time.Hour`). Task 4/5 call these directly.

- [ ] **Step 1: Write the failing tests**

```go
package lineupapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignAndVerifySession(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	value := signSession(secret, now)
	if err := verifySession(secret, value, now); err != nil {
		t.Fatalf("verify immediately after sign: %v", err)
	}
	if err := verifySession(secret, value, now.Add(sessionTTL-time.Minute)); err != nil {
		t.Fatalf("verify just before expiry: %v", err)
	}
	if err := verifySession(secret, value, now.Add(sessionTTL+time.Minute)); err == nil {
		t.Fatal("want error verifying after expiry, got nil")
	}
}

func TestVerifySessionRejectsTamperedValue(t *testing.T) {
	secret := []byte("test-secret")
	now := time.Now()
	value := signSession(secret, now)

	if err := verifySession([]byte("wrong-secret"), value, now); err == nil {
		t.Fatal("want error verifying with the wrong secret, got nil")
	}
	if err := verifySession(secret, value+"x", now); err == nil {
		t.Fatal("want error verifying a tampered value, got nil")
	}
	if err := verifySession(secret, "not-even-close", now); err == nil {
		t.Fatal("want error verifying a malformed value, got nil")
	}
	if err := verifySession(secret, "", now); err == nil {
		t.Fatal("want error verifying an empty value, got nil")
	}
}

func TestHasValidSession(t *testing.T) {
	secret := []byte("test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	if hasValidSession(req, secret) {
		t.Fatal("want false with no cookie set")
	}

	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, time.Now())
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	if !hasValidSession(req, secret) {
		t.Fatal("want true with a freshly set cookie")
	}

	if hasValidSession(req, nil) {
		t.Fatal("want false when the server has no configured secret (misconfiguration must fail closed)")
	}
}

func TestClearSessionCookieExpiresImmediately(t *testing.T) {
	rec := httptest.NewRecorder()
	clearSessionCookie(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("want one cookie with MaxAge < 0, got %+v", cookies)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/lineupapi/... -run 'TestSignAndVerifySession|TestVerifySessionRejectsTamperedValue|TestHasValidSession|TestClearSessionCookieExpiresImmediately' -v`
Expected: FAIL — `signSession`, `verifySession`, `hasValidSession`, `setSessionCookie`, `clearSessionCookie`, `sessionTTL` undefined.

- [ ] **Step 3: Write the implementation**

```go
package lineupapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookieName = "rosterbot_session"
	sessionTTL        = 30 * 24 * time.Hour
)

var errInvalidSession = errors.New("invalid or expired session")

type sessionPayload struct {
	ExpiresAt int64 `json:"exp"`
}

// signSession mints a signed, stateless session cookie value:
// base64url(payload) + "." + base64url(HMAC-SHA256(payload, secret)). Nothing
// is stored server-side — verifying later needs only the same secret.
func signSession(secret []byte, now time.Time) string {
	payload, _ := json.Marshal(sessionPayload{ExpiresAt: now.Add(sessionTTL).Unix()})
	return signPayload(secret, payload)
}

func signPayload(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// verifySession checks the signature and expiry of a session cookie value.
func verifySession(secret []byte, value string, now time.Time) error {
	payload, err := verifyPayload(secret, value)
	if err != nil {
		return err
	}
	var p sessionPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return errInvalidSession
	}
	if now.Unix() > p.ExpiresAt {
		return errInvalidSession
	}
	return nil
}

func verifyPayload(secret []byte, value string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errInvalidSession
	}
	dot := strings.IndexByte(value, '.')
	if dot < 0 {
		return nil, errInvalidSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(value[:dot])
	if err != nil {
		return nil, errInvalidSession
	}
	sig, err := base64.RawURLEncoding.DecodeString(value[dot+1:])
	if err != nil {
		return nil, errInvalidSession
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	want := mac.Sum(nil)
	if subtle.ConstantTimeCompare(sig, want) != 1 {
		return nil, errInvalidSession
	}
	return payload, nil
}

func setSessionCookie(w http.ResponseWriter, secret []byte, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    signSession(secret, now),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// hasValidSession reports whether the request carries a session cookie signed
// by secret and not yet expired. An empty secret (misconfiguration) fails
// closed, matching authorized()'s empty-token behavior.
func hasValidSession(r *http.Request, secret []byte) bool {
	if len(secret) == 0 {
		return false
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySession(secret, c.Value, time.Now()) == nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/lineupapi/... -run 'TestSignAndVerifySession|TestVerifySessionRejectsTamperedValue|TestHasValidSession|TestClearSessionCookieExpiresImmediately' -v`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/lineupapi/session.go internal/lineupapi/session_test.go
git commit -m "feat(dashboard): add stateless session cookie signing/verification"
```

---

## Task 2: Identity type, IdentityStore interface, FileIdentityStore

**Files:**
- Create: `internal/lineupapi/identity.go`
- Test: `internal/lineupapi/identity_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/go-webauthn/webauthn`)

**Interfaces:**
- Consumes: nothing new.
- Produces: `type Identity struct { WebAuthnUserID []byte; Credentials []webauthn.Credential }`, `type IdentityStore interface { GetIdentity(ctx) (*Identity, bool, error); PutIdentity(ctx, *Identity) error }`, `type identityUser struct{ id *Identity }` implementing `webauthn.User`, `newWebAuthnUserID() ([]byte, error)`, `NewFileIdentityStore(dir string) *FileIdentityStore`. Task 3 (S3 adapter) implements the same `IdentityStore` interface; Task 4/5 consume `identityUser` and `newWebAuthnUserID`.

- [ ] **Step 1: Add the go-webauthn dependency**

```bash
go get github.com/go-webauthn/webauthn@latest
go mod tidy
```

- [ ] **Step 2: Write the failing tests**

```go
package lineupapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestIdentityUserAdapter(t *testing.T) {
	id := &Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}
	u := identityUser{id: id}

	if string(u.WebAuthnID()) != "handle-123" {
		t.Fatalf("WebAuthnID() = %q, want %q", u.WebAuthnID(), "handle-123")
	}
	if len(u.WebAuthnCredentials()) != 1 || string(u.WebAuthnCredentials()[0].ID) != "cred-1" {
		t.Fatalf("WebAuthnCredentials() = %+v, want one credential with ID cred-1", u.WebAuthnCredentials())
	}
	if u.WebAuthnName() == "" || u.WebAuthnDisplayName() == "" {
		t.Fatal("WebAuthnName()/WebAuthnDisplayName() must not be empty — go-webauthn rejects empty names")
	}
}

func TestNewWebAuthnUserIDIsRandomAnd64Bytes(t *testing.T) {
	a, err := newWebAuthnUserID()
	if err != nil {
		t.Fatalf("newWebAuthnUserID: %v", err)
	}
	if len(a) != 64 {
		t.Fatalf("len = %d, want 64", len(a))
	}
	b, err := newWebAuthnUserID()
	if err != nil {
		t.Fatalf("newWebAuthnUserID: %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two calls returned the same handle — want random")
	}
}

func TestFileIdentityStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewFileIdentityStore(dir)

	if _, ok, err := s.GetIdentity(context.Background()); err != nil || ok {
		t.Fatalf("GetIdentity on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	want := &Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1"), PublicKey: []byte("pk")}},
	}
	if err := s.PutIdentity(context.Background(), want); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}

	got, ok, err := s.GetIdentity(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetIdentity after Put: ok=%v err=%v", ok, err)
	}
	if string(got.WebAuthnUserID) != "handle-123" || len(got.Credentials) != 1 || string(got.Credentials[0].ID) != "cred-1" {
		t.Fatalf("got = %+v, want %+v", got, want)
	}

	if _, err := os.Stat(filepath.Join(dir, "webauthn-identity.json")); err != nil {
		t.Fatalf("expected file at webauthn-identity.json: %v", err)
	}
}

var _ IdentityStore = (*FileIdentityStore)(nil)
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/lineupapi/... -run 'TestIdentityUserAdapter|TestNewWebAuthnUserIDIsRandomAnd64Bytes|TestFileIdentityStoreRoundTrip' -v`
Expected: FAIL — `Identity`, `identityUser`, `newWebAuthnUserID`, `NewFileIdentityStore` undefined.

- [ ] **Step 4: Write the implementation**

```go
package lineupapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Identity is the dashboard's single WebAuthn identity: a stable random user
// handle plus every registered passkey. There is exactly one Identity for the
// whole dashboard — one operator, multiple device-bound credentials.
type Identity struct {
	WebAuthnUserID []byte                `json:"webauthn_user_id"`
	Credentials    []webauthn.Credential `json:"credentials"`
}

// IdentityStore is the read/write side for the Identity record. Unlike
// ObjectStore/Publisher (write-once-per-key, read-many), this store is
// read-modify-written on every passkey registration, login (sign-counter
// update), and revocation.
type IdentityStore interface {
	GetIdentity(ctx context.Context) (*Identity, bool, error)
	PutIdentity(ctx context.Context, id *Identity) error
}

// identityUser adapts *Identity to webauthn.User.
type identityUser struct {
	id *Identity
}

func (u identityUser) WebAuthnID() []byte                        { return u.id.WebAuthnUserID }
func (u identityUser) WebAuthnName() string                      { return "rosterbot" }
func (u identityUser) WebAuthnDisplayName() string                { return "rosterbot" }
func (u identityUser) WebAuthnCredentials() []webauthn.Credential { return u.id.Credentials }

// newWebAuthnUserID generates a stable random 64-byte user handle for a brand
// new Identity. Called once, the first time register/begin runs against an
// empty store (see Task 4's loadOrCreateIdentity).
func newWebAuthnUserID() ([]byte, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// FileIdentityStore is a local-filesystem IdentityStore, one JSON file at
// <dir>/webauthn-identity.json. Used by `rosterbot serve`.
type FileIdentityStore struct {
	dir string
}

func NewFileIdentityStore(dir string) *FileIdentityStore { return &FileIdentityStore{dir: dir} }

func (s *FileIdentityStore) path() string {
	return filepath.Join(s.dir, "webauthn-identity.json")
}

func (s *FileIdentityStore) GetIdentity(_ context.Context) (*Identity, bool, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, false, err
	}
	return &id, true, nil
}

func (s *FileIdentityStore) PutIdentity(_ context.Context, id *Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(), data, 0o644)
}

var _ IdentityStore = (*FileIdentityStore)(nil)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/lineupapi/... -run 'TestIdentityUserAdapter|TestNewWebAuthnUserIDIsRandomAnd64Bytes|TestFileIdentityStoreRoundTrip' -v`
Expected: PASS (all 3 tests)

- [ ] **Step 6: Commit**

```bash
git add internal/lineupapi/identity.go internal/lineupapi/identity_test.go go.mod go.sum
git commit -m "feat(dashboard): add WebAuthn Identity type, IdentityStore, and local file store"
```

---

## Task 3: S3 IdentityStore adapter

**Files:**
- Create: `internal/lineupapi/s3lineup/identity.go`
- Test: `internal/lineupapi/s3lineup/identity_test.go`

**Interfaces:**
- Consumes: `lineupapi.Identity`, `lineupapi.IdentityStore` (Task 2). Same `api` interface and `fakeS3`/`ptr` test helpers already defined in `internal/lineupapi/s3lineup/output_test.go`/`s3lineup.go` — reuse them, don't redefine.
- Produces: `NewIdentity(ctx, bucket, prefix string) (*IdentityStore, error)`. Task 9 (lambda wiring) calls this.

- [ ] **Step 1: Write the failing test**

```go
package s3lineup

import (
	"context"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

func TestIdentityStoreRoundTrip(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	s := &IdentityStore{client: f, bucket: "b", prefix: "webauthn/"}

	if _, ok, err := s.GetIdentity(context.Background()); err != nil || ok {
		t.Fatalf("GetIdentity on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	want := &lineupapi.Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}
	if err := s.PutIdentity(context.Background(), want); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if _, stored := f.objects["webauthn/identity.json"]; !stored {
		t.Fatalf("object not stored at expected key; got keys %v", keys(f.objects))
	}

	got, ok, err := s.GetIdentity(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetIdentity after Put: ok=%v err=%v", ok, err)
	}
	if string(got.WebAuthnUserID) != "handle-123" || len(got.Credentials) != 1 {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lineupapi/s3lineup/... -run TestIdentityStoreRoundTrip -v`
Expected: FAIL — `IdentityStore` undefined in package `s3lineup`.

- [ ] **Step 3: Write the implementation**

```go
package s3lineup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// IdentityStore reads/writes the single WebAuthn Identity record at
// <prefix>identity.json.
type IdentityStore struct {
	client api
	bucket string
	prefix string
}

// NewIdentity builds an IdentityStore. prefix should end in "/", e.g. "webauthn/".
func NewIdentity(ctx context.Context, bucket, prefix string) (*IdentityStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &IdentityStore{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *IdentityStore) objKey() string { return s.prefix + "identity.json" }

func (s *IdentityStore) GetIdentity(ctx context.Context) (*lineupapi.Identity, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: ptr(s.objKey())})
	if err != nil {
		var nsk *types.NoSuchKey
		var nf *types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	var id lineupapi.Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, false, err
	}
	return &id, true, nil
}

func (s *IdentityStore) PutIdentity(ctx context.Context, id *lineupapi.Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         ptr(s.objKey()),
		Body:        bytes.NewReader(data),
		ContentType: ptr("application/json"),
	})
	return err
}

var _ lineupapi.IdentityStore = (*IdentityStore)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lineupapi/s3lineup/... -run TestIdentityStoreRoundTrip -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/lineupapi/s3lineup/identity.go internal/lineupapi/s3lineup/identity_test.go
git commit -m "feat(dashboard): add S3-backed IdentityStore adapter"
```

---

## Task 4: Registration endpoints (`register/begin`, `register/finish`)

**Files:**
- Create: `internal/lineupapi/webauthn.go`
- Modify: `internal/lineupapi/handler.go:27-63` (Config struct, Handler router)
- Test: `internal/lineupapi/webauthn_test.go`

**Interfaces:**
- Consumes: `IdentityStore`, `Identity`, `identityUser`, `newWebAuthnUserID` (Task 2); `hasValidSession`, `sessionCookieName` (Task 1); `authorized(r, token)`, `writeJSON`, `writeErr` (existing `handler.go`).
- Produces: `Config.Identities IdentityStore`, `Config.WebAuthn *webauthn.WebAuthn`, `Config.SessionSecret []byte` fields on the existing `Config` struct; `ceremonyCookieName` const, `setCeremonyCookie`, `ceremonySessionFromRequest`, `clearCeremonyCookie`; `(cfg Config) canRegister(r *http.Request) bool`; `(cfg Config) loadOrCreateIdentity(ctx) (*Identity, error)`; the two handlers, registered on the router at `POST /v1/auth/register/begin` and `POST /v1/auth/register/finish`. Task 5 adds more handlers to the same router and file.

- [ ] **Step 1: Write the failing tests**

```go
package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

// fakeIdentities is an in-memory IdentityStore for handler tests.
type fakeIdentities struct {
	id  *Identity
	ok  bool
	err error
}

func (f *fakeIdentities) GetIdentity(_ context.Context) (*Identity, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.id, f.ok, nil
}

func (f *fakeIdentities) PutIdentity(_ context.Context, id *Identity) error {
	if f.err != nil {
		return f.err
	}
	f.id = id
	f.ok = true
	return nil
}

func testWebAuthn(t *testing.T) *webauthn.WebAuthn {
	t.Helper()
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          "localhost",
		RPOrigins:     []string{"http://localhost:8080"},
		RPDisplayName: "rosterbot (test)",
	})
	if err != nil {
		t.Fatalf("webauthn.New: %v", err)
	}
	return wa
}

func TestRegisterBegin_RejectsWithoutSessionOrToken(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestRegisterBegin_AcceptsBootstrapToken(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var gotCeremonyCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == ceremonyCookieName {
			gotCeremonyCookie = true
		}
	}
	if !gotCeremonyCookie {
		t.Fatal("want a ceremony cookie set on a successful register/begin")
	}
}

func TestRegisterBegin_AcceptsValidSession(t *testing.T) {
	secret := []byte("s")
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: secret})

	// Mint a valid session cookie the same way login/finish would.
	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, time.Now())

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestRegisterFinish_RejectsWithoutCeremonyCookie(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no in-progress ceremony)", rec.Code)
	}
}

func TestRegisterFinish_RejectsWithoutSessionOrToken(t *testing.T) {
	h := Handler(Config{Token: "secret-token", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/finish", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
```

Add `"time"` to `webauthn_test.go`'s imports (needed for the `time.Now()` call above).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/lineupapi/... -run 'TestRegisterBegin|TestRegisterFinish' -v`
Expected: FAIL — `Config.Identities`/`Config.WebAuthn`/`Config.SessionSecret` undefined, `ceremonyCookieName` undefined, routes 404.

- [ ] **Step 3: Write the implementation**

Create `internal/lineupapi/webauthn.go`:

```go
// Package lineupapi (this file): WebAuthn passkey registration. See
// docs/superpowers/specs/2026-07-17-dashboard-passkey-auth-design.md.
package lineupapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	ceremonyCookieName = "rosterbot_ceremony"
	ceremonyTTL         = 5 * time.Minute
)

// NewWebAuthn builds the RP config used by every ceremony handler. rpID must
// be the bare hostname (no scheme/port); rpOrigin must be the full origin
// (scheme+host, no trailing slash) the browser reports in clientDataJSON.
func NewWebAuthn(rpID, rpOrigin, rpDisplayName string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPOrigins:     []string{rpOrigin},
		RPDisplayName: rpDisplayName,
	})
}

func setCeremonyCookie(w http.ResponseWriter, session *webauthn.SessionData) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(data),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ceremonyTTL.Seconds()),
	})
	return nil
}

func ceremonySessionFromRequest(r *http.Request) (*webauthn.SessionData, error) {
	c, err := r.Cookie(ceremonyCookieName)
	if err != nil {
		return nil, errors.New("no in-progress ceremony")
	}
	data, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, errors.New("corrupt ceremony cookie")
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, errors.New("corrupt ceremony cookie")
	}
	return &session, nil
}

func clearCeremonyCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ceremonyCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

// canRegister allows enrolling a new passkey either from an already-logged-in
// session (adding a second device) or via the one-time bootstrap token (the
// very first passkey, or recovery if every passkey was ever lost/revoked).
func (cfg Config) canRegister(r *http.Request) bool {
	return hasValidSession(r, cfg.SessionSecret) || authorized(r, cfg.Token)
}

// loadOrCreateIdentity returns the existing Identity, or a brand new one (not
// yet persisted — the caller persists it after a credential is attached) if
// none exists yet.
func (cfg Config) loadOrCreateIdentity(ctx context.Context) (*Identity, error) {
	id, ok, err := cfg.Identities.GetIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		return id, nil
	}
	handle, err := newWebAuthnUserID()
	if err != nil {
		return nil, err
	}
	return &Identity{WebAuthnUserID: handle}, nil
}

func (cfg Config) handleAuthRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if !cfg.canRegister(r) {
		writeErr(w, http.StatusForbidden, "not authorized to register a passkey")
		return
	}
	identity, err := cfg.loadOrCreateIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	creation, session, err := cfg.WebAuthn.BeginRegistration(identityUser{id: identity},
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not begin registration")
		return
	}
	if err := setCeremonyCookie(w, session); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start ceremony")
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

func (cfg Config) handleAuthRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if !cfg.canRegister(r) {
		writeErr(w, http.StatusForbidden, "not authorized to register a passkey")
		return
	}
	session, err := ceremonySessionFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "registration session expired, try again")
		return
	}
	identity, err := cfg.loadOrCreateIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	cred, err := cfg.WebAuthn.FinishRegistration(identityUser{id: identity}, *session, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "passkey registration failed")
		return
	}
	identity.Credentials = append(identity.Credentials, *cred)
	if err := cfg.Identities.PutIdentity(r.Context(), identity); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save passkey")
		return
	}
	clearCeremonyCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "registered"})
}
```

Modify `internal/lineupapi/handler.go`: add three fields to `Config` (around line 30-37) and register the two new routes + the `/v1/auth/` bypass in `Handler` (around line 46-63):

```go
type Config struct {
	Token         string
	Lineups       ObjectStore
	Runs          RunStore
	Jobs          JobRunner
	Notifications NotificationStore
	Output        OutputStore

	// WebAuthn passkey auth (see webauthn.go).
	Identities    IdentityStore
	WebAuthn      *webauthn.WebAuthn
	SessionSecret []byte
}
```

```go
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/lineup/today", cfg.handleLineup)
	mux.HandleFunc("GET /v1/runs", cfg.handleRuns)
	mux.HandleFunc("GET /v1/runs/{id}", cfg.handleRunDetail)
	mux.HandleFunc("GET /v1/runs/{id}/output", cfg.handleRunOutput)
	mux.HandleFunc("GET /v1/notifications", cfg.handleNotifications)
	mux.HandleFunc("GET /v1/jobs", cfg.handleJobs)
	mux.HandleFunc("POST /v1/jobs/{name}", cfg.handleJob)

	// Auth routes gate themselves (open login, session-or-token register,
	// session-only passkey management in Task 5) instead of the blanket
	// isAuthed check below.
	mux.HandleFunc("POST /v1/auth/register/begin", cfg.handleAuthRegisterBegin)
	mux.HandleFunc("POST /v1/auth/register/finish", cfg.handleAuthRegisterFinish)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			mux.ServeHTTP(w, r)
			return
		}
		if !isAuthed(r, cfg) {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// isAuthed reports whether the request is authenticated by either a valid
// session cookie (the everyday passkey-login path) or the legacy bearer
// token (break-glass / CLI use).
func isAuthed(r *http.Request, cfg Config) bool {
	return hasValidSession(r, cfg.SessionSecret) || authorized(r, cfg.Token)
}
```

Add the import: `"github.com/go-webauthn/webauthn/webauthn"` to `handler.go`'s import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/lineupapi/... -v`
Expected: PASS for all new tests, and every pre-existing test in the package still passes (in particular `TestHandlerAuth` — `isAuthed` must behave identically to the old `authorized`-only check when no session cookie is present).

- [ ] **Step 5: Commit**

```bash
git add internal/lineupapi/webauthn.go internal/lineupapi/webauthn_test.go internal/lineupapi/handler.go
git commit -m "feat(dashboard): add passkey registration endpoints (register/begin, register/finish)"
```

---

## Task 5: Login, passkey management, and logout endpoints

**Files:**
- Modify: `internal/lineupapi/webauthn.go` (append)
- Modify: `internal/lineupapi/handler.go` (register 5 more routes)
- Test: `internal/lineupapi/webauthn_test.go` (append)

**Interfaces:**
- Consumes: everything from Task 1, 2, 4.
- Produces: `handleAuthLoginBegin`, `handleAuthLoginFinish`, `handleListPasskeys`, `handleRevokePasskey`, `handleLogout`, all registered on the router. This completes the `/v1/auth/*` surface — Task 6/7 (serve/lambda wiring) and Task 10-12 (frontend) consume the full set.

- [ ] **Step 1: Write the failing tests**

```go
func TestLoginBegin_NotFoundWhenNoPasskeysRegistered(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginBegin_ReturnsOptionsWhenPasskeyExists(t *testing.T) {
	identities := &fakeIdentities{ok: true, id: &Identity{
		WebAuthnUserID: []byte("handle-123"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var gotCeremonyCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == ceremonyCookieName {
			gotCeremonyCookie = true
		}
	}
	if !gotCeremonyCookie {
		t.Fatal("want a ceremony cookie set on a successful login/begin")
	}
}

func TestLoginFinish_RejectsWithoutCeremonyCookie(t *testing.T) {
	identities := &fakeIdentities{ok: true, id: &Identity{WebAuthnUserID: []byte("h"), Credentials: []webauthn.Credential{{ID: []byte("c")}}}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/finish", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no in-progress ceremony)", rec.Code)
	}
}

func TestListPasskeys_RequiresSession(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no session", rec.Code)
	}

	// Bearer token alone (no session) must NOT satisfy this route — passkey
	// management is a logged-in-browser action, not a break-glass one.
	req = httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with token but no session", rec.Code)
	}
}

func TestListPasskeys_ReturnsRegisteredIDs(t *testing.T) {
	secret := []byte("s")
	identities := &fakeIdentities{ok: true, id: &Identity{
		WebAuthnUserID: []byte("h"),
		Credentials:    []webauthn.Credential{{ID: []byte("cred-1")}},
	}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, time.Now())
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/passkeys", nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	wantID := base64.RawURLEncoding.EncodeToString([]byte("cred-1"))
	if !strings.Contains(rec.Body.String(), wantID) {
		t.Fatalf("body = %s, want it to contain credential id %s", rec.Body.String(), wantID)
	}
}

func TestRevokePasskey_RemovesMatchingCredential(t *testing.T) {
	secret := []byte("s")
	identities := &fakeIdentities{ok: true, id: &Identity{
		WebAuthnUserID: []byte("h"),
		Credentials: []webauthn.Credential{
			{ID: []byte("keep-me")},
			{ID: []byte("revoke-me")},
		},
	}}
	h := Handler(Config{Token: "t", Identities: identities, WebAuthn: testWebAuthn(t), SessionSecret: secret})

	sessionRec := httptest.NewRecorder()
	setSessionCookie(sessionRec, secret, time.Now())
	targetID := base64.RawURLEncoding.EncodeToString([]byte("revoke-me"))
	req := httptest.NewRequest(http.MethodDelete, "/v1/auth/passkeys/"+targetID, nil)
	for _, c := range sessionRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
	if len(identities.id.Credentials) != 1 || string(identities.id.Credentials[0].ID) != "keep-me" {
		t.Fatalf("credentials after revoke = %+v, want only keep-me left", identities.id.Credentials)
	}
}

func TestLogout_ClearsSessionCookie(t *testing.T) {
	h := Handler(Config{Token: "t", Identities: &fakeIdentities{}, WebAuthn: testWebAuthn(t), SessionSecret: []byte("s")})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("want the session cookie cleared (MaxAge < 0)")
	}
}
```

Add `"encoding/base64"` and `"time"` to `webauthn_test.go`'s imports if not already present from Task 4.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/lineupapi/... -run 'TestLoginBegin|TestLoginFinish|TestListPasskeys|TestRevokePasskey|TestLogout' -v`
Expected: FAIL — handlers/routes don't exist yet (404s where the tests expect 404/200/401/204).

- [ ] **Step 3: Write the implementation**

Append to `internal/lineupapi/webauthn.go`:

```go
func (cfg Config) handleAuthLoginBegin(w http.ResponseWriter, r *http.Request) {
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	if !ok || len(identity.Credentials) == 0 {
		writeErr(w, http.StatusNotFound, "no passkeys registered yet")
		return
	}
	assertion, session, err := cfg.WebAuthn.BeginLogin(identityUser{id: identity})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not begin login")
		return
	}
	if err := setCeremonyCookie(w, session); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not start ceremony")
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

func (cfg Config) handleAuthLoginFinish(w http.ResponseWriter, r *http.Request) {
	session, err := ceremonySessionFromRequest(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "login session expired, try again")
		return
	}
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil || !ok {
		writeErr(w, http.StatusUnauthorized, "login failed")
		return
	}
	cred, err := cfg.WebAuthn.FinishLogin(identityUser{id: identity}, *session, r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "login failed")
		return
	}
	// Persist the updated sign counter / clone-warning flag. Best-effort: a
	// store failure here shouldn't fail a login that already verified.
	for i := range identity.Credentials {
		if bytes.Equal(identity.Credentials[i].ID, cred.ID) {
			identity.Credentials[i] = *cred
		}
	}
	_ = cfg.Identities.PutIdentity(r.Context(), identity)

	clearCeremonyCookie(w)
	setSessionCookie(w, cfg.SessionSecret, time.Now())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type passkeyOut struct {
	ID string `json:"id"`
}

func (cfg Config) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	if !hasValidSession(r, cfg.SessionSecret) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "identity store unavailable")
		return
	}
	out := []passkeyOut{}
	if ok {
		for _, c := range identity.Credentials {
			out = append(out, passkeyOut{ID: base64.RawURLEncoding.EncodeToString(c.ID)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

func (cfg Config) handleRevokePasskey(w http.ResponseWriter, r *http.Request) {
	if !hasValidSession(r, cfg.SessionSecret) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	targetID, err := base64.RawURLEncoding.DecodeString(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid passkey id")
		return
	}
	identity, ok, err := cfg.Identities.GetIdentity(r.Context())
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "no passkeys registered")
		return
	}
	kept := identity.Credentials[:0]
	for _, c := range identity.Credentials {
		if !bytes.Equal(c.ID, targetID) {
			kept = append(kept, c)
		}
	}
	identity.Credentials = kept
	if err := cfg.Identities.PutIdentity(r.Context(), identity); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (cfg Config) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
```

Add `"bytes"` to `webauthn.go`'s import block (introduced in Task 4 without it — this task is the first to need it).

Modify `internal/lineupapi/handler.go`'s `Handler` function to register the five remaining routes next to the two from Task 4:

```go
	mux.HandleFunc("POST /v1/auth/register/begin", cfg.handleAuthRegisterBegin)
	mux.HandleFunc("POST /v1/auth/register/finish", cfg.handleAuthRegisterFinish)
	mux.HandleFunc("POST /v1/auth/login/begin", cfg.handleAuthLoginBegin)
	mux.HandleFunc("POST /v1/auth/login/finish", cfg.handleAuthLoginFinish)
	mux.HandleFunc("GET /v1/auth/passkeys", cfg.handleListPasskeys)
	mux.HandleFunc("DELETE /v1/auth/passkeys/{id}", cfg.handleRevokePasskey)
	mux.HandleFunc("POST /v1/auth/logout", cfg.handleLogout)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/lineupapi/... -v`
Expected: PASS — every test in the package, old and new.

- [ ] **Step 5: Run go vet and go mod tidy**

Run: `go vet ./... && go mod tidy`
Expected: no errors, no diff in `go.mod`/`go.sum` beyond what Task 2 already added.

- [ ] **Step 6: Commit**

```bash
git add internal/lineupapi/webauthn.go internal/lineupapi/webauthn_test.go internal/lineupapi/handler.go
git commit -m "feat(dashboard): add passkey login, management, and logout endpoints"
```

---

## Task 6: Local `serve --web` wiring

**Files:**
- Modify: `cmd/serve.go`
- Modify: `cmd/serve_test.go`

**Interfaces:**
- Consumes: `lineupapi.NewFileIdentityStore`, `lineupapi.NewWebAuthn` (Task 2/4), `lineupapi.Config.{Identities,WebAuthn,SessionSecret}` (Task 4).
- Produces: `newServeMux` gains a `sessionSecret []byte` parameter; `runServe` reads a new required `ROSTERBOT_SESSION_SECRET` env var. Nothing outside `cmd/serve.go` depends on this.

- [ ] **Step 1: Write the failing test**

Add to `cmd/serve_test.go`:

```go
func TestServeMux_AuthRoutesWork(t *testing.T) {
	lineupDir := t.TempDir()
	mux := newServeMux("test-token", []byte("test-session-secret"), lineupDir, "")

	// No identity registered yet: login/begin is 404, not 401/500.
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/begin", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /v1/auth/login/begin (no identity) = %d, want 404", rec.Code)
	}

	// register/begin with the bootstrap token succeeds and sets a ceremony cookie.
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/register/begin", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/auth/register/begin = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run TestServeMux_AuthRoutesWork -v`
Expected: FAIL — `newServeMux` doesn't accept a second `[]byte` argument yet.

- [ ] **Step 3: Write the implementation**

Modify `cmd/serve.go`:

```go
func newServeMux(token string, sessionSecret []byte, lineupDir, webDir string) http.Handler {
	wa, err := lineupapi.NewWebAuthn("localhost", "http://localhost:8080", "rosterbot (local)")
	if err != nil {
		// Config is static (RPID/origin are compile-time constants for local
		// dev); a validation failure here means a coding mistake, not a
		// runtime condition callers should handle.
		panic("newServeMux: invalid local WebAuthn config: " + err.Error())
	}

	apiHandler := lineupapi.Handler(lineupapi.Config{
		Token:         token,
		Lineups:       lineupapi.NewFileStore(lineupDir),
		Runs:          lineupapi.NewFileRunStore(lineupDir + "/runs"),
		Notifications: lineupapi.NewFileNotificationStore(lineupDir + "/notifications"),
		Output:        lineupapi.NewFileOutputStore(lineupDir + "/outputs"),
		Identities:    lineupapi.NewFileIdentityStore(lineupDir),
		WebAuthn:      wa,
		SessionSecret: sessionSecret,
		// Jobs is nil locally: triggering real ECS tasks only makes sense on AWS.
		// POST /v1/jobs/* returns 501 from `serve`.
	})

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	if webDir != "" {
		if _, err := os.Stat(webDir); err == nil {
			mux.Handle("/", http.FileServer(http.Dir(webDir)))
		}
	}
	return mux
}

func runServe(cmd *cobra.Command, args []string) error {
	token := os.Getenv("ROSTERBOT_API_TOKEN")
	if token == "" {
		return fmt.Errorf("ROSTERBOT_API_TOKEN is not set — the server needs a bearer token to authenticate requests")
	}
	sessionSecret := os.Getenv("ROSTERBOT_SESSION_SECRET")
	if sessionSecret == "" {
		return fmt.Errorf("ROSTERBOT_SESSION_SECRET is not set — the server needs a secret to sign passkey login sessions")
	}
	if _, err := os.Stat(serveWebDir); err != nil {
		fmt.Printf("serving lineup API on %s (reading %s; jobs disabled locally; dashboard not served: %s not found)\n", serveAddr, serveDir, serveWebDir)
	} else {
		fmt.Printf("serving lineup API + dashboard on %s (reading %s; jobs disabled locally)\n", serveAddr, serveDir)
	}
	return http.ListenAndServe(serveAddr, newServeMux(token, []byte(sessionSecret), serveDir, serveWebDir))
}
```

Update the two existing calls to `newServeMux` in `cmd/serve_test.go` (`TestServeMux_RoutesAPIAndStatic`, `TestServeMux_NoWebDirConfigured`) to pass a session secret as the second argument, e.g. `newServeMux("test-token", []byte("test-session-secret"), lineupDir, webDir)`.

Update the `serveCmd.Long` help text (the block starting `` `Serve the read-only lineup API...` ``) to mention `ROSTERBOT_SESSION_SECRET` alongside `ROSTERBOT_API_TOKEN`:

```go
	Long: `Serve the read-only lineup API over HTTP for local testing before deploy.

It reads the precomputed JSON written by ` + "`optimize --publish-lineup`" + ` (the
same bytes the deployed Lambda serves) — it does NOT run the optimizer or touch
Fantrax. Requires ROSTERBOT_API_TOKEN (bootstrap/break-glass auth) and
ROSTERBOT_SESSION_SECRET (signs passkey login sessions); requests need either
a valid "rosterbot_session" cookie (set by a passkey login) or an
"Authorization: Bearer <token>" header.

It also serves the dashboard's static files (web/dashboard by default) at "/",
same-origin with the API — the same split CloudFront does in production between
its default behavior (static files) and its "/v1/*" behavior (the Lambda API),
so the dashboard's relative "/v1/..." fetches behave identically in both places.
WebAuthn is configured for RPID "localhost" — passkeys work against
http://localhost:8080 in real browsers (Chrome/Safari treat localhost as a
secure context), no HTTPS or mocking required for local testing.

Typical local flow:
  go run . optimize --dry-run --publish-lineup   # writes .lineup/lineup-today.json
  ROSTERBOT_API_TOKEN=test ROSTERBOT_SESSION_SECRET=test-secret go run . serve
  open http://localhost:8080/                    # dashboard: set up a passkey, then log in with it
  curl -H "Authorization: Bearer test" localhost:8080/v1/lineup/today`,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/... -v`
Expected: PASS for all tests in `cmd`, including the updated `TestServeMux_RoutesAPIAndStatic` and `TestServeMux_NoWebDirConfigured`.

- [ ] **Step 5: Run go vet**

Run: `go vet ./...`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add cmd/serve.go cmd/serve_test.go
git commit -m "feat(dashboard): wire passkey auth into local serve --web"
```

---

## Task 7: Lambda wiring

**Files:**
- Modify: `lambda/main.go`

**Interfaces:**
- Consumes: `s3lineup.NewIdentity` (Task 3), `lineupapi.NewWebAuthn` (Task 4), `lineupapi.Config.{Identities,WebAuthn,SessionSecret}` (Task 4).
- Produces: reads `RP_ID`, `RP_ORIGIN` env vars (set by Task 8's CDK change) and a new SSM param (default name `/rosterbot/DASHBOARD_SESSION_SECRET`, overridable via `SESSION_SECRET_PARAM` env var, mirroring the existing `API_TOKEN_PARAM` pattern).

- [ ] **Step 1: Write the implementation**

There's no local way to exercise a real Lambda cold start (it needs live AWS creds + SSM + S3), so this task has no new automated test — consistent with `lambda/main.go` having none today. Correctness is verified by Task 13's manual end-to-end check against the deployed dashboard.

Modify `lambda/main.go`:

```go
	output, err := s3lineup.NewOutput(ctx, bucket, "runs/")
	if err != nil {
		log.Fatalf("init s3 output store: %v", err)
	}
	identities, err := s3lineup.NewIdentity(ctx, bucket, "webauthn/")
	if err != nil {
		log.Fatalf("init s3 identity store: %v", err)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	jobs, err := newECSRunner(ecs.NewFromConfig(cfg))
	if err != nil {
		log.Fatalf("init job runner: %v", err)
	}

	token, err := loadToken(ctx)
	if err != nil {
		log.Fatalf("load API token: %v", err)
	}
	sessionSecret, err := loadSessionSecret(ctx)
	if err != nil {
		log.Fatalf("load session secret: %v", err)
	}
	rpID := os.Getenv("RP_ID")
	rpOrigin := os.Getenv("RP_ORIGIN")
	if rpID == "" || rpOrigin == "" {
		log.Fatal("RP_ID and RP_ORIGIN must be set")
	}
	wa, err := lineupapi.NewWebAuthn(rpID, rpOrigin, "rosterbot")
	if err != nil {
		log.Fatalf("init webauthn config: %v", err)
	}

	handler := lineupapi.Handler(lineupapi.Config{
		Token:         token,
		Lineups:       lineups,
		Runs:          runs,
		Jobs:          jobs,
		Notifications: notifs,
		Output:        output,
		Identities:    identities,
		WebAuthn:      wa,
		SessionSecret: []byte(sessionSecret),
	})
	lambda.Start(adapt(handler))
}
```

Add a `loadSessionSecret` function next to the existing `loadToken` (same file):

```go
// loadSessionSecret reads the session-cookie HMAC secret from SSM Parameter
// Store (SecureString) named by SESSION_SECRET_PARAM. Fetched once at cold
// start, mirroring loadToken.
func loadSessionSecret(ctx context.Context) (string, error) {
	name := os.Getenv("SESSION_SECRET_PARAM")
	if name == "" {
		name = "/rosterbot/DASHBOARD_SESSION_SECRET"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", err
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: boolPtr(true),
	})
	if err != nil {
		return "", err
	}
	return *out.Parameter.Value, nil
}
```

Add the import: `"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"` is already imported (used by `lineups`/`runs`/etc.); no new import package needed since `s3lineup.NewIdentity` lives in the same already-imported package, and `lineupapi.NewWebAuthn` lives in the already-imported `lineupapi` package.

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./lambda/...`
Expected: no errors.

- [ ] **Step 3: Run go vet**

Run: `go vet ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add lambda/main.go
git commit -m "feat(dashboard): wire passkey auth into the Lambda API"
```

---

## Task 8: CDK infra changes

**Files:**
- Modify: `infra/infra.go`

**Interfaces:**
- Consumes: nothing new from earlier tasks (this is infra-only).
- Produces: SSM param `/rosterbot/DASHBOARD_SESSION_SECRET`, Lambda env vars `SESSION_SECRET_PARAM`, `RP_ID`, `RP_ORIGIN`, IAM grants, S3 `webauthn/*` read-write grant. Consumed at runtime by Task 7.

- [ ] **Step 1: Add the SSM env var + IAM grant to the Lambda's existing block**

`infra/infra.go` around line 217-237 (the `apiFn` `GoFunction` and its policy statements): add `SESSION_SECRET_PARAM` to `Environment`, and extend the `ssm:GetParameter` policy's `Resources` to include the new param ARN:

```go
	apiFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("LineupApi"), &awscdklambdagoalpha.GoFunctionProps{
		Entry: jsii.String("../lambda"),
		// Pin to provided.al2023: provided.al2 (the GoFunction default) loses
		// support 2026-07-31. The Go binary is statically linked, so the AL
		// version under it is immaterial — this is a base-OS swap only.
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		Environment: &map[string]*string{
			"STATE_BUCKET":         stateBucket.BucketName(),
			"API_TOKEN_PARAM":      jsii.String("/rosterbot/ROSTERBOT_API_TOKEN"),
			"SESSION_SECRET_PARAM": jsii.String("/rosterbot/DASHBOARD_SESSION_SECRET"),
			"CLUSTER":              cluster.ClusterArn(),
			"TASK_DEF":             taskDef.TaskDefinitionArn(),
			"SUBNETS":              awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds),
			"SECURITY_GROUPS":      taskSg.SecurityGroupId(),
			"CONTAINER_NAME":       jsii.String("bot"),
		},
	})
	// Least privilege: read lineup/ + the run ledger/output objects + the one
	// token param. runledger/ is the ledger (rosterbot-432); runs/ is still
	// read for per-run captured output blobs (runs/<id>/output.json).
	stateBucket.GrantRead(apiFn, jsii.String("lineup/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runledger/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runs/*"))
	stateBucket.GrantRead(apiFn, jsii.String("notifications/*"))
	// webauthn/ holds the single Identity record and is read-modify-written
	// on every registration and login (sign-counter update).
	stateBucket.GrantReadWrite(apiFn, jsii.String("webauthn/*"))
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: jsii.Strings("ssm:GetParameter"),
		Resources: jsii.Strings(
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/ROSTERBOT_API_TOKEN",
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/DASHBOARD_SESSION_SECRET",
		),
	}))
```

- [ ] **Step 2: Add RP_ID/RP_ORIGIN after the dashboard distribution exists**

The Lambda's RP config needs the dashboard's own CloudFront domain, but that distribution (`dashboardDist`) is constructed later in the file (it takes `apiFn`'s Function URL as an origin, so it can't come first). CDK resolves this the standard way: add the environment variables after both resources exist, once `dashboardDist` is in scope. Find the block that creates `dashboardDist` and adds `DashboardUrl`/`DashboardCdnId` outputs (around line 266-285) and add these two lines immediately after:

```go
	awscdk.NewCfnOutput(stack, jsii.String("DashboardCdnId"), &awscdk.CfnOutputProps{Value: dashboardDist.DistributionId()})

	// The Lambda's WebAuthn RP config needs the dashboard's own origin, which
	// only exists once this distribution is created — added here rather than
	// in apiFn's initial Environment map above to break the circular
	// dependency (dashboardDist's origin is apiFn's Function URL).
	apiFn.AddEnvironment(jsii.String("RP_ID"), dashboardDist.DistributionDomainName(), nil)
	apiFn.AddEnvironment(
		jsii.String("RP_ORIGIN"),
		awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dashboardDist.DistributionDomainName()}),
		nil,
	)
```

- [ ] **Step 3: Synth to verify the stack compiles and diffs as expected**

```bash
cd infra
go build ./...
cdk synth -c enableBuild=true > /dev/null
```

Expected: both commands succeed with no errors. `cdk synth` succeeding confirms `AddEnvironment` exists on the generated `GoFunction` binding and the circular-reference pattern resolves correctly at synth time.

- [ ] **Step 4: Bootstrap the new SSM parameter (manual, one-time, run by a human before deploy)**

```bash
openssl rand -base64 48 | tr -d '\n' | aws ssm put-parameter --name /rosterbot/DASHBOARD_SESSION_SECRET --type SecureString --value "$(cat -)" --overwrite
```

- [ ] **Step 5: Deploy**

```bash
cd infra
cdk deploy --require-approval never -c enableBuild=true
```

Expected: deploy succeeds; note the `DashboardUrl` output for Task 13's manual verification.

- [ ] **Step 6: Commit**

```bash
git add infra/infra.go
git commit -m "feat(infra): provision session secret + RP_ID/RP_ORIGIN for passkey auth"
```

---

## Task 9: `webauthn.js` — browser ceremony helpers

**Files:**
- Create: `web/dashboard/webauthn.js`

**Interfaces:**
- Consumes: `api` (Task 10's updated `api.js` — `authRegisterBegin`, `authRegisterFinish`, `authLoginBegin`, `authLoginFinish`).
- Produces: `registerPasskey(bootstrapToken)`, `loginWithPasskey()`. Task 11 (bootstrap/login UI) and Task 12 (add-another-passkey panel) call these.

Note: this task depends on Task 10's `api.js` shape, so implement it together with Task 10 in the same session even though they're separate commits — `webauthn.js`'s imports won't resolve until Task 10 lands.

- [ ] **Step 1: Write the implementation**

```js
// webauthn.js — browser-side WebAuthn ceremony helpers: binary<->base64url
// conversion (the WebAuthn API's credential fields are ArrayBuffers, which
// don't survive JSON.stringify/the server's JSON responses) and the
// register/login ceremony orchestration.
import { api } from "./api.js";

function bufToB64url(buf) {
  const bytes = new Uint8Array(buf);
  let str = "";
  for (const b of bytes) str += String.fromCharCode(b);
  return btoa(str).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function b64urlToBuf(b64url) {
  const pad = (4 - (b64url.length % 4)) % 4;
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat(pad);
  const str = atob(b64);
  const bytes = new Uint8Array(str.length);
  for (let i = 0; i < str.length; i++) bytes[i] = str.charCodeAt(i);
  return bytes.buffer;
}

// decodeCreationOptions/decodeRequestOptions convert the server's JSON (every
// binary field as a base64url string) into the ArrayBuffer shape
// navigator.credentials.create()/.get() require.
function decodeCreationOptions(opts) {
  const pk = opts.publicKey;
  return {
    publicKey: {
      ...pk,
      challenge: b64urlToBuf(pk.challenge),
      user: { ...pk.user, id: b64urlToBuf(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials || []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    },
  };
}

function decodeRequestOptions(opts) {
  const pk = opts.publicKey;
  return {
    publicKey: {
      ...pk,
      challenge: b64urlToBuf(pk.challenge),
      allowCredentials: (pk.allowCredentials || []).map((c) => ({ ...c, id: b64urlToBuf(c.id) })),
    },
  };
}

// encodeAttestation/encodeAssertion convert navigator.credentials' result
// back into the JSON shape the server's protocol.ParseCredentialCreationResponse
// / ParseCredentialRequestResponse expect. `id` is already a base64url string
// per the WebAuthn spec (the browser computes it, unlike rawId).
function encodeAttestation(cred) {
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      attestationObject: bufToB64url(cred.response.attestationObject),
      clientDataJSON: bufToB64url(cred.response.clientDataJSON),
    },
  };
}

function encodeAssertion(cred) {
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      authenticatorData: bufToB64url(cred.response.authenticatorData),
      clientDataJSON: bufToB64url(cred.response.clientDataJSON),
      signature: bufToB64url(cred.response.signature),
      userHandle: cred.response.userHandle ? bufToB64url(cred.response.userHandle) : null,
    },
  };
}

// registerPasskey runs a full registration ceremony. bootstrapToken is the
// ROSTERBOT_API_TOKEN, required only when setting up the very first passkey
// (no session cookie exists yet); omit it when adding an additional device
// while already logged in.
export async function registerPasskey(bootstrapToken) {
  const opts = await api.authRegisterBegin(bootstrapToken);
  const cred = await navigator.credentials.create(decodeCreationOptions(opts));
  await api.authRegisterFinish(encodeAttestation(cred), bootstrapToken);
}

// loginWithPasskey runs a full login ceremony against the one registered
// identity and, on success, leaves the browser holding a session cookie.
export async function loginWithPasskey() {
  const opts = await api.authLoginBegin();
  const cred = await navigator.credentials.get(decodeRequestOptions(opts));
  await api.authLoginFinish(encodeAssertion(cred));
}
```

- [ ] **Step 2: Cross-check the wire shape against the actual library types**

Run: `go doc github.com/go-webauthn/webauthn/protocol.CredentialCreationResponse` and `go doc github.com/go-webauthn/webauthn/protocol.CredentialAssertionResponse` (after Task 2's `go get`). Confirm the JSON field names (`id`, `rawId`, `type`, `response.attestationObject`/`clientDataJSON` for creation; `response.authenticatorData`/`clientDataJSON`/`signature`/`userHandle` for assertion) match what `encodeAttestation`/`encodeAssertion` above produce. If the library's actual struct tags differ, adjust the two functions to match — this is the one piece of this plan derived from the WebAuthn spec rather than a directly-observed library example, so treat the `go doc` output as authoritative over the code above. Task 13's manual browser test is the final confirmation either way.

- [ ] **Step 3: Commit**

```bash
git add web/dashboard/webauthn.js
git commit -m "feat(dashboard): add browser-side WebAuthn ceremony helpers"
```

---

## Task 10: `api.js` — drop the token, add auth endpoints

**Files:**
- Modify: `web/dashboard/api.js`

**Interfaces:**
- Consumes: nothing new.
- Produces: `api.authLoginBegin()`, `api.authLoginFinish(assertion)`, `api.authRegisterBegin(bootstrapToken?)`, `api.authRegisterFinish(attestation, bootstrapToken?)`, `api.authPasskeys()`, `api.authRevokePasskey(id)`, `api.authLogout()`. Removes `getToken`/`setToken`/`clearToken`/`isLoggedIn`/`TOKEN_KEY`. Task 9, 11, 12 all import from this file.

- [ ] **Step 1: Rewrite the file**

```js
// api.js — thin fetch wrapper around the rosterbot control API. Every request
// is a same-origin relative path (CloudFront path-routes /v1/* to the Lambda
// in production; `rosterbot serve --web` does the same locally), so no CORS
// handling is needed anywhere in this app. Auth rides a same-origin,
// httpOnly session cookie set by the passkey login ceremony — there is no
// client-readable token to store.

// ApiError carries the HTTP status so callers can special-case 401
// (unauthenticated), 404 (nothing published yet / no passkeys registered),
// 409 (job already running), and 501 (job triggering unavailable locally)
// without parsing message strings.
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function request(method, path, body, bootstrapToken) {
  const headers = body ? { "Content-Type": "application/json" } : {};
  if (bootstrapToken) headers["Authorization"] = "Bearer " + bootstrapToken;
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const data = await res.json();
      if (data.error) msg = data.error;
    } catch {
      // body wasn't JSON; keep statusText
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return null;
  return res.json();
}

export const api = {
  lineupToday: () => request("GET", "/v1/lineup/today"),
  runs: (limit = 25) => request("GET", `/v1/runs?limit=${limit}`),
  runDetail: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}`),
  runOutput: (id) => request("GET", `/v1/runs/${encodeURIComponent(id)}/output`),
  notifications: (limit = 25) => request("GET", `/v1/notifications?limit=${limit}`),
  jobs: () => request("GET", "/v1/jobs"),
  triggerJob: (name, params) => request("POST", `/v1/jobs/${encodeURIComponent(name)}`, { params }),

  authLoginBegin: () => request("POST", "/v1/auth/login/begin"),
  authLoginFinish: (assertion) => request("POST", "/v1/auth/login/finish", assertion),
  authRegisterBegin: (bootstrapToken) => request("POST", "/v1/auth/register/begin", undefined, bootstrapToken),
  authRegisterFinish: (attestation, bootstrapToken) =>
    request("POST", "/v1/auth/register/finish", attestation, bootstrapToken),
  authPasskeys: () => request("GET", "/v1/auth/passkeys"),
  authRevokePasskey: (id) => request("DELETE", `/v1/auth/passkeys/${encodeURIComponent(id)}`),
  authLogout: () => request("POST", "/v1/auth/logout"),
};
```

- [ ] **Step 2: Manual verification**

```bash
ROSTERBOT_API_TOKEN=test ROSTERBOT_SESSION_SECRET=test-secret go run . serve
curl -i -X POST localhost:8080/v1/auth/login/begin
```

Expected: `404` with an `{"error":"no passkeys registered yet"}` body (no identity registered yet in a fresh `.lineup/` dir) — confirms `api.js`'s method names line up with the server routes wired in Task 4/5/6. This step doesn't yet exercise the browser fetch wrapper (Task 13 does), just confirms the backend contract this file targets.

- [ ] **Step 3: Commit**

```bash
git add web/dashboard/api.js
git commit -m "feat(dashboard): replace token auth with session-cookie auth in api.js"
```

---

## Task 11: Login + bootstrap UI

**Files:**
- Modify: `web/dashboard/app.js`
- Modify: `web/dashboard/index.html`
- Modify: `web/dashboard/style.css`

**Interfaces:**
- Consumes: `api`, `ApiError` (Task 10); `registerPasskey`, `loginWithPasskey` (Task 9).
- Produces: the dashboard's login gate now offers "Sign in with passkey" (existing identity) or a first-run bootstrap form (paste the token once) instead of the old token-paste screen. Task 12 adds a "Passkeys" nav entry the shell can route to once logged in.

- [ ] **Step 1: Rewrite `index.html`'s login section**

Replace the `<div id="login-screen">...</div>` block with two screens:

```html
<div id="login-screen">
  <div class="login-card">
    <h1>rosterbot</h1>
    <button id="passkey-login-btn" type="button">Sign in with passkey</button>
    <p id="login-error" class="error"></p>
  </div>
</div>

<div id="bootstrap-screen" hidden>
  <form id="bootstrap-form" class="login-card">
    <h1>rosterbot</h1>
    <p class="muted">No passkey is registered yet. Paste the API token once to set one up.</p>
    <label for="bootstrap-token-input">API token</label>
    <input id="bootstrap-token-input" type="password" autocomplete="off" placeholder="Paste ROSTERBOT_API_TOKEN">
    <button type="submit">Set up passkey</button>
    <p id="bootstrap-error" class="error"></p>
  </form>
</div>
```

(Leave the `<div id="shell" hidden>...</div>` block below it unchanged for now — Task 12 adds a nav entry inside it.)

- [ ] **Step 2: Rewrite `app.js`**

```js
// app.js — login/bootstrap gate + hash router + nav wiring. ROUTES maps a URL
// hash to a view's render(root) function; later tasks add entries here.
import { api, ApiError } from "./api.js";
import { registerPasskey, loginWithPasskey } from "./webauthn.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";
import { renderRuns } from "./runs.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
  "#runs": renderRuns,
};
const DEFAULT_ROUTE = "#lineup";

const root = document.getElementById("view-root");
const loginScreen = document.getElementById("login-screen");
const bootstrapScreen = document.getElementById("bootstrap-screen");
const shell = document.getElementById("shell");
const passkeyLoginBtn = document.getElementById("passkey-login-btn");
const loginError = document.getElementById("login-error");
const bootstrapForm = document.getElementById("bootstrap-form");
const bootstrapTokenInput = document.getElementById("bootstrap-token-input");
const bootstrapError = document.getElementById("bootstrap-error");
const logoutBtn = document.getElementById("logout-btn");

async function boot() {
  try {
    await api.jobs();
    showShell();
    return;
  } catch (err) {
    if (!(err instanceof ApiError) || err.status !== 401) {
      // Non-auth failure (e.g. a network hiccup): don't lock the user out on
      // a transient error. Individual views handle their own load failures.
      showShell();
      return;
    }
  }
  await showLoginOrBootstrap();
}

// showLoginOrBootstrap decides which pre-login screen to show by probing
// login/begin: a 404 means no identity has ever been registered (first run,
// or every passkey was revoked/lost), so the token-bootstrap screen is the
// only way forward; any other outcome means a real login attempt is possible.
async function showLoginOrBootstrap() {
  try {
    await api.authLoginBegin();
    showLogin();
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      showBootstrap();
    } else {
      showLogin("Could not reach the login API.");
    }
  }
}

function showLogin(message) {
  loginScreen.hidden = false;
  bootstrapScreen.hidden = true;
  shell.hidden = true;
  loginError.textContent = message || "";
}

function showBootstrap(message) {
  loginScreen.hidden = true;
  bootstrapScreen.hidden = false;
  shell.hidden = true;
  bootstrapError.textContent = message || "";
}

function showShell() {
  loginScreen.hidden = true;
  bootstrapScreen.hidden = true;
  shell.hidden = false;
  window.addEventListener("hashchange", route);
  route();
}

passkeyLoginBtn.addEventListener("click", async () => {
  loginError.textContent = "";
  try {
    await loginWithPasskey();
    showShell();
  } catch (err) {
    loginError.textContent = "Passkey sign-in failed or was cancelled.";
  }
});

bootstrapForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  const token = bootstrapTokenInput.value.trim();
  if (!token) return;
  bootstrapError.textContent = "";
  try {
    await registerPasskey(token);
    bootstrapTokenInput.value = "";
    showShell();
  } catch (err) {
    bootstrapError.textContent = "Setup failed — check the token and try again.";
  }
});

logoutBtn.addEventListener("click", async () => {
  try {
    await api.authLogout();
  } catch {
    // Logging out is best-effort client-side too — clearing local UI state
    // shouldn't hang on a failed network call.
  }
  window.location.hash = "";
  showLogin();
});

function route() {
  const hash = window.location.hash || DEFAULT_ROUTE;
  const render = ROUTES[hash] || ROUTES[DEFAULT_ROUTE];
  document.querySelectorAll("nav a").forEach((a) => {
    a.classList.toggle("active", a.getAttribute("href") === hash);
  });
  root.innerHTML = "";
  render(root);
}

boot();
```

- [ ] **Step 3: Update `style.css`**

The existing `#login-screen` rules (flex-centered full-height card) stay as-is; add matching rules for the new `#bootstrap-screen` right below them in `style.css`:

```css
#bootstrap-screen {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: 1rem;
}
#bootstrap-screen[hidden] { display: none; }
```

Rename the existing `#login-screen form` selector (if `style.css` targets the form directly) to also match `.login-card` so both screens share styling — check the current rule (likely `#login-screen form { ... }` given `index.html`'s original structure had the form as the screen's only child) and change it to `#login-screen .login-card, #bootstrap-screen .login-card`.

- [ ] **Step 4: Manual verification**

```bash
ROSTERBOT_API_TOKEN=test ROSTERBOT_SESSION_SECRET=test-secret go run . serve
open http://localhost:8080/
```

Expected, in a real browser with a platform authenticator (Touch ID / Windows Hello) available:
1. First load shows the bootstrap screen ("No passkey is registered yet").
2. Paste `test` into the token field, submit — a Touch ID/Windows Hello prompt appears, then the dashboard shell loads.
3. Reload the page — this time it goes straight to the shell (existing session cookie still valid), no login screen at all.
4. Click "Log out" — returns to the login screen (not bootstrap, since a passkey is now registered).
5. Click "Sign in with passkey" — Touch ID/Windows Hello prompts again, dashboard shell loads.

- [ ] **Step 5: Commit**

```bash
git add web/dashboard/app.js web/dashboard/index.html web/dashboard/style.css
git commit -m "feat(dashboard): replace token-paste login with passkey login + bootstrap screen"
```

---

## Task 12: Passkeys management panel

**Files:**
- Create: `web/dashboard/passkeys.js`
- Modify: `web/dashboard/app.js` (add route)
- Modify: `web/dashboard/index.html` (add nav link)

**Interfaces:**
- Consumes: `api`, `ApiError` (Task 10); `registerPasskey` (Task 9); `escapeHtml` (existing `render.js`).
- Produces: `renderPasskeys(root)`, wired into `ROUTES["#passkeys"]`.

- [ ] **Step 1: Write `passkeys.js`**

```js
// passkeys.js — lists registered passkeys and lets you add another device or
// revoke one you've lost. Mirrors jobs.js's card-per-item + error-card style.
import { api, ApiError } from "./api.js";
import { registerPasskey } from "./webauthn.js";
import { escapeHtml } from "./render.js";

export async function renderPasskeys(root) {
  root.innerHTML = "<p class=\"muted\">Loading passkeys…</p>";
  let passkeys;
  try {
    const resp = await api.authPasskeys();
    passkeys = resp.passkeys;
  } catch (err) {
    root.innerHTML = "";
    root.appendChild(errorCard(err));
    return;
  }
  root.innerHTML = "";
  root.appendChild(addButton(root));
  for (const pk of passkeys) {
    root.appendChild(passkeyCard(pk));
  }
}

function addButton(root) {
  const wrapper = document.createElement("div");
  wrapper.className = "card";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.textContent = "Add another passkey";
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    try {
      await registerPasskey();
      await renderPasskeys(root.parentElement || root);
    } catch (err) {
      btn.disabled = false;
      const p = document.createElement("p");
      p.className = "error";
      p.textContent = "Could not add a passkey — try again.";
      wrapper.appendChild(p);
    }
  });
  wrapper.appendChild(btn);
  return wrapper;
}

function passkeyCard(pk) {
  const card = document.createElement("div");
  card.className = "card";

  const id = document.createElement("p");
  id.textContent = "Passkey " + pk.id.slice(0, 12) + "…";
  card.appendChild(id);

  const revoke = document.createElement("button");
  revoke.type = "button";
  revoke.className = "danger";
  revoke.textContent = "Revoke";
  revoke.addEventListener("click", async () => {
    if (!confirm("Revoke this passkey? Its device will no longer be able to sign in.")) return;
    try {
      await api.authRevokePasskey(pk.id);
      card.remove();
    } catch (err) {
      alert("Could not revoke: " + escapeHtml(err.message));
    }
  });
  card.appendChild(revoke);

  return card;
}

function errorCard(err) {
  const card = document.createElement("div");
  card.className = "card";
  const p = document.createElement("p");
  p.className = "error";
  p.textContent = err instanceof ApiError ? err.message : "Could not load passkeys.";
  card.appendChild(p);
  return card;
}
```

- [ ] **Step 2: Wire the route and nav link**

In `app.js`, add the import and route entry:

```js
import { renderPasskeys } from "./passkeys.js";

const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
  "#runs": renderRuns,
  "#passkeys": renderPasskeys,
};
```

In `index.html`, add a nav link inside the existing `<nav>` block (next to the `Runs` link, before the external Recap/Projections links):

```html
      <a href="#runs">Runs</a>
      <a href="#passkeys">Passkeys</a>
```

- [ ] **Step 3: Manual verification**

```bash
ROSTERBOT_API_TOKEN=test ROSTERBOT_SESSION_SECRET=test-secret go run . serve
open http://localhost:8080/
```

Expected: log in (per Task 11's flow), click "Passkeys" in the nav — see the one passkey registered during bootstrap. Click "Add another passkey" — Touch ID/Windows Hello prompts for a *new* credential (register a second fingerprint/device if available, or the same device again — the library's `WithExclusions` isn't wired in this plan, so re-registering the same authenticator is allowed and simply adds a second credential entry); the new passkey appears in the list. Click "Revoke" on one — confirm dialog, then it disappears from the list. Log out and confirm the revoked passkey can no longer sign in (only the remaining one can, if two were registered).

- [ ] **Step 4: Commit**

```bash
git add web/dashboard/passkeys.js web/dashboard/app.js web/dashboard/index.html
git commit -m "feat(dashboard): add passkey management panel (list, add, revoke)"
```

---

## Task 13: Docs + production rollout verification

**Files:**
- Modify: `docs/aws-deployment.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing consumed by other tasks; this is the last task.

- [ ] **Step 1: Update `docs/aws-deployment.md`**

Find the dashboard bullet (currently starting `- **S3 dashboard bucket**...`, around line 26) and the "Lineup + control API" bullet (around line 16) and extend both to mention passkey auth. Append after the existing "Lineup + control API" bullet's text (which currently ends `...on the task/execution roles. Tasks it launches use a dedicated egress-only SG (TaskSg) in the default VPC's public subnets. See the README "Lineup HTTP API" section for the contract.`):

```markdown
  Auth accepts either a signed session cookie (set by a successful passkey login, `/v1/auth/*`
  routes) or the legacy Bearer token — the token is no longer surfaced in the dashboard's normal UI
  after the first passkey is registered, but stays wired in as a break-glass/recovery credential (see
  "Passkey auth" below).
```

Add a new bullet after the dashboard bullet describing the new pieces:

```markdown
- **Passkey auth** — the dashboard's real login is WebAuthn (`internal/lineupapi/webauthn.go`,
  library `github.com/go-webauthn/webauthn`). One `Identity` record (a stable user handle + every
  registered passkey) lives at `webauthn/identity.json` in the state bucket. Sessions are a
  stateless HMAC-signed cookie — no session datastore — signed with a new SSM SecureString,
  `/rosterbot/DASHBOARD_SESSION_SECRET`, bootstrapped the same way as the token:
  `aws ssm put-parameter --name /rosterbot/DASHBOARD_SESSION_SECRET --type SecureString --value '<random 48+ bytes>'`.
  `RP_ID`/`RP_ORIGIN` are set on the Lambda via `apiFn.AddEnvironment` *after* `DashboardCdn` is
  constructed (the distribution's origin is the Lambda's own Function URL, so the two resources
  reference each other — CDK's standard fix is adding the env var post-construction instead of in
  the Lambda's initial props). To register the very first passkey (or recover after every passkey is
  lost), paste `ROSTERBOT_API_TOKEN` into the dashboard's bootstrap screen — it only appears when
  zero passkeys are registered.
```

- [ ] **Step 2: Update `README.md`**

Find the dashboard/"Lineup HTTP API" section README.md documents (added in PR #63) and update its auth description from "paste your token" to describe the passkey flow, keeping the token documented as the bootstrap/recovery path. Match whatever heading structure already exists there (read the current section before editing — don't guess the heading text).

- [ ] **Step 3: Full-repo smoke test**

```bash
make clean-cache && make run-all
```

Expected: every command still runs clean in dry-run/read-only mode (this change doesn't touch any command outside `serve`, but the smoke test catches any accidental cross-package breakage).

- [ ] **Step 4: Production rollout (manual, run by a human, not part of the coding tasks)**

This is the design's dual-running rollout, done for real against the deployed dashboard — not scripted, since it requires an actual browser with a real platform authenticator against the live CloudFront URL:

1. Confirm Task 8 deployed successfully and `DashboardUrl` loads.
2. Open the dashboard, register a real passkey on your phone (bootstrap screen, paste the real token).
3. Open the dashboard on your laptop, log in with that passkey (cross-device passkey sync or a QR-code hybrid transport, depending on your platform), then register a second passkey directly on the laptop from the Passkeys panel.
4. Confirm login works from both devices independently after a full log-out.
5. Only after both are confirmed working: this plan's Task 11 already ships bootstrap+login screens dual-running with nothing to "remove" — the old token-paste-only screen was already replaced in Task 11, so there is no separate removal step. (The design doc's Rollout section anticipated a dual-running period before deleting the token screen; this plan instead ships the passkey screen as the only screen from Task 11 onward, with the token demoted to the bootstrap/recovery path from the start, since keeping two parallel login UIs alive in the same small app for a single operator adds more risk — a stale code path that's easy to forget to remove — than it saves. If this checkpoint reveals a passkey-login problem in production, temporarily reintroduce a token-only login fallback by reverting Task 11's commit rather than debugging live.)

- [ ] **Step 5: Commit**

```bash
git add docs/aws-deployment.md README.md
git commit -m "docs: document passkey auth for the dashboard"
```

---

## Self-Review Notes

- **Spec coverage:** every row of the design doc's endpoint table (register/login begin+finish, passkeys list/revoke, logout) has a task (4, 5). Identity storage (file + S3) is Tasks 2-3. Session/ceremony cookies are Tasks 1 and 4. Infra (SSM param, IAM, env vars) is Task 8. Frontend (webauthn.js, api.js, login/bootstrap UI, passkeys panel) is Tasks 9-12. Docs are Task 13.
- **Deviation from the design doc, called out explicitly:** the design doc's Rollout section described shipping the passkey UI *alongside* the old token-paste screen, then removing the token screen after production verification. This plan ships the passkey/bootstrap screen as the *only* login UI from Task 11 onward (the token demotes to the bootstrap/recovery path immediately, no dual-running period in the code). Rationale is in Task 13 Step 4 — for a single-operator app, a temporarily-dual-running login UI is extra surface to keep in sync and remember to delete, and Task 11's manual verification step already gives a safe checkpoint before Task 13's real production rollout. If this trade-off is wrong, say so before Task 11 starts.
- **Testing gap, by design:** the happy-path crypto of `register/finish` and `login/finish` (a real signed attestation/assertion from an authenticator) is not covered by an automated test anywhere in this plan — building a synthetic signed WebAuthn fixture from scratch is a large, separate undertaking and the design doc's own Testing section already scoped this out in favor of manual verification (Tasks 11/12's browser steps, Task 13's production checkpoint). Every other branch (missing auth, missing/expired ceremony cookie, store errors, session validity) is unit tested.

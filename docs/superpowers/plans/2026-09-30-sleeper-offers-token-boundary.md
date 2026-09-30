# Sleeper Token Boundary + `football-offers` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Grade every trade offer made to the operator on Sleeper before they respond, by reading the undocumented GraphQL API with the operator's own session token through a read-only client whose blast radius is visible in the import graph.

**Architecture:** A new leaf package `internal/sleeperauth` holds the authenticated GraphQL transport (`Query`) and two typed reads (`Me`, `ProposedTrades`); no `Mutate` exists, and a dependency-direction test pins that only `cmd/football_offers.go` imports the package. A new hourly `football-offers` command verifies the token's identity, discovers leagues the same way `football-trades` does (Plan 1's `discoverLeagues`), pulls proposed trades for the operator's roster in each, grades them with the existing `dynasty` grader, and alerts once per transaction id under check → send → mark. Infra adds a second Fargate task definition so the token reaches that one task only. This is Plan 2 of 3 from the spec; Plan 1 (multi-league `football-trades`) is on branch `claude/rosterbot-sleeper-integration-c7318d` / PR #224 and every interface below that says "exists" landed there.

**Tech Stack:** Go 1.2x (root module), cobra, `net/http` + `encoding/json` (no GraphQL library), `internal/sleeper` (public REST client), `internal/dynasty` (grader), `internal/alertmarker`, `internal/notify`, `internal/pushover`, AWS CDK in Go (`infra/`).

**Spec:** `docs/superpowers/specs/2026-09-22-sleeper-multi-league-pickups-offers-design.md` — Sections 1 and 3 as revised 2026-09-28 (the live-offer status is `"proposed"`, not `"pending"`).

## Global Constraints

- **The token never enters code, logs, errors, cache keys, test fixtures or chat.** `SLEEPER_TOKEN` is read from env by `sleeperauth.FromEnv()` only; the operator places it in `.env` locally and in the SSM parameter `/rosterbot/SLEEPER_TOKEN` (SecureString, us-west-1) for the deployment. Test tokens are made-up strings.
- GraphQL endpoint `https://sleeper.app/graphql`; JSON body `{"query","variables"}`; headers `content-type: application/json`, an explicit `User-Agent`, and `authorization: <token>` (raw, no `Bearer`). Errors arrive as HTTP 200 with `errors[]` of `{code, message, path}`; a gated field without a valid token answers `code: "unauthorized"`; a bad token answers HTTP 401. No disk cache anywhere in this package.
- A live trade offer has `status == "proposed"`. Observed statuses: `proposed`, `complete`, `rejected`, `failed`.
- `settings.expires_at` is epoch **seconds**; `created` is epoch **millis**.
- `me` can return `email`, `phone` and `token`; the `Me` query document selects `user_id` only.
- Alert order is check → send → mark; `--dry-run` skips both send and mark; a marker-store failure degrades to a duplicate alert, never silence.
- `ErrUnauthorized` fails the whole run; any other single-league error is warned, that league skipped, and the run exits non-zero at the end (Plan 1's isolation pattern).
- Every unchecked error is a lint failure (`errcheck` excludes only the print family); `nilerr` (a soft-fail must print or wrap its error); `nolintlint` with `require-explanation`; `noctx` (use `http.NewRequestWithContext`); `gofmt`/`golangci-lint` run via hooks.
- Coverage lines print unconditionally, including the zero case.
- New env vars and commands go in `README.md`; internal detail in `docs/dynasty-football.md`; a new `internal/` package with no doc mention fails `TestEveryInternalPackageHasDocCoverage`.
- `TestSeasonGate_EveryScheduledCommandIsClassified` reads the infra schedule table: the command must be classified in `seasonPolicies` before its schedule exists.
- The `secret()` helper in `infra/infra.go` references SSM `/rosterbot/<NAME>`, which **must exist in us-west-1 before deploy** or every task launch that references it fails. Creating it is the operator's action, never the implementer's.
- Operator identity: Sleeper user id `738883211463155712`. Commit after every task; never push from a task step.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/sleeper/types.go` (modify) | `Transaction.Settings` + `ExpiresAt()`. |
| `internal/sleeper/client_test.go` (modify) | Decode/expiry tests. |
| `internal/sleeperauth/client.go` (create) | Package doc, `Endpoint`, `FromEnv`, `New`, `Client.Query`, error mapping, sentinels. |
| `internal/sleeperauth/client_test.go` (create) | httptest transport tests; token-never-in-error. |
| `internal/sleeperauth/reads.go` (create) | `Me`, `ProposedTrades`, query documents, draft-pick shape tolerance. |
| `internal/sleeperauth/reads_test.go` (create) | Document-content and decode tests. |
| `internal/sleeperauth/depcheck_test.go` (create) | Import-direction test. |
| `internal/sleeperauth/diag_proposed_test.go` (create) | `diag`-tagged live probe; operator-run. |
| `internal/statestore/layout/layout.go` (modify) | `FootballOffers` artifact. |
| `internal/statestore/layout/layout_test.go`, `internal/statestore/tenant_test.go` (modify) | Enumeration lists. |
| `internal/statestore/statestore.go` (modify) | `FootballOfferMarkers()`. |
| `cmd/football_offers.go` (create) | Command: identity check, per-league loop, `selectOffers`, `gradeAndAlertOffers`, `formatOfferAlert`. |
| `cmd/football_offers_test.go` (create) | Pure-function and check→send→mark tests. |
| `cmd/season_gate.go` (modify) | `"football-offers": {}`. |
| `infra/infra.go` (modify) | `OffersTask` task definition, `FootballOffers` schedule, `job.taskDef`. |
| `Makefile` (modify) | `run-all` line. |
| `README.md`, `docs/dynasty-football.md` (modify) | Command, env var, token handling, internals. |

---

### Task 1: `Transaction.Settings` and `ExpiresAt`

**Files:**
- Modify: `internal/sleeper/types.go` (the `Transaction` struct; add `time` import)
- Test: `internal/sleeper/client_test.go`

**Interfaces:**
- Produces: `Transaction.Settings map[string]any` (json `settings`), `func (t Transaction) ExpiresAt() (time.Time, bool)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/sleeper/client_test.go`:

```go
func TestTransaction_ExpiresAtReadsEpochSeconds(t *testing.T) {
	const body = `{"transaction_id":"t1","type":"trade","status":"proposed",
	  "created":1788371562251,"settings":{"expires_at":1788630762}}`
	var txn Transaction
	if err := json.Unmarshal([]byte(body), &txn); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := txn.ExpiresAt()
	if !ok {
		t.Fatalf("ExpiresAt: want ok")
	}
	if got.Unix() != 1788630762 {
		t.Errorf("ExpiresAt = %v (unix %d), want unix 1788630762 — settings.expires_at is SECONDS, not millis", got, got.Unix())
	}
	if got.Location() != time.UTC {
		t.Errorf("ExpiresAt location = %v, want UTC", got.Location())
	}
}

func TestTransaction_ExpiresAtAbsentOrMalformedIsNoExpiry(t *testing.T) {
	for name, body := range map[string]string{
		"no settings":     `{"transaction_id":"t1"}`,
		"other settings":  `{"transaction_id":"t1","settings":{"waiver_bid":35,"seq":2}}`,
		"string value":    `{"transaction_id":"t1","settings":{"expires_at":"soon"}}`,
		"zero":            `{"transaction_id":"t1","settings":{"expires_at":0}}`,
	} {
		var txn Transaction
		if err := json.Unmarshal([]byte(body), &txn); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if _, ok := txn.ExpiresAt(); ok {
			t.Errorf("%s: ExpiresAt reported an expiry from %s", name, body)
		}
	}
}
```

Add `"time"` to the test file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sleeper/ -run TestTransaction_ExpiresAt -v`
Expected: FAIL — `txn.ExpiresAt undefined` (compile error).

- [ ] **Step 3: Add the field and method**

In `internal/sleeper/types.go`, add `"time"` to the imports, and add to the `Transaction` struct after `ConsenterIDs`:

```go
	// Settings is Sleeper's free-form per-transaction map: waiver_bid and seq on
	// a waiver claim, expires_at on a trade offer. Decoded as any because the
	// values are not all one type and a single mistyped key would fail the
	// whole row's decode. Read expires_at through ExpiresAt.
	Settings map[string]any `json:"settings"`
```

and append after the struct:

```go
// ExpiresAt returns settings.expires_at as a UTC time, when present.
//
// Sleeper stores it as epoch SECONDS while Created is epoch MILLIS; reading
// one as the other is off by a factor of a thousand, which is why the unit is
// pinned here and in the test. Absent, non-numeric or zero reads as "no
// expiry": an offer without one never lapses on its own, so the safe default
// is to keep it.
func (t Transaction) ExpiresAt() (time.Time, bool) {
	v, ok := t.Settings["expires_at"]
	if !ok {
		return time.Time{}, false
	}
	f, ok := v.(float64) // encoding/json decodes every JSON number into any as float64
	if !ok || f <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0).UTC(), true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sleeper/ ./internal/dynasty/ ./cmd/ -run 'Transaction|Trade|Football' `
Expected: PASS (the `dynasty` and `cmd` packages build keyed `sleeper.Transaction{}` literals; the new field must not break them).

- [ ] **Step 5: Commit**

```bash
git add internal/sleeper/types.go internal/sleeper/client_test.go
git commit -m "feat(sleeper): Transaction.Settings and ExpiresAt (epoch seconds)"
```

---

### Task 2: `internal/sleeperauth` — the authenticated transport

**Files:**
- Create: `internal/sleeperauth/client.go`
- Test: `internal/sleeperauth/client_test.go`

**Interfaces:**
- Produces: `var Endpoint = "https://sleeper.app/graphql"` (tests override), `const EnvToken = "SLEEPER_TOKEN"`, `var ErrNoToken`, `var ErrUnauthorized`, `func FromEnv() (*Client, error)`, `func New(token string) *Client`, `func (c *Client) Query(ctx context.Context, doc string, vars map[string]any, out any) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/sleeperauth/client_test.go`:

```go
package sleeperauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The token in every test is a made-up string; the point of several of these
// tests is that it never appears anywhere but the authorization header.
const testToken = "tok-abc123-NEVER-LOG-ME"

// serve runs a handler on an httptest server and points Endpoint at it for the
// test's lifetime.
func serve(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := Endpoint
	Endpoint = srv.URL
	t.Cleanup(func() { Endpoint = old })
}

func TestFromEnv_MissingTokenIsErrNoToken(t *testing.T) {
	t.Setenv(EnvToken, "")
	if _, err := FromEnv(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	t.Setenv(EnvToken, testToken)
	if _, err := FromEnv(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestQuery_SendsTokenUserAgentAndBody(t *testing.T) {
	var gotAuth, gotUA, gotCT string
	var gotBody map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotCT = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"data":{"me":{"user_id":"42"}}}`))
	})
	var out struct {
		Me struct {
			UserID string `json:"user_id"`
		} `json:"me"`
	}
	err := New(testToken).Query(context.Background(), `{ me { user_id } }`, map[string]any{"x": 1}, &out)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotAuth != testToken {
		t.Errorf("authorization = %q, want the raw token (no Bearer prefix)", gotAuth)
	}
	if !strings.Contains(gotUA, "rosterbot") {
		t.Errorf("user-agent = %q, want it to name rosterbot", gotUA)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotBody["query"] != `{ me { user_id } }` || gotBody["variables"].(map[string]any)["x"] != float64(1) {
		t.Errorf("body = %v", gotBody)
	}
	if out.Me.UserID != "42" {
		t.Errorf("decoded user_id = %q", out.Me.UserID)
	}
}

func TestQuery_HTTP401IsUnauthorizedAndNeverLeaksTheToken(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token ` + testToken + `"}`)) // a hostile body echoing the token
	})
	err := New(testToken).Query(context.Background(), `{ me { user_id } }`, nil, &struct{}{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text contains the token: %q", err.Error())
	}
}

func TestQuery_GraphQLUnauthorizedCodeIsUnauthorized(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":null},"errors":[{"code":"unauthorized","data":{},"message":"Unauthorized","path":["league_transactions"]}]}`))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error text contains the token: %q", err.Error())
	}
}

func TestQuery_OtherGraphQLErrorsAreJoinedAndNamePaths(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"code":"x","message":"first","path":["a"]},{"code":"y","message":"second","path":["b","c"]}]}`))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if err == nil || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"first", "second", "a", "b.c"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
}

func TestQuery_NonJSONBodyIsAnErrorNotAPanic(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>oops</html>`)) // the server does this for some malformed requests
	})
	if err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{}); err == nil {
		t.Fatal("want a decode error")
	}
}

func TestQuery_Non2xxStatusIsAnErrorWithoutTheBody(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(testToken))
	})
	err := New(testToken).Query(context.Background(), `{ x }`, nil, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/sleeperauth/ -v`
Expected: FAIL — the package does not exist yet / `undefined: Endpoint`.

- [ ] **Step 3: Write the transport**

Create `internal/sleeperauth/client.go`:

```go
// Package sleeperauth is the AUTHENTICATED Sleeper client: the undocumented
// GraphQL API behind the Sleeper app, driven with the operator's own session
// token.
//
// It is a separate package from internal/sleeper (the public, token-free REST
// client) so the token's blast radius is visible in the import graph:
// TestOnlyTheOffersCommandImportsThisPackage pins that no internal/* package
// and no cmd/ file other than football_offers.go imports it. There is
// deliberately no on-disk cache here — the reads exist for freshness, the
// volume is a dozen calls an hour, and a cache would be one more place a
// token-derived key could end up.
//
// v1 is READ-ONLY. There is no Mutate. When writes arrive they take a
// WriteAuthorization value obtainable only through an explicit opt-in (an
// --apply-style flag plus an env switch), mirroring internal/lineuprun's
// applyAuthorization, so a write cannot happen by statement order.
//
// The API is unsupported by Sleeper: the schema can change without notice and
// the token grants full account access for about a year. See
// docs/dynasty-football.md for the operating notes and the reference the
// query shapes were checked against.
package sleeperauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Endpoint is the app's own GraphQL host. A var so tests can point it at an
// httptest server; production never changes it.
var Endpoint = "https://sleeper.app/graphql"

// EnvToken is the env var FromEnv reads. The value is the `token` entry the
// Sleeper web app keeps in localStorage; the operator places it in .env
// locally and in SSM for the deployment. Nothing in this package prints it.
const EnvToken = "SLEEPER_TOKEN"

// userAgent identifies the client honestly. Sleeper answers 403 to a request
// with no User-Agent at all.
const userAgent = "rosterbot (+https://github.com/nixon-commits/rosterbot)"

var (
	// ErrNoToken is returned by FromEnv when SLEEPER_TOKEN is unset, so the
	// one command that needs it fails fast with a plain message.
	ErrNoToken = errors.New("sleeperauth: SLEEPER_TOKEN is not set")
	// ErrUnauthorized covers both HTTP 401 and a GraphQL error carrying
	// code "unauthorized": the token has expired, been revoked, or is wrong.
	// Callers fail the whole run on it — every league would fail the same way.
	ErrUnauthorized = errors.New("sleeperauth: unauthorized (token expired, revoked, or wrong)")
)

// Client posts GraphQL documents with the operator's token.
type Client struct {
	http  *http.Client
	token string
}

// FromEnv builds a client from SLEEPER_TOKEN.
func FromEnv() (*Client, error) {
	tok := strings.TrimSpace(os.Getenv(EnvToken))
	if tok == "" {
		return nil, ErrNoToken
	}
	return New(tok), nil
}

// New builds a client for a token the caller already holds (tests).
func New(token string) *Client {
	return &Client{http: &http.Client{Timeout: 20 * time.Second}, token: token}
}

// gqlError is one entry of a GraphQL errors[] array. Sleeper always sets
// code, message and path; data is ignored.
type gqlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (e gqlError) String() string {
	parts := make([]string, 0, len(e.Path))
	for _, p := range e.Path {
		parts = append(parts, fmt.Sprint(p))
	}
	if len(parts) == 0 {
		return e.Message
	}
	return strings.Join(parts, ".") + ": " + e.Message
}

// Query posts doc (with vars, which may be nil) and decodes the response's
// data into out.
//
// Error mapping: HTTP 401 and any errors[] entry with code "unauthorized"
// become ErrUnauthorized; any other non-2xx status becomes an error naming
// the status only (the body is dropped — Sleeper sometimes answers with an
// HTML page, and a body is the one place a token could be echoed back);
// other GraphQL errors are joined into one error naming each path and
// message. The token is never formatted into anything.
func (c *Client) Query(ctx context.Context, doc string, vars map[string]any, out any) error {
	payload := map[string]any{"query": doc}
	if vars != nil {
		payload["variables"] = vars
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sleeperauth: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sleeperauth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sleeperauth: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("sleeperauth: http %d", resp.StatusCode)
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("sleeperauth: decode response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			if e.Code == "unauthorized" {
				return fmt.Errorf("%w: %s", ErrUnauthorized, e.String())
			}
			msgs = append(msgs, e.String())
		}
		return fmt.Errorf("sleeperauth: graphql: %s", strings.Join(msgs, "; "))
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("sleeperauth: decode data: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sleeperauth/ -v`
Expected: PASS (7 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/sleeperauth/client.go internal/sleeperauth/client_test.go
git commit -m "feat(sleeperauth): authenticated Sleeper GraphQL transport, read-only

Token from SLEEPER_TOKEN only, sent raw in authorization; 401 and code
unauthorized map to ErrUnauthorized; no body and no token ever reaches an
error string; no disk cache; no Mutate."
```

---

### Task 3: `Me`, `ProposedTrades`, and the import-direction test

**Files:**
- Create: `internal/sleeperauth/reads.go`
- Test: `internal/sleeperauth/reads_test.go`, `internal/sleeperauth/depcheck_test.go`

**Interfaces:**
- Consumes: `sleeper.Transaction` (Task 1 fields), `sleeper.TransactionDraftPick{Season string; Round, RosterID, PreviousOwnerID, OwnerID int}` (exists).
- Produces: `func (c *Client) Me(ctx) (string, error)`, `func (c *Client) ProposedTrades(ctx, leagueID string, rosterID int) ([]sleeper.Transaction, error)`, exported query documents `MeQuery`, `ProposedTradesQuery` (so the diag probe and the tests read the same strings).

- [ ] **Step 1: Write the failing tests**

Create `internal/sleeperauth/reads_test.go`:

```go
package sleeperauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMeQuery_SelectsUserIDOnly(t *testing.T) {
	// me can return email, phone and the account's own token. None of them
	// may be requested, because a decoded field is one step from a log line.
	for _, banned := range []string{"token", "email", "phone", "cookies", "real_name"} {
		if strings.Contains(MeQuery, banned) {
			t.Errorf("MeQuery selects %q: %s", banned, MeQuery)
		}
	}
	if !strings.Contains(MeQuery, "user_id") {
		t.Errorf("MeQuery does not select user_id: %s", MeQuery)
	}
}

func TestMe_DecodesUserID(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"me":{"user_id":"738883211463155712"}}}`))
	})
	got, err := New(testToken).Me(context.Background())
	if err != nil || got != "738883211463155712" {
		t.Fatalf("Me = %q, %v", got, err)
	}
}

func TestMe_EmptyUserIDIsAnError(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"me":{"user_id":""}}}`))
	})
	if _, err := New(testToken).Me(context.Background()); err == nil {
		t.Fatal("want an error for an empty user_id")
	}
}

func TestProposedTrades_QueryShapeAndVariables(t *testing.T) {
	var body map[string]any
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[]}}`))
	})
	if _, err := New(testToken).ProposedTrades(context.Background(), "1312135439800356864", 8); err != nil {
		t.Fatal(err)
	}
	doc, _ := body["query"].(string)
	for _, want := range []string{`status: "proposed"`, `type: "trade"`, "roster_id: $roster_id", "league_id: $league_id", "consenter_ids", "creator", "settings", "created", "draft_picks", "waiver_budget"} {
		if !strings.Contains(doc, want) {
			t.Errorf("query lacks %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "leg:") {
		t.Errorf("query passes a leg; the roster_id form needs none:\n%s", doc)
	}
	vars := body["variables"].(map[string]any)
	if vars["league_id"] != "1312135439800356864" || vars["roster_id"] != float64(8) {
		t.Errorf("variables = %v", vars)
	}
}

func TestProposedTrades_DecodesRows(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[{
		  "transaction_id":"1234","type":"trade","status":"proposed","roster_ids":[3,8],
		  "adds":{"5012":8,"9487":3},"drops":{"5012":3,"9487":8},
		  "draft_picks":[{"season":"2027","round":2,"roster_id":3,"previous_owner_id":3,"owner_id":8}],
		  "waiver_budget":[{"sender":3,"receiver":8,"amount":10}],
		  "created":1788371562251,"creator":"111","consenter_ids":[3],
		  "settings":{"expires_at":1788630762}}]}}`))
	})
	rows, err := New(testToken).ProposedTrades(context.Background(), "L", 8)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	r := rows[0]
	if r.TransactionID != "1234" || r.Status != "proposed" || r.Creator != "111" || len(r.ConsenterIDs) != 1 {
		t.Errorf("row = %+v", r)
	}
	if len(r.DraftPicks) != 1 || r.DraftPicks[0].Round != 2 || r.DraftPicks[0].OwnerID != 8 {
		t.Errorf("draft picks = %+v", r.DraftPicks)
	}
	if exp, ok := r.ExpiresAt(); !ok || exp.Unix() != 1788630762 {
		t.Errorf("ExpiresAt = %v, %v", exp, ok)
	}
}

func TestProposedTrades_ToleratesStringFormDraftPicks(t *testing.T) {
	// A community reference reports draft picks arriving as comma-separated
	// strings on some GraphQL transaction reads:
	// "roster_id,season,round,owner_id,previous_owner_id". Until the diag probe
	// settles which form league_transactions uses, accept both.
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[{
		  "transaction_id":"1","type":"trade","status":"proposed","roster_ids":[3,8],
		  "draft_picks":["3,2027,2,8,3"],"created":1,"creator":"111"}]}}`))
	})
	rows, err := New(testToken).ProposedTrades(context.Background(), "L", 8)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	p := rows[0].DraftPicks
	if len(p) != 1 || p[0].RosterID != 3 || p[0].Season != "2027" || p[0].Round != 2 || p[0].OwnerID != 8 || p[0].PreviousOwnerID != 3 {
		t.Errorf("draft picks = %+v", p)
	}
}

func TestProposedTrades_MalformedStringPickIsAnError(t *testing.T) {
	serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"league_transactions":[{"transaction_id":"1","draft_picks":["3,2027"]}]}}`))
	})
	if _, err := New(testToken).ProposedTrades(context.Background(), "L", 8); err == nil {
		t.Fatal("a pick string with the wrong field count must be an error, not a silently dropped asset")
	}
}
```

Create `internal/sleeperauth/depcheck_test.go`:

```go
package sleeperauth

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyTheOffersCommandImportsThisPackage pins the token boundary the
// package exists for: the client that carries a full-access account token is
// reachable from exactly one command file and from no internal/* package, so
// the blast radius of the token is readable from the import graph rather
// than from a code search.
//
// DIRECT imports only, on jobwire's depcheck precedent: transitive reach is
// what the direct rule prevents.
func TestOnlyTheOffersCommandImportsThisPackage(t *testing.T) {
	const module = "github.com/nixon-commits/rosterbot"
	const self = module + "/internal/sleeperauth"

	rootOut, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m: %v\n%s", err, rootOut)
	}
	root := strings.TrimSpace(string(rootOut))

	// No internal/* package may import this one.
	cmd := exec.CommandContext(t.Context(), "go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "./internal/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./internal/...: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, deps, _ := strings.Cut(line, " ")
		if pkg == self {
			continue
		}
		for _, dep := range strings.Fields(deps) {
			if dep == self {
				t.Errorf("%s imports %s — only cmd/football_offers.go may", pkg, self)
			}
		}
	}

	// Under cmd/, only the offers command (and its test) may import it.
	allowed := map[string]bool{"football_offers.go": true, "football_offers_test.go": true}
	files, err := filepath.Glob(filepath.Join(root, "cmd", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), `"`+self+`"`) && !allowed[filepath.Base(f)] {
			t.Errorf("%s imports %s — only football_offers.go may", filepath.Base(f), self)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/sleeperauth/ -run 'TestMe|TestProposedTrades' -v`
Expected: FAIL — `undefined: MeQuery`.

- [ ] **Step 3: Write the reads**

Create `internal/sleeperauth/reads.go`:

```go
package sleeperauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

// MeQuery selects the caller's user_id and NOTHING ELSE. The me object also
// carries email, phone and the account's own token; a decoded field is one
// step from a log line, so they are never requested. TestMeQuery_SelectsUserIDOnly
// pins this.
const MeQuery = `{ me { user_id } }`

// ProposedTradesQuery lists the live trade offers involving one roster.
//
// status "proposed" is the live-offer status (not "pending", which matches
// nothing); type "trade" excludes pending waiver claims, which share the
// status; roster_id filters server-side, and league_transactions needs no
// leg, unlike league_transactions_by_status. The selection is the REST
// transaction's fields plus creator, consenter_ids and settings.
const ProposedTradesQuery = `query($league_id: Snowflake!, $roster_id: Int!) {
  league_transactions(league_id: $league_id, status: "proposed", type: "trade", roster_id: $roster_id) {
    transaction_id type status roster_ids adds drops draft_picks waiver_budget
    created creator consenter_ids settings
  }
}`

// Me returns the Sleeper user id the token belongs to. The offers command
// compares it with SLEEPER_USER_ID before doing anything else, so a token
// from the wrong account is a loud failure rather than a job that runs green
// and never finds an offer.
func (c *Client) Me(ctx context.Context) (string, error) {
	var out struct {
		Me struct {
			UserID string `json:"user_id"`
		} `json:"me"`
	}
	if err := c.Query(ctx, MeQuery, nil, &out); err != nil {
		return "", err
	}
	if out.Me.UserID == "" {
		return "", errors.New("sleeperauth: me returned no user_id")
	}
	return out.Me.UserID, nil
}

// wireTransaction is sleeper.Transaction with draft_picks held raw, because
// the GraphQL API has been observed to return picks either as objects (the
// REST shape) or as "roster_id,season,round,owner_id,previous_owner_id"
// strings. Decoding straight into []TransactionDraftPick would fail the whole
// row on the string form, and a failed row is an offer that never alerts.
type wireTransaction struct {
	sleeper.Transaction
	DraftPicks json.RawMessage `json:"draft_picks"`
}

// ProposedTrades returns every live trade offer in leagueID that involves
// rosterID, in the REST transaction shape.
func (c *Client) ProposedTrades(ctx context.Context, leagueID string, rosterID int) ([]sleeper.Transaction, error) {
	var out struct {
		Rows []wireTransaction `json:"league_transactions"`
	}
	vars := map[string]any{"league_id": leagueID, "roster_id": rosterID}
	if err := c.Query(ctx, ProposedTradesQuery, vars, &out); err != nil {
		return nil, err
	}
	txns := make([]sleeper.Transaction, 0, len(out.Rows))
	for _, w := range out.Rows {
		picks, err := decodeDraftPicks(w.DraftPicks)
		if err != nil {
			return nil, fmt.Errorf("sleeperauth: transaction %s: %w", w.TransactionID, err)
		}
		t := w.Transaction
		t.DraftPicks = picks
		txns = append(txns, t)
	}
	return txns, nil
}

// decodeDraftPicks accepts the object form, the string form, null, or absent.
func decodeDraftPicks(raw json.RawMessage) ([]sleeper.TransactionDraftPick, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var objs []sleeper.TransactionDraftPick
	if err := json.Unmarshal(raw, &objs); err == nil {
		return objs, nil
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err != nil {
		return nil, fmt.Errorf("draft_picks is neither objects nor strings: %s", string(raw))
	}
	picks := make([]sleeper.TransactionDraftPick, 0, len(strs))
	for _, s := range strs {
		f := strings.Split(s, ",")
		if len(f) != 5 {
			return nil, fmt.Errorf("draft pick %q: want 5 comma-separated fields (roster_id,season,round,owner_id,previous_owner_id)", s)
		}
		var p sleeper.TransactionDraftPick
		var err error
		if p.RosterID, err = strconv.Atoi(f[0]); err != nil {
			return nil, fmt.Errorf("draft pick %q: roster_id: %w", s, err)
		}
		p.Season = f[1]
		if p.Round, err = strconv.Atoi(f[2]); err != nil {
			return nil, fmt.Errorf("draft pick %q: round: %w", s, err)
		}
		if p.OwnerID, err = strconv.Atoi(f[3]); err != nil {
			return nil, fmt.Errorf("draft pick %q: owner_id: %w", s, err)
		}
		if p.PreviousOwnerID, err = strconv.Atoi(f[4]); err != nil {
			return nil, fmt.Errorf("draft pick %q: previous_owner_id: %w", s, err)
		}
		picks = append(picks, p)
	}
	return picks, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/sleeperauth/ -v`
Expected: PASS, including `TestOnlyTheOffersCommandImportsThisPackage` (nothing imports the package yet, which satisfies the subset rule).

- [ ] **Step 5: Commit**

```bash
git add internal/sleeperauth/reads.go internal/sleeperauth/reads_test.go internal/sleeperauth/depcheck_test.go
git commit -m "feat(sleeperauth): Me (user_id only) and ProposedTrades; pin the import boundary"
```

---

### Task 4: ⚠ Operator step — the live probe

**Files:**
- Create: `internal/sleeperauth/diag_proposed_test.go` (build tag `diag`; never runs in CI)

**Interfaces:**
- Consumes: `sleeperauth.New`, `Client.Query`, `Client.Me`, `Client.ProposedTrades`, `sleeper.NewClient().Rosters` / `.State`.

This task has two halves: the implementer writes the probe; **the operator runs it** with their token in `.env` and reports the output. The implementer does not run it and never asks for the token.

- [ ] **Step 1: Write the probe**

Create `internal/sleeperauth/diag_proposed_test.go`:

```go
//go:build diag

// Live probe for the offers query. Build-tagged so it never runs in CI: it
// needs the operator's SLEEPER_TOKEN and answers a question only live data
// can — does league_transactions(status:"proposed", roster_id) return every
// offer the Sleeper app shows for the operator's roster, and what shape do
// the rows have? Run by the OPERATOR:
//
//	set -a; source .env; set +a
//	DIAG_LEAGUE_ID=<league with a live offer> go test -tags diag -run TestDiagProposed ./internal/sleeperauth/ -v
//
// It prints ids, counts, statuses and raw draft_picks JSON. It never prints
// the token.
package sleeperauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/sleeper"
)

func TestDiagProposed(t *testing.T) {
	c, err := FromEnv()
	if err != nil {
		t.Skipf("%v — set it in .env and source it", err)
	}
	userID := os.Getenv("SLEEPER_USER_ID")
	leagueID := os.Getenv("DIAG_LEAGUE_ID")
	if userID == "" || leagueID == "" {
		t.Skip("SLEEPER_USER_ID and DIAG_LEAGUE_ID are required")
	}
	ctx := context.Background()

	me, err := c.Me(ctx)
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	fmt.Printf("me.user_id=%s matches SLEEPER_USER_ID=%v\n", me, me == userID)

	pub := sleeper.NewClient()
	rosters, err := pub.Rosters(ctx, leagueID)
	if err != nil {
		t.Fatalf("rosters: %v", err)
	}
	myRoster := 0
	for _, r := range rosters {
		if r.OwnerID == userID {
			myRoster = r.RosterID
		}
	}
	fmt.Printf("league %s: my roster_id=%d (0 = none found)\n", leagueID, myRoster)

	// 1. The query the job will use.
	mine, err := c.ProposedTrades(ctx, leagueID, myRoster)
	if err != nil {
		t.Fatalf("ProposedTrades: %v", err)
	}
	fmt.Printf("league_transactions(status proposed, type trade, roster_id=%d): %d row(s)\n", myRoster, len(mine))
	for _, x := range mine {
		exp, ok := x.ExpiresAt()
		fmt.Printf("  id=%s status=%s roster_ids=%v creator=%s consenters=%v created=%d expires=%v(%v) picks=%d faab=%d\n",
			x.TransactionID, x.Status, x.RosterIDs, x.Creator, x.ConsenterIDs, x.Created, exp, ok, len(x.DraftPicks), len(x.WaiverBudget))
	}

	// 2. The same query without roster_id, RAW, to see every proposed trade in
	//    the league and the raw draft_picks shape.
	var raw struct {
		Rows []map[string]json.RawMessage `json:"league_transactions"`
	}
	const all = `query($league_id: Snowflake!) { league_transactions(league_id: $league_id, status: "proposed", type: "trade") { transaction_id roster_ids draft_picks settings status } }`
	if err := c.Query(ctx, all, map[string]any{"league_id": leagueID}, &raw); err != nil {
		t.Fatalf("league-wide proposed: %v", err)
	}
	fmt.Printf("league_transactions(status proposed, type trade, no roster_id): %d row(s)\n", len(raw.Rows))
	for _, r := range raw.Rows {
		fmt.Printf("  id=%s roster_ids=%s draft_picks=%s settings=%s\n", r["transaction_id"], r["roster_ids"], r["draft_picks"], r["settings"])
	}

	// 3. The leg-scoped alternative for the current week, for comparison.
	st, err := pub.State(ctx)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	var byStatus struct {
		Rows []struct {
			TransactionID string `json:"transaction_id"`
			RosterIDs     []int  `json:"roster_ids"`
		} `json:"league_transactions_by_status"`
	}
	const legQ = `query($league_id: Snowflake!, $leg: Int!) { league_transactions_by_status(league_id: $league_id, status: "proposed", leg: $leg) { transaction_id roster_ids } }`
	if err := c.Query(ctx, legQ, map[string]any{"league_id": leagueID, "leg": st.Week}, &byStatus); err != nil {
		t.Fatalf("by_status: %v", err)
	}
	fmt.Printf("league_transactions_by_status(status proposed, leg=%d): %d row(s)\n", st.Week, len(byStatus.Rows))
	fmt.Println("COMPARE the roster_id count with what the Sleeper app shows under your league's Trades tab (offers to you + offers from you).")
}
```

- [ ] **Step 2: Compile-check with the tag, without running it**

Run: `go vet -tags diag ./internal/sleeperauth/`
Expected: clean. (Without a token the test skips; do not source anyone's `.env`.)

- [ ] **Step 3: Commit**

```bash
git add internal/sleeperauth/diag_proposed_test.go
git commit -m "test(sleeperauth): diag-tagged live probe for the proposed-offer query"
```

- [ ] **Step 4: ⚠ STOP — operator runs the probe and reports**

The operator runs the command in the file's header against a league that currently has at least one live offer (creating a throwaway offer to themselves from a second league mate is not possible; a real pending offer, or one the operator asks a league mate to send, is needed). Record the answer in the plan's ledger:

- If the `roster_id` count equals what the app shows → keep `ProposedTradesQuery` as written (default).
- If it is lower and the `no roster_id` or `by_status` count is right → change `ProposedTradesQuery` to the form that was right and add a test for it; the fallback the spec names is `league_transactions_by_status(status: "proposed", leg)` over the current and previous week.
- If `draft_picks` printed as strings → `decodeDraftPicks`' string form is the live one; keep both.

If the operator cannot run it before Task 5 starts, continue with the default and leave the ledger note open; the probe stays in the tree for whenever an offer exists.

---

### Task 5: `FootballOffers` markers — layout and statestore

**Files:**
- Modify: `internal/statestore/layout/layout.go` (after `FootballTradeLog`)
- Modify: `internal/statestore/layout/layout_test.go:359` (the prefix-collision list), `internal/statestore/tenant_test.go:121` (the all-artifacts list)
- Modify: `internal/statestore/statestore.go` (artifact var + constructor)
- Test: `internal/statestore/layout/layout_test.go`

**Interfaces:**
- Produces: `layout.FootballOffers`, `func (s *Selector) FootballOfferMarkers() (lineupapi.BlobStore, error)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/statestore/layout/layout_test.go`:

```go
func TestFootballOffers_IsAMarkerFamilyLikeFootballTrades(t *testing.T) {
	a := FootballOffers
	if a.S3Prefix != "football/offers/" || a.LocalDir != ".football/offers" {
		t.Errorf("prefix/dir = %q/%q", a.S3Prefix, a.LocalDir)
	}
	if !a.Durable || a.MaxAge != 0 || a.PerTenant || a.Partitioned || a.NoBackfill {
		t.Errorf("flags: %+v — markers are durable, ageless, deployment-wide, flat", a)
	}
	for _, x := range All() {
		if x.Name == a.Name {
			t.Errorf("FootballOffers must be absent from All(): a quiet league writes no markers for weeks and an age check would read that as stale")
		}
	}
	// A sibling of football/trades/, not nested under it: neither the trade
	// markers nor the trade log may see an offer marker, and vice versa.
	if strings.HasPrefix(a.S3Prefix, FootballTrades.S3Prefix) || strings.HasPrefix(FootballTrades.S3Prefix, a.S3Prefix) {
		t.Errorf("FootballOffers %q nests with FootballTrades %q", a.S3Prefix, FootballTrades.S3Prefix)
	}
}
```

Also add `FootballOffers` to the `[]Artifact{ILStarts, GSFloorAlerts, StaleCacheAlerts, FootballTrades, Notification}` list at `layout_test.go:359`, and to the `all := append(layout.All(), layout.Progress, layout.FootballTrades, layout.FootballTradeLog)` list in `internal/statestore/tenant_test.go:121` (it is not PerTenant, so `wantPerTenant` needs no entry).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/statestore/... -run 'TestFootballOffers|TestOpsAlert|PerTenant' -v`
Expected: FAIL — `undefined: FootballOffers`.

- [ ] **Step 3: Add the artifact and constructor**

In `internal/statestore/layout/layout.go`, directly after the `FootballTradeLog` declaration:

```go
	// FootballOffers holds one dedup marker per Sleeper trade OFFER the
	// operator has been alerted about (football-offers, Plan 2). Same shape
	// and reasoning as FootballTrades: keyed on the global transaction_id,
	// written check -> send -> mark, no MaxAge and absent from All() because a
	// league with no offers writes nothing for weeks. A sibling prefix, not a
	// child of football/trades/: an offer and the completed trade it becomes
	// share a transaction_id, and one namespace would let the offer's marker
	// silence the completed-trade alert (or the reverse).
	FootballOffers = Artifact{Name: "Football Offer Markers", S3Prefix: "football/offers/", LocalDir: ".football/offers", Durable: true, Producer: "FootballOffers"}
```

In `internal/statestore/statestore.go`, add `footballOffersArtifact = of(layout.FootballOffers)` beside `footballTradesArtifact`, and after `FootballTradeMarkers`:

```go
// FootballOfferMarkers is one dedup marker object per Sleeper trade offer
// alerted by football-offers -- S3 under football/offers/ when STATE_BUCKET is
// set, else .football/offers/. Same BlobStore machinery as the trade markers;
// a separate prefix because an offer and the trade it becomes share a
// transaction_id and must not silence each other.
func (s *Selector) FootballOfferMarkers() (lineupapi.BlobStore, error) {
	return blobStore(s, footballOffersArtifact, "")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/statestore/... `
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/statestore/layout/layout.go internal/statestore/layout/layout_test.go internal/statestore/tenant_test.go internal/statestore/statestore.go
git commit -m "feat(layout): FootballOffers marker family, sibling of football/trades/"
```

---

### Task 6: the `football-offers` command

**Files:**
- Create: `cmd/football_offers.go`
- Test: `cmd/football_offers_test.go`
- Modify: `cmd/season_gate.go` (`seasonPolicies` gains `"football-offers": {}` beside `"football-trades": {}`)

**Interfaces:**
- Consumes: `initFootball()`, `cfg.requireSleeperUserID()`, `cfg.SleeperUserID`, `discoverLeagues(ctx, sc, cfg, season, out)`, `leagueContext{League, Profile}` (Plan 1); `sleeperauth.FromEnv/Me/ProposedTrades/ErrUnauthorized`; `statestore.FromEnv().FootballOfferMarkers()`; `alertmarker.New/Sent/Send`; `dynasty.TeamNames`, `dynasty.BuildTradeSides(txn, players, bundle, names, format) []TradeSide`, `dynasty.GradeTrade(sides) TradeVerdict` (`Status`, `FavoredTeamID` = roster id as a string, `FavoredTeamName`, `Pct`, `UnpricedAssets`), `dynasty.TradeVerdictSummary`; `notify.Send`; `pushover.Truncate`; package-level `dryRun`, `warn`, `cacheDir`, `cacheTTL`.
- Produces: `selectOffers(txns []sleeper.Transaction, myRoster int, myUserID string, now time.Time) (keep []sleeper.Transaction, expired, answered int)`, `formatOfferAlert(league dynasty.LeagueProfile, myRoster int, txn sleeper.Transaction, sides []dynasty.TradeSide, v dynasty.TradeVerdict) (title, body string)`, `gradeAndAlertOffers(ctx, in offerRunInputs) offerRunResult`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/football_offers_test.go`:

```go
package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
)

var offerNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// offer builds a proposed trade to roster 8 (the operator) from roster 3.
func offer(id string, mutate func(*sleeper.Transaction)) sleeper.Transaction {
	t := sleeper.Transaction{
		TransactionID: id, Type: "trade", Status: "proposed", RosterIDs: []int{3, 8},
		Adds: map[string]int{"4984": 8, "9509": 3}, Drops: map[string]int{"4984": 3, "9509": 8},
		Created: offerNow.Add(-2 * time.Hour).UnixMilli(), Creator: "user-3", ConsenterIDs: []int{3},
	}
	if mutate != nil {
		mutate(&t)
	}
	return t
}

func TestSelectOffers_KeepsOnlyLiveOffersToMeAwaitingMe(t *testing.T) {
	in := []sleeper.Transaction{
		offer("keep", nil),
		offer("mine", func(x *sleeper.Transaction) { x.Creator = "me" }),                 // I sent it (C, deferred)
		offer("waiver", func(x *sleeper.Transaction) { x.Type = "waiver" }),               // a pending claim
		offer("done", func(x *sleeper.Transaction) { x.Status = "complete" }),             // not live
		offer("notme", func(x *sleeper.Transaction) { x.RosterIDs = []int{3, 5} }),        // not my roster
		offer("answered", func(x *sleeper.Transaction) { x.ConsenterIDs = []int{3, 8} }), // I already accepted
		offer("expired", func(x *sleeper.Transaction) {
			x.Settings = map[string]any{"expires_at": float64(offerNow.Add(-time.Minute).Unix())}
		}),
		offer("fresh", func(x *sleeper.Transaction) {
			x.Settings = map[string]any{"expires_at": float64(offerNow.Add(time.Hour).Unix())}
		}),
	}
	keep, expired, answered := selectOffers(in, 8, "me", offerNow)
	var ids []string
	for _, k := range keep {
		ids = append(ids, k.TransactionID)
	}
	if strings.Join(ids, ",") != "keep,fresh" {
		t.Errorf("kept %v, want keep,fresh", ids)
	}
	if expired != 1 || answered != 1 {
		t.Errorf("expired=%d answered=%d, want 1 and 1", expired, answered)
	}
}

func offerFixture(t *testing.T) (offerRunInputs, *[]string) {
	t.Helper()
	sent := &[]string{}
	return offerRunInputs{
		markers:  lineupapi.NewFileBlobStore(t.TempDir(), ""),
		now:      offerNow,
		offers:   []sleeper.Transaction{offer("o1", nil)},
		myRoster: 8,
		players: map[string]sleeper.Player{
			"4984": {PlayerID: "4984", FirstName: "Josh", LastName: "Allen"},
			"9509": {PlayerID: "9509", FirstName: "Bijan", LastName: "Robinson"},
		},
		bundle: &statsguy.Bundle{Players: map[string]statsguy.Player{
			"4984": {ID: "4984", Value: statsguy.FormatValues{SFDynasty: 9000}},
			"9509": {ID: "9509", Value: statsguy.FormatValues{SFDynasty: 11000}},
		}},
		names:  map[int]string{3: "CeeDee Top", 8: "Tha Mobb"},
		league: dynasty.LeagueProfile{LeagueID: "L1", Name: "Palm Trees", Format: "sf_dynasty"},
		send:   func(title, body string) error { *sent = append(*sent, title+"\n"+body); return nil },
		out:    io.Discard,
	}, sent
}

func TestGradeAndAlertOffers_AlertsFromMySideAndMarks(t *testing.T) {
	in, sent := offerFixture(t)
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Graded != 1 || res.Alerted != 1 || len(*sent) != 1 {
		t.Fatalf("res=%+v sent=%d", res, len(*sent))
	}
	msg := (*sent)[0]
	// I receive Allen (9000) and give Robinson (11000): the offer favors THEM.
	if !strings.HasPrefix(msg, "[Palm Trees] Offer from CeeDee Top: favors CeeDee Top") {
		t.Errorf("title = %q", strings.SplitN(msg, "\n", 2)[0])
	}
	if !strings.Contains(msg, "You get: Josh Allen (9000)") || !strings.Contains(msg, "You give: Bijan Robinson (11000)") {
		t.Errorf("body = %q", msg)
	}
	if !strings.Contains(msg, "waiting on you") {
		t.Errorf("body lacks the consent state: %q", msg)
	}
	if _, found, err := in.markers.Get(context.Background(), "o1"); err != nil || !found {
		t.Errorf("marker not written: found=%v err=%v", found, err)
	}
}

func TestGradeAndAlertOffers_FavorsYouWhenIComeOutAhead(t *testing.T) {
	in, sent := offerFixture(t)
	in.offers[0].Adds = map[string]int{"9509": 8, "4984": 3} // now I get Robinson
	in.offers[0].Drops = map[string]int{"9509": 3, "4984": 8}
	gradeAndAlertOffers(context.Background(), in)
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "favors you (+") {
		t.Errorf("sent = %v", *sent)
	}
}

func TestGradeAndAlertOffers_ExpiryIsNamed(t *testing.T) {
	in, sent := offerFixture(t)
	in.offers[0].Settings = map[string]any{"expires_at": float64(offerNow.Add(26 * time.Hour).Unix())}
	gradeAndAlertOffers(context.Background(), in)
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "expires 2026-10-01 14:00 UTC") {
		t.Errorf("sent = %v", *sent)
	}
}

func TestGradeAndAlertOffers_DryRunSendsAndMarksNothing(t *testing.T) {
	in, sent := offerFixture(t)
	in.dryRun = true
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Graded != 1 || res.Alerted != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
	if _, found, _ := in.markers.Get(context.Background(), "o1"); found {
		t.Error("dry-run wrote a marker")
	}
}

func TestGradeAndAlertOffers_FailedSendIsNotMarked(t *testing.T) {
	in, _ := offerFixture(t)
	in.send = func(string, string) error { return errors.New("apns down") }
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Alerted != 0 {
		t.Errorf("res=%+v", res)
	}
	if _, found, _ := in.markers.Get(context.Background(), "o1"); found {
		t.Error("a failed send must not mark: the next poll has to retry")
	}
}

func TestGradeAndAlertOffers_MarkedOfferIsSkippedBeforeGrading(t *testing.T) {
	in, sent := offerFixture(t)
	if err := in.markers.Publish("o1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	res := gradeAndAlertOffers(context.Background(), in)
	if res.Skipped != 1 || res.Graded != 0 || len(*sent) != 0 {
		t.Errorf("res=%+v sent=%d", res, len(*sent))
	}
}

func TestGradeAndAlertOffers_NilMarkersStillSends(t *testing.T) {
	in, sent := offerFixture(t)
	in.markers = nil
	gradeAndAlertOffers(context.Background(), in)
	if len(*sent) != 1 {
		t.Errorf("sent = %d", len(*sent))
	}
}

func TestFormatOfferAlert_ThreeTeamOfferNamesEachGiver(t *testing.T) {
	in, _ := offerFixture(t)
	txn := offer("o3", func(x *sleeper.Transaction) {
		x.RosterIDs = []int{3, 5, 8}
		x.Adds = map[string]int{"4984": 8, "9509": 5}
		x.Drops = map[string]int{"4984": 3, "9509": 8}
	})
	in.names[5] = "Ghost Riders"
	sides := dynasty.BuildTradeSides(txn, in.players, in.bundle, in.names, in.league.Format)
	title, body := formatOfferAlert(in.league, 8, txn, sides, dynasty.GradeTrade(sides))
	if !strings.HasPrefix(title, "[Palm Trees] Offer (3 teams)") {
		t.Errorf("title = %q", title)
	}
	if !strings.Contains(body, "Ghost Riders gets: Bijan Robinson") {
		t.Errorf("body = %q", body)
	}
}
```

`lineupapi.BlobStore.Publish(key, body)` takes no context and `Get(ctx, key)` does — the same calls `cmd/football_trades_log_test.go` and `relogRows` make.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/ -run 'SelectOffers|GradeAndAlertOffers|FormatOfferAlert' -v`
Expected: FAIL — `undefined: selectOffers`.

- [ ] **Step 3: Write the command**

Create `cmd/football_offers.go`:

```go
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/pushover"
	"github.com/nixon-commits/rosterbot/internal/sleeper"
	"github.com/nixon-commits/rosterbot/internal/sleeperauth"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statsguy"
	"github.com/spf13/cobra"
)

var footballOffersCmd = &cobra.Command{
	Use:   "football-offers",
	Short: "Grade every Sleeper trade offer made to you before you respond",
	Long: `Reads the live trade offers proposed TO the operator's roster in every
non-complete NFL league SLEEPER_USER_ID belongs to, through Sleeper's
undocumented GraphQL API with the operator's own session token
(SLEEPER_TOKEN -- the only job that receives it), grades each with the same
StatsGuy sum football-trades uses, and pushes one alert per offer written
from the operator's side: what you get, what you give, the verdict, who has
already accepted, and when it expires.

Read-only: nothing is accepted, rejected or countered. Idempotent via one
dedup marker per transaction_id under football/offers/ (check -> send ->
mark); --dry-run sends and marks nothing. The run fails outright when the
token is rejected -- every league would fail the same way -- and ops
alerting escalates on the third consecutive failure.`,
	RunE: runFootballOffers,
}

func init() { rootCmd.AddCommand(footballOffersCmd) }

func runFootballOffers(cmd *cobra.Command, args []string) error {
	cfg, sc, err := initFootball()
	if err != nil {
		return err
	}
	if err := cfg.requireSleeperUserID(); err != nil {
		return err
	}
	auth, err := sleeperauth.FromEnv()
	if err != nil {
		return err
	}
	ctx := context.Background()

	// Identity FIRST. A token pasted from the wrong account, or a wrong user
	// id, would otherwise run green forever and never find an offer.
	uid, err := auth.Me(ctx)
	if err != nil {
		return fmt.Errorf("sleeper identity check: %w", err)
	}
	if uid != cfg.SleeperUserID {
		return fmt.Errorf("SLEEPER_TOKEN belongs to Sleeper user %s but SLEEPER_USER_ID is %s: the token is from another account, or the user id is wrong", uid, cfg.SleeperUserID)
	}
	fmt.Printf("football-offers: token verified for user %s\n", uid)

	state, err := sc.State(ctx)
	if err != nil {
		return fmt.Errorf("sleeper state: %w", err)
	}
	leagues, err := discoverLeagues(ctx, sc, cfg, state.Season, os.Stdout)
	if err != nil {
		return err
	}
	players, err := sc.PlayersNFL(ctx)
	if err != nil {
		return fmt.Errorf("sleeper players: %w", err)
	}
	bundle, err := statsguy.LoadBundle(ctx, cacheDir, cacheTTL(statsguy.CacheTTL))
	if err != nil {
		return fmt.Errorf("statsguy bundle: %w", err)
	}
	// Soft: a marker store we cannot build disables dedup, not the alert --
	// the same policy as football-trades.
	markers, err := statestore.FromEnv().FootballOfferMarkers()
	if err != nil {
		warn("football-offers: init markers: %v (alerts will repeat until resolved)", err)
		markers = nil
	}
	now := time.Now().UTC()

	var (
		failed []string
		total  offerRunResult
		found  int
	)
	for _, lc := range leagues {
		li, err := loadLeagueOffers(ctx, sc, auth, cfg.SleeperUserID, lc.League.LeagueID)
		if err != nil {
			if errors.Is(err, sleeperauth.ErrUnauthorized) {
				// Every league would fail the same way; fail the run so ops
				// alerting sees a real outage rather than six warnings.
				return fmt.Errorf("football-offers: %s: %w", lc.League.Name, err)
			}
			warn("football-offers: %s: %v (continuing with the other leagues)", lc.League.Name, err)
			failed = append(failed, lc.League.Name)
			continue
		}
		if li.myRoster == 0 {
			fmt.Printf("football-offers: %s: no roster owned by %s; skipped\n", lc.League.Name, cfg.SleeperUserID)
			continue
		}
		keep, expired, answered := selectOffers(li.offers, li.myRoster, cfg.SleeperUserID, now)
		found += len(li.offers)
		total.Expired += expired
		total.Answered += answered
		// Unconditional coverage line, zero case included: a league with no
		// offers and a query that returns nothing look identical without it.
		fmt.Printf("football-offers: %s: %d proposed for roster %d, %d to me and live\n", lc.League.Name, len(li.offers), li.myRoster, len(keep))

		res := gradeAndAlertOffers(ctx, offerRunInputs{
			markers:  markers,
			now:      now,
			offers:   keep,
			myRoster: li.myRoster,
			players:  players,
			bundle:   bundle,
			names:    li.names,
			league:   lc.Profile,
			dryRun:   dryRun,
			send:     func(title, body string) error { return sendFootballOfferAlert(ctx, title, body) },
			out:      os.Stdout,
		})
		total.Graded += res.Graded
		total.Alerted += res.Alerted
		total.Skipped += res.Skipped
	}

	fmt.Printf("football-offers: %d league(s), %d proposed, %d already alerted, %d expired, %d already answered, %d graded, %d sent\n",
		len(leagues), found, total.Skipped, total.Expired, total.Answered, total.Graded, total.Alerted)
	if len(failed) > 0 {
		return fmt.Errorf("football-offers: %d of %d league(s) failed: %s", len(failed), len(leagues), strings.Join(failed, ", "))
	}
	return nil
}

// leagueOffers is one league's inputs: the operator's roster id there (0 when
// they own none), the proposed trades involving it, and the team names.
type leagueOffers struct {
	myRoster int
	offers   []sleeper.Transaction
	names    map[int]string
}

func loadLeagueOffers(ctx context.Context, sc *sleeper.Client, auth *sleeperauth.Client, userID, leagueID string) (leagueOffers, error) {
	rosters, err := sc.Rosters(ctx, leagueID)
	if err != nil {
		return leagueOffers{}, fmt.Errorf("sleeper rosters: %w", err)
	}
	users, err := sc.Users(ctx, leagueID)
	if err != nil {
		return leagueOffers{}, fmt.Errorf("sleeper users: %w", err)
	}
	li := leagueOffers{names: dynasty.TeamNames(rosters, users)}
	for _, r := range rosters {
		if r.OwnerID == userID {
			li.myRoster = r.RosterID
		}
	}
	if li.myRoster == 0 {
		return li, nil
	}
	li.offers, err = auth.ProposedTrades(ctx, leagueID, li.myRoster)
	if err != nil {
		return leagueOffers{}, fmt.Errorf("proposed trades: %w", err)
	}
	return li, nil
}

// selectOffers keeps the offers worth alerting: live trades proposed by
// someone else that involve my roster and still await my consent. The two
// dropped classes worth counting are returned so the summary can say why a
// proposed offer produced no alert.
func selectOffers(txns []sleeper.Transaction, myRoster int, myUserID string, now time.Time) (keep []sleeper.Transaction, expired, answered int) {
	for _, t := range txns {
		if t.Type != "trade" || t.Status != "proposed" || t.Creator == myUserID || !containsInt(t.RosterIDs, myRoster) {
			continue
		}
		if exp, ok := t.ExpiresAt(); ok && !exp.After(now) {
			expired++
			continue
		}
		if containsInt(t.ConsenterIDs, myRoster) {
			answered++ // I already accepted; it is waiting on someone else
			continue
		}
		keep = append(keep, t)
	}
	return keep, expired, answered
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// offerRunInputs is everything gradeAndAlertOffers needs for ONE league, with
// sending and printing injected -- the same seam tradeRunInputs provides for
// football-trades, so the check -> send -> mark rule is asserted, not read.
type offerRunInputs struct {
	markers  lineupapi.BlobStore
	now      time.Time
	offers   []sleeper.Transaction
	myRoster int
	players  map[string]sleeper.Player
	bundle   *statsguy.Bundle
	names    map[int]string
	league   dynasty.LeagueProfile
	dryRun   bool
	send     func(title, body string) error
	out      io.Writer
}

// offerRunResult is what one league's poll decided.
type offerRunResult struct {
	Graded, Alerted, Skipped, Expired, Answered int
}

// gradeAndAlertOffers runs check -> send -> mark over one league's live
// offers. Nothing is logged durably: the activity feed record notify.Send
// writes first is the durable trace, and an offer's outcome (accepted,
// rejected, countered) is not this job's to record -- an accepted one
// surfaces through football-trades as a completed trade.
func gradeAndAlertOffers(ctx context.Context, in offerRunInputs) offerRunResult {
	var res offerRunResult
	m := alertmarker.New(in.markers, alertmarker.WithLogf(func(format string, args ...any) {
		warn("football-offers: "+format, args...)
	}))
	for _, txn := range in.offers {
		if m.Sent(ctx, txn.TransactionID) {
			res.Skipped++
			continue
		}
		sides := dynasty.BuildTradeSides(txn, in.players, in.bundle, in.names, in.league.Format)
		verdict := dynasty.GradeTrade(sides)
		res.Graded++

		title, body := formatOfferAlert(in.league, in.myRoster, txn, sides, verdict)
		fmt.Fprintln(in.out, title)
		fmt.Fprintln(in.out, body)
		if in.dryRun {
			continue
		}
		// check -> send -> mark (rosterbot-chs): a failed send marks nothing
		// and retries next poll; a marker-write failure after a successful
		// send degrades to a duplicate, never silence.
		if err := m.Send(txn.TransactionID, []byte(dynasty.TradeVerdictSummary(verdict)), func() error {
			return in.send(title, body)
		}); err != nil {
			warn("football-offers: send failed for %s: %v", txn.TransactionID, err)
			continue
		}
		res.Alerted++
	}
	return res
}

// formatOfferAlert renders one offer from the operator's side.
//
// Title: "[League] Offer from <proposer>: favors you (+N%)" -- or favors
// them, dead even, or the two no-verdict reasons formatTradeAlert names.
// Body: "You get: ... | You give: ... | <column> | expires ... | waiting on
// you". A three-team offer names each giving side instead of "You give".
func formatOfferAlert(league dynasty.LeagueProfile, myRoster int, txn sleeper.Transaction, sides []dynasty.TradeSide, v dynasty.TradeVerdict) (title, body string) {
	me := strconv.Itoa(myRoster)
	var mine *dynasty.TradeSide
	var others []dynasty.TradeSide
	for i := range sides {
		if sides[i].TeamID == me {
			mine = &sides[i]
		} else {
			others = append(others, sides[i])
		}
	}

	from := "Offer"
	switch {
	case len(others) == 1:
		from = "Offer from " + others[0].TeamName
	case len(others) > 1:
		from = fmt.Sprintf("Offer (%d teams)", len(sides))
	}

	var verdict string
	switch {
	case v.Status == dynasty.TradeFavors && v.FavoredTeamID == me:
		verdict = fmt.Sprintf("favors you (+%.0f%%)", v.Pct)
	case v.Status == dynasty.TradeFavors:
		verdict = fmt.Sprintf("favors %s (+%.0f%%)", v.FavoredTeamName, v.Pct)
	case v.Status == dynasty.TradeDeadEven:
		verdict = "dead even"
	case v.UnpricedAssets > 0:
		verdict = "too many unpriced assets to grade"
	default:
		verdict = "nothing to compare"
	}
	title = fmt.Sprintf("[%s] %s: %s", league.Name, from, verdict)

	var parts []string
	if mine != nil {
		parts = append(parts, "You get: "+assetList(mine.Assets))
	}
	if len(others) == 1 {
		parts = append(parts, "You give: "+assetList(others[0].Assets))
	} else {
		for _, o := range others {
			parts = append(parts, o.TeamName+" gets: "+assetList(o.Assets))
		}
	}
	parts = append(parts, league.Format)
	if exp, ok := txn.ExpiresAt(); ok {
		parts = append(parts, "expires "+exp.Format("2006-01-02 15:04 UTC"))
	}
	accepted := len(txn.ConsenterIDs)
	if accepted > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d accepted, waiting on you", accepted, len(txn.RosterIDs)))
	} else {
		parts = append(parts, "waiting on you")
	}
	return title, pushover.Truncate(strings.Join(parts, " | "))
}

func assetList(assets []dynasty.TradeAsset) string {
	if len(assets) == 0 {
		return "nothing"
	}
	names := make([]string, 0, len(assets))
	for _, a := range assets {
		if a.Priced {
			names = append(names, fmt.Sprintf("%s (%d)", a.Name, a.Value))
		} else {
			names = append(names, a.Name+" (unpriced)")
		}
	}
	return strings.Join(names, ", ")
}

// sendFootballOfferAlert routes through the notify dispatcher with the same
// kind football-trades uses; the title's "Offer" is what tells them apart. A
// dedicated kind would be an iOS-visible change and is not bundled here.
func sendFootballOfferAlert(ctx context.Context, title, body string) error {
	return notify.Send(ctx, notify.Event{Kind: "transactions", Title: title, Message: body})
}
```

Then in `cmd/season_gate.go`, add `"football-offers": {},` directly after `"football-trades": {},`.

- [ ] **Step 4: Run tests, lint, tidy**

Run: `go test ./cmd/ -run 'SelectOffers|GradeAndAlertOffers|FormatOfferAlert|SeasonGate|Football' -v && go test ./internal/sleeperauth/ && make lint && go mod tidy && git diff --stat go.mod go.sum`
Expected: PASS; `TestOnlyTheOffersCommandImportsThisPackage` still passes (only `football_offers.go` and its test import the package); lint clean; go.mod/go.sum unchanged.

If `TestGradeAndAlertOffers_AlertsFromMySideAndMarks` fails on the exact title because `BuildTradeSides` orders sides differently, fix the test's expectation only after reading `BuildTradeSidesAll`'s side ordering — never by changing the grader.

- [ ] **Step 5: Live dry-run (operator's `.env` carries `SLEEPER_TOKEN`, `SLEEPER_USER_ID`, `SLEEPER_LEAGUE_ID`)**

The implementer cannot run this (no token). The operator runs, from the worktree with the vars exported:

```bash
go run . football-offers --dry-run 2>&1 | tail -30
```

Expected: `football-offers: token verified for user 738883211463155712`, six league profile lines, one coverage line per league (`N proposed for roster R, M to me and live`), any live offer printed as title + body, and a summary line; exit 0.

- [ ] **Step 6: Commit**

```bash
git add cmd/football_offers.go cmd/football_offers_test.go cmd/season_gate.go
git commit -m "feat(football-offers): grade every proposed trade offer to the operator, read-only

Identity check first, roster-scoped proposed-trade reads per league, offers
selected by live/to-me/awaiting-me, graded with the football-trades grader,
alerted from the operator's side with expiry and consent state, one marker
per transaction id under football/offers/, check -> send -> mark."
```

---

### Task 7: Infra — the offers task definition and hourly schedule; Makefile gate

**Files:**
- Modify: `infra/infra.go` (container options variable; new `OffersTask`; `job.taskDef`; the jobs table; the schedule loop; a new gap constant)
- Modify: `Makefile` (`run-all`)

**Interfaces:**
- Consumes: `taskDef`, `botContainer`, `secret(name)`, the `job` struct and `jobs` table, `hourlyGap`/`sixHourlyGap` constants.
- Produces: task definition construct id `OffersTask`; schedule id `FootballOffers` with command `football-offers`; `JOB_SCHEDULES` gains it automatically.

- [ ] **Step 1: ⚠ Operator pre-step, BEFORE this lands on main**

The operator creates the SecureString `/rosterbot/SLEEPER_TOKEN` in us-west-1 with their Sleeper session token (the value of the `token` key under the Sleeper web app's local storage, DevTools → Application → Local Storage → `sleeper.com`). The implementer never asks for, sees, or handles the value. A missing parameter fails the OffersTask launch at provisioning — and, because both task definitions share one execution role, must exist before the deploy the same way `SLEEPER_USER_ID` had to.

- [ ] **Step 2: Add the task definition**

In `infra/infra.go`:

(a) Change `botContainer := taskDef.AddContainer(jsii.String("bot"), &awsecs.ContainerDefinitionOptions{` to build the options as a variable first, so a second container cannot drift from the first:

```go
	botOpts := &awsecs.ContainerDefinitionOptions{
		// ... the existing literal, unchanged ...
	}
	botContainer := taskDef.AddContainer(jsii.String("bot"), botOpts)
```

(b) Directly after that line (the execution role exists only once a container with an ECR image and secrets has been added, so this must come after, not before):

```go
	// --- football-offers: the one task that carries the operator's Sleeper
	// session token (rosterbot spec 2026-09-22, Section 1) ---
	//
	// A SECOND task definition rather than one more entry in botOpts.Secrets:
	// the token grants full access to the operator's Sleeper account for about
	// a year, and every other job -- the hourly lineup job, the API-launched
	// ones, the per-tenant fan-out -- would otherwise carry it in its
	// environment for no reason. Same image, same roles, same environment and
	// the same secrets plus one; the container options are copied from
	// botOpts so the two cannot drift.
	//
	// Shares TaskRole and ExecutionRole with the main task on purpose: the S3
	// and SSM grants above apply unchanged, and CDK adds the read on the new
	// parameter to the shared execution role when the container is added.
	//
	// The one thing botContainer receives that this container does not is
	// the DASHBOARD_CF_DIST_ID_PARAM environment added later for
	// projection-site, which this job never runs.
	offersSecrets := map[string]awsecs.Secret{}
	for k, v := range *botOpts.Secrets {
		offersSecrets[k] = v
	}
	offersSecrets["SLEEPER_TOKEN"] = secret("SLEEPER_TOKEN")
	offersOpts := *botOpts
	offersOpts.Secrets = &offersSecrets
	offersTaskDef := awsecs.NewFargateTaskDefinition(stack, jsii.String("OffersTask"), &awsecs.FargateTaskDefinitionProps{
		Cpu:            jsii.Number(1024),
		MemoryLimitMiB: jsii.Number(2048),
		RuntimePlatform: &awsecs.RuntimePlatform{
			CpuArchitecture:       awsecs.CpuArchitecture_ARM64(),
			OperatingSystemFamily: awsecs.OperatingSystemFamily_LINUX(),
		},
		TaskRole:      taskDef.TaskRole(),
		ExecutionRole: taskDef.ExecutionRole(),
	})
	offersTaskDef.AddContainer(jsii.String("bot"), &offersOpts)
```

(c) In the gap constants block, add:

```go
		allDayHourlyGap = 3 * time.Hour // 1h nominal + 2h slack; runs all day, unlike Lineup's windowed hourlyGap
```

(d) Extend the `job` struct with an optional task definition and add the schedule row (a keyed literal, since the struct grows a field the positional rows do not set):

```go
	type job struct {
		id, cron string
		cmd      *[]*string
		maxGap   time.Duration
		// taskDef overrides the shared task definition. Only FootballOffers
		// sets it: that job alone carries SLEEPER_TOKEN.
		taskDef awsecs.FargateTaskDefinition
	}
```

and after the `FootballTrades` row:

```go
		// Every trade offer proposed TO the operator, graded before they
		// respond. Hourly all day: Sleeper offers wait on the operator, so the
		// value is the grade, not the notification -- but an offer with a short
		// expiry is worth hearing about within the hour. Minute 20 keeps it
		// clear of the :00 Lineup launches and the :45 FootballTrades ones.
		{id: "FootballOffers", cron: "cron(20 * * * ? *)", cmd: jsii.Strings("football-offers"), maxGap: allDayHourlyGap, taskDef: offersTaskDef},
```

(e) In the schedule loop's `awseventstargets.NewEcsTask` call, replace `TaskDefinition: taskDef,` with:

```go
			TaskDefinition:  jobTaskDef(j.taskDef, taskDef),
```

and add near the loop:

```go
// jobTaskDef picks a job's own task definition when it has one, else the
// shared one.
func jobTaskDef(own, shared awsecs.FargateTaskDefinition) awsecs.FargateTaskDefinition {
	if own != nil {
		return own
	}
	return shared
}
```

(A nil interface check is correct here: `job.taskDef` is the `awsecs.FargateTaskDefinition` interface and the positional rows leave it nil.)

- [ ] **Step 3: Build, pins, diff**

Run: `make build-modules && make check-pins`
Expected: clean.

Run (from `infra/`, in a subshell): `(cd infra && cdk diff -c enableBuild=true --method=template 2>&1 | grep -E "OffersTask|FootballOffers|SLEEPER_TOKEN|JOB_SCHEDULES|^\[[+~-]\]" | head -40)`
Expected: `[+]` for the `OffersTask` task definition, the `FootballOffersRule` and its target; `[~]` on the shared execution role's policy (the new SSM parameter read) and on the ops-notify Lambda's `JOB_SCHEDULES` environment; **no change to the existing `Task` task definition**. Lambda asset-hash churn is the known local-synth artefact. If the existing `Task` shows as modified or replaced, stop: the options refactor changed its rendering, and the task is not done until it does not.

- [ ] **Step 4: Makefile**

In `Makefile`'s `run-all` recipe, after the `football-trades --dry-run` line, add (one tab-indented line):

```make
	@echo "=== football-offers --dry-run ===";           if [ -n "$$SLEEPER_TOKEN" ] && [ -n "$$SLEEPER_USER_ID" ] && [ -n "$$SLEEPER_LEAGUE_ID" ]; then time go run . football-offers --dry-run; else echo "SKIPPED: SLEEPER_TOKEN unset (the offers job needs the operator's Sleeper session token; SLEEPER_USER_ID and SLEEPER_LEAGUE_ID too)"; fi && echo
```

- [ ] **Step 5: Commit**

```bash
git add infra/infra.go Makefile
git commit -m "infra: OffersTask carries SLEEPER_TOKEN alone; hourly FootballOffers schedule; run-all gate"
```

---

### Task 8: Docs — README and `docs/dynasty-football.md`

**Files:**
- Modify: `README.md` (env table; football `<details>` block; the "Required for the dynasty football commands" sentence)
- Modify: `docs/dynasty-football.md` (a new `internal/sleeperauth` paragraph after the multi-league discovery paragraph; a new `football-offers` paragraph after the `football-trades` paragraph)

- [ ] **Step 1: README**

Env table, after `SLEEPER_FORMAT_OVERRIDES`:

```markdown
| `SLEEPER_TOKEN` | — | The operator's Sleeper session token, read by `football-offers` **only** (locally from `.env`; in AWS it reaches that one task from SSM `/rosterbot/SLEEPER_TOKEN`). It is the `token` value under the Sleeper web app's local storage (DevTools → Application → Local Storage → `sleeper.com`): account-scoped, full access, roughly a year's lifetime, and Sleeper does not document or support the API it unlocks. Never commit it. When it expires or is revoked, `football-offers` fails every run and ops alerting pages on the third; paste a fresh one into the parameter to recover. |
```

Change the "Required for the dynasty football (Sleeper) commands" sentence to end: "`SLEEPER_LEAGUE_ID`, plus `SLEEPER_USER_ID` for `football-trades` and `football-offers`, and `SLEEPER_TOKEN` for `football-offers`."

Football `<details>` block: add `rosterbot football-offers --dry-run` under the command examples, and append to the paragraph: "`football-offers` (hourly) reads the trade offers **proposed to** the operator's roster in every discovered league through Sleeper's undocumented GraphQL API with the operator's own session token — the public API never shows a live offer — verifies first that the token belongs to `SLEEPER_USER_ID`, grades each offer with the same StatsGuy sum, and pushes one alert per offer written from the operator's side (what you get, what you give, the verdict, who has already accepted, when it expires). It is read-only: nothing is accepted, rejected or countered, and offers the operator sent are not covered. Dedup is one marker per transaction id under `football/offers/`; `--dry-run` sends and marks nothing; an offer that has already expired, or that the operator has already accepted, is counted in the summary rather than alerted."

- [ ] **Step 2: `docs/dynasty-football.md`**

After the **Multi-league discovery** paragraph, add:

```markdown
**`internal/sleeperauth`** — the AUTHENTICATED Sleeper client: `POST https://sleeper.app/graphql` (the app's own host) with the operator's session token sent raw in `authorization`, an explicit `User-Agent` (Sleeper 403s a request without one), and **no disk cache** (freshness is the point, the volume is a dozen calls an hour, and a cache is one more place a token-derived key could land). A separate package from `internal/sleeper` so the token's blast radius is readable from the import graph: `TestOnlyTheOffersCommandImportsThisPackage` pins that no `internal/*` package and no `cmd/` file but `football_offers.go` imports it. **v1 is read-only — there is no `Mutate`**; when writes arrive they take a `WriteAuthorization` obtainable only through an explicit opt-in, on `lineuprun.Emit`'s `applyAuthorization` precedent. Two typed reads: `Me` selects **`user_id` only** (`me` also returns email, phone and the account's own token, and a decoded field is one step from a log line — `TestMeQuery_SelectsUserIDOnly`), and `ProposedTrades(leagueID, rosterID)` wraps `league_transactions(status: "proposed", type: "trade", roster_id)` — `"proposed"` is the live-offer status (`"pending"` matches nothing), `type` excludes pending waiver claims, `roster_id` filters server-side and the query needs no `leg`. Rows decode as `sleeper.Transaction` plus `Settings` (`ExpiresAt()` reads `expires_at` as epoch **seconds**; `Created` is millis), with `draft_picks` accepted in both the object form and the `"roster_id,season,round,owner_id,previous_owner_id"` string form a community reference reports. Errors: HTTP 401 and a GraphQL `code: "unauthorized"` both map to `ErrUnauthorized`; a non-2xx body is dropped (Sleeper sometimes answers HTML, and a body is where a token could be echoed); `TestQuery_HTTP401IsUnauthorizedAndNeverLeaksTheToken` feeds a body containing the token and asserts the error does not. The `diag`-tagged `diag_proposed_test.go` is the operator-run live probe that confirms the roster-scoped query returns every offer the app shows. Query shapes were checked against `github.com/Filip-Kin/sleeper-graphql` (full introspected SDL plus live responses) and spot-verified unauthenticated on 2026-09-28. The API is unsupported and the token grants full account access for about a year; expiry surfaces as a failing `football-offers` run.
```

After the **`football-trades`** paragraph, add:

```markdown
**`football-offers`** (`cmd/football_offers.go`) — hourly (`FootballOffers`, `cron(20 * * * ? *)`), the **only task that receives `SLEEPER_TOKEN`**: infra builds a second Fargate task definition (`OffersTask`) from the same container options as the main task plus that one secret, sharing its task and execution roles, so no other job carries the token. Order of work: `sleeperauth.FromEnv` (fail fast on `ErrNoToken`); **identity check first** — `Me()` must equal `SLEEPER_USER_ID` or the run fails naming the mismatch, so a token from the wrong account cannot run green forever; then Plan 1's `discoverLeagues`, and per league `loadLeagueOffers` (the operator's roster is the one whose `owner_id` is `SLEEPER_USER_ID`; none → printed and skipped) followed by `selectOffers`, a pure filter keeping `type == "trade"`, `status == "proposed"`, the operator's roster in `roster_ids`, `creator != SLEEPER_USER_ID` (offers the operator sent are spec item C, deferred), `expires_at` absent or in the future, and the operator's roster **not** in `consenter_ids` (already accepted, waiting on others); the two dropped classes are counted (`expired`, `answered`) and printed in the summary. An unconditional per-league coverage line (`N proposed for roster R, M to me and live`) separates a quiet league from a blind query. `gradeAndAlertOffers` is the `tradeRunInputs` seam again: `BuildTradeSides` + `GradeTrade` in the league profile's column, `formatOfferAlert` written from the operator's side (`[League] Offer from <proposer>: favors you (+N%)` | `favors <them>` | `dead even` | the two no-verdict reasons; body `You get … | You give … | <column> | expires … | waiting on you`, a three-team offer naming each giving side), then check → send → mark on `layout.FootballOffers` (`football/offers/` ↔ `.football/offers`, a SIBLING of `football/trades/` because an offer and the completed trade it becomes share a `transaction_id` and one namespace would let either marker silence the other; durable, no `MaxAge`, absent from `All()`). `--dry-run` sends and marks nothing. Nothing is logged durably beyond the activity-feed record `notify.Send` writes first: an offer's outcome is not this job's to record, and an accepted one surfaces through `football-trades` as a completed trade within six hours. Delivery uses kind `transactions`; the title's "Offer" is what tells it from a trade. **Failure policy:** `ErrUnauthorized` fails the whole run (every league would fail the same way; `opsalert` escalates on the third consecutive failure, so an expired token pages within about three hours with no new plumbing); any other single-league error is warned and skipped, and the run exits non-zero at the end. Classified year-round in `seasonPolicies`.
```

- [ ] **Step 3: Full suite and lint**

Run: `go test ./... 2>&1 | tail -15 && make lint`
Expected: all PASS (`TestEveryInternalPackageHasDocCoverage` now sees `internal/sleeperauth`; `TestSeasonGate_EveryScheduledCommandIsClassified` sees `football-offers`); lint clean.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/dynasty-football.md
git commit -m "docs: football-offers, SLEEPER_TOKEN handling, internal/sleeperauth"
```

---

## Self-Review

**Spec coverage (Sections 1 and 3, revised):**
- `SLEEPER_TOKEN` env, `FromEnv`/`ErrNoToken`, SSM injection to the offers task only → Tasks 2, 7. ✔
- `Query` to `sleeper.app/graphql`, User-Agent, raw `authorization`, no disk cache → Task 2. ✔
- `Me` selects `user_id` only; `ProposedTrades` wraps `league_transactions(status "proposed", type "trade", roster_id)` with no leg; the leg-scoped fallback is the probe's decision → Tasks 3, 4. ✔
- HTTP 401 and `code: "unauthorized"` → `ErrUnauthorized`; token never in an error; other errors joined → Task 2. ✔
- No `Mutate`; boundary tests (no `internal/*` importer; only `cmd/football_offers.go`) → Task 3. ✔
- `Transaction` `Creator`/`ConsenterIDs` (Plan 1) plus `Settings`/`ExpiresAt` → Task 1. ✔
- Hourly `FootballOffers` schedule, year-round classification → Tasks 6, 7. ✔
- Identity check first → Task 6. ✔
- Per-league roster lookup, filter (type, status, my roster, not my creator, not expired, awaiting me), expiry in the alert → Task 6. ✔
- Grade with `BuildTradeSides`/`GradeTrade` in the league's column; alert from the operator's side with consent state → Task 6. ✔
- Marker family `FootballOffers`, check → send → mark, dry-run marks nothing, kind `transactions` → Tasks 5, 6. ✔
- Failure policy (unauthorized fails the run; others isolate) → Task 6. ✔
- `run-all` gate with a loud skip; docs; `jobwire` untouched → Tasks 7, 8. ✔
- Deliberate scope: no durable offer log (the feed record is the trace; outcomes belong to Plan D), no dedicated notify kind (iOS-visible), no offers the operator sent (spec item C).

**Placeholder scan:** Task 4 and Task 6 Step 5 are explicit operator steps with the exact command and the decision rule written out; no TBDs.

**Type consistency:** `ProposedTrades(ctx, leagueID string, rosterID int)` (Task 3) matches its call in `loadLeagueOffers` (Task 6). `offerRunInputs.league dynasty.LeagueProfile` matches `formatOfferAlert(league dynasty.LeagueProfile, …)`. `dynasty.TradeVerdict.FavoredTeamID` is compared to `strconv.Itoa(myRoster)`, which is how `BuildTradeSidesAll` keys `SideAll.TeamID`. `Transaction.ExpiresAt()` (Task 1) is used by `selectOffers` and `formatOfferAlert` (Task 6) and the probe (Task 4). `layout.FootballOffers` (Task 5) backs `FootballOfferMarkers()` used in Task 6. `allDayHourlyGap` is defined in Task 7 where it is used.

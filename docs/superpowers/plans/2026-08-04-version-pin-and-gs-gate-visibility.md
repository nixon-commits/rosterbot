# Single Fantrax Version Pin + GS-Gate Visibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Fantrax API version appear exactly once in source with a daily probe that pages when it goes stale (rosterbot-7i3), and make the GS gate report the starts it suppressed instead of discarding them (rosterbot-i1c).

**Architecture:** Part 1 exports three existing helpers from the `go-fantrax` fork so rosterbot's one hand-rolled `/fxpa/req` envelope can use them, then adds a `version-check` command that POSTs the pinned version unauthenticated and exits non-zero only on `STALE_CLIENT` — alerting rides the existing run-ledger → `opsalert.Streak` → Pushover path with no new notification code. Part 2 changes `applyGSGate` to return a report of what it suppressed, replaces the downstream inference that currently re-derives it, and persists a `gs_suppressed` flag into the projection snapshot so the weekly rollup is a read of data already synced to S3.

**Tech Stack:** Go 1.x, cobra CLI, AWS CDK (Go) for infra, `httptest` for network-boundary tests, golden-file tests for terminal rendering.

**Spec:** `docs/superpowers/specs/2026-08-04-version-pin-and-gs-gate-visibility-design.md`

## Global Constraints

- Two Go module trees: the root module, and the nested `lambda/`, `opsnotify/`, `infra/` modules which pull the root via `replace ../`. Root `go build ./...` / `go vet ./...` / `go mod tidy` do **not** descend into them. Any `go.mod` change to the root re-stales all three — run `make build-modules` before merging.
- `gofmt` and `go vet` run automatically via PostToolUse hooks on every Edit/Write. Still run `go vet ./...` and `go mod tidy` explicitly after code changes.
- Every new top-level CLI command (a new `cmd/<x>.go` registered on `rootCmd`) **must** get a corresponding line appended to the `run-all` recipe in the `Makefile`.
- Tests require no credentials. All network dependencies are mocked via interfaces or `httptest` servers. Never write a test that reaches the real Fantrax or MLB API.
- Endpoint URLs that tests need to override are package-level `var`, not `const` — existing convention (`standingsURL` in `internal/fantrax/gs_check.go:33`, the `schedule` package URLs).
- `infra/` changes deploy only with `cdk deploy -c enableBuild=true`. Without the flag the deploy **destroys the CodeBuild project**.
- Idempotency invariant (CLAUDE.md): after any optimizer change, run `optimize --dry-run` twice against the same date and confirm the second run reports "No changes needed".
- Golden files are regenerated with `-update` but the **diff must be read** — it is the review.
- Issue tracking is `bd`, never TodoWrite or markdown TODO lists.
- Exact current pinned version value: `185.1.0`.

---

## Task 1: Export the three helpers from the go-fantrax fork

The fork is a separate repo checked out at `/Users/jnixon/go-fantrax`, remote named `fork`, currently at commit `5b9d97f` ("auth_client: bump fantraxAPIVersion 181.0.0 -> 185.1.0"). This task is entirely inside that repo plus one `go.mod` line in rosterbot.

**Files:**
- Modify: `/Users/jnixon/go-fantrax/auth_client/fantrax_client.go:37-73` (rename constant, export two functions)
- Modify: `/Users/jnixon/go-fantrax/auth_client/get_player_pool.go:119`
- Modify: `/Users/jnixon/go-fantrax/auth_client/get_team_service_time.go:52`
- Modify: `/Users/jnixon/go-fantrax/auth_client/get_team_roster.go:96`
- Modify: `/Users/jnixon/go-fantrax/auth_client/get_transactions.go:43,195`
- Modify: `/Users/jnixon/go-fantrax/auth_client/edit_roster.go:125`
- Modify: `/Users/jnixon/rosterbot/go.mod:62` (replace directive pseudo-version)

**Interfaces:**
- Consumes: nothing.
- Produces: `auth_client.APIVersion` (exported `const string`, value `"185.1.0"`); `auth_client.BuildFullRequest(msgs []FantraxMessage, refUrl string) map[string]interface{}`; `auth_client.ReadBody(resp *http.Response) ([]byte, error)`. Tasks 2 and 3 consume these.

- [ ] **Step 1: Rename the constant to its exported form**

In `/Users/jnixon/go-fantrax/auth_client/fantrax_client.go`, change line 57 from:

```go
const fantraxAPIVersion = "185.1.0"
```

to:

```go
const APIVersion = "185.1.0"
```

Leave the doc comment above it (lines 37-56) **completely unchanged** — it is the recovery runbook for finding a new version, and it stays the documented procedure. Only update its first line to name the new identifier:

```go
// APIVersion is the client version sent in every /fxpa/req payload.
```

- [ ] **Step 2: Export the two helper functions**

In the same file, change line 62 from `func buildFullRequest(` to `func BuildFullRequest(`, and line 71 from `"v":      fantraxAPIVersion,` to `"v":      APIVersion,`.

Change line 88 from `func readBody(` to `func ReadBody(`.

Add a sentence to `ReadBody`'s doc comment recording that it errors on *any* `pageError` code — callers that need to inspect the code itself (such as a version probe distinguishing `STALE_CLIENT` from `WARNING_NOT_LOGGED_IN`) must parse the body themselves:

```go
// ReadBody reads resp.Body and checks for an embedded Fantrax pageError before
// returning the bytes. Any method calling /fxpa/req should use this instead of
// io.ReadAll so API errors surface immediately with a descriptive message rather
// than as a confusing downstream unmarshal or "no responses" failure.
//
// It returns an error for ANY non-empty pageError code, including benign ones
// like WARNING_NOT_LOGGED_IN. A caller that needs to branch on the code itself
// must unmarshal the body directly rather than calling this.
```

- [ ] **Step 3: Update the remaining internal references**

Replace every remaining `fantraxAPIVersion` with `APIVersion`, and every `buildFullRequest(` call with `BuildFullRequest(`, and every `readBody(` call with `ReadBody(`. Find them all:

```bash
cd /Users/jnixon/go-fantrax && grep -rn "fantraxAPIVersion\|buildFullRequest\|readBody" --include='*.go' .
```

Expected remaining sites for the constant: `get_player_pool.go:119`, `get_team_service_time.go:52`, `get_team_roster.go:96`, `get_transactions.go:43`, `get_transactions.go:195`, `edit_roster.go:125`.

- [ ] **Step 4: Verify the fork builds and its tests pass**

```bash
cd /Users/jnixon/go-fantrax && go build ./... && go vet ./... && go test ./...
```

Expected: all pass. Then confirm nothing unexported remains:

```bash
cd /Users/jnixon/go-fantrax && grep -rn "fantraxAPIVersion\|buildFullRequest\|readBody" --include='*.go' .
```

Expected: **no output**.

- [ ] **Step 5: Commit the fork change**

```bash
cd /Users/jnixon/go-fantrax && git add -A && git commit -m "auth_client: export APIVersion, BuildFullRequest, ReadBody

rosterbot builds one /fxpa/req envelope itself (the getStandings SCHEDULE
call) and had to duplicate both the version literal and the envelope shape
because the helpers were unexported. That second pin caused two partial
outages when only this repo's version moved.

The envelope it hand-rolled is byte-identical to buildFullRequest's output;
nothing needed a different shape. Exporting these lets it delete the copy.

No behavior change."
```

- [ ] **Step 6: STOP — confirm before pushing**

Pushing to the `fork` remote is outward-facing and is what downstream `go.mod` resolution will pin against. **Ask the user to confirm before running the push.** Then:

```bash
cd /Users/jnixon/go-fantrax && git push fork HEAD && git rev-parse HEAD
```

Record the full SHA from `git rev-parse HEAD` — the next step needs it.

- [ ] **Step 7: Point rosterbot at the new fork commit**

```bash
cd /Users/jnixon/rosterbot
go mod edit -replace github.com/pmurley/go-fantrax=github.com/nixon-commits/go-fantrax@<FULL_SHA_FROM_STEP_6>
go mod tidy
```

`go mod tidy` resolves the SHA into a pseudo-version. Verify `go.mod:62` now reads a `v0.1.14-0.<newtimestamp>-<newsha12>` value different from `v0.1.14-0.20260803155028-5b9d97f48f4c`.

- [ ] **Step 8: Verify the root and nested modules still build**

```bash
cd /Users/jnixon/rosterbot && go build ./... && go vet ./... && make build-modules
```

Expected: all pass. `make build-modules` is mandatory here — the root `go.mod` just changed, which re-stales `lambda/`, `opsnotify/` and `infra/`. If one fails, fix with `cd <dir> && go mod tidy`.

- [ ] **Step 9: Commit**

```bash
cd /Users/jnixon/rosterbot && git add go.mod go.sum && git commit -m "chore: bump go-fantrax fork for exported APIVersion/BuildFullRequest/ReadBody

Prepares rosterbot-7i3: gs_check.go can now use the library's own envelope
builder and pageError-aware body reader instead of its own copies."
```

---

## Task 2: Delete rosterbot's duplicate envelope and version literal

**Files:**
- Modify: `internal/fantrax/gs_check.go:1-96` (imports, envelope construction, body read)
- Test: `internal/fantrax/gs_check_test.go` (verify whether it exists first; if it does not, this task adds no test — the change is covered by Task 1's build plus the grep assertion in Step 4)

**Interfaces:**
- Consumes: `auth_client.BuildFullRequest`, `auth_client.ReadBody` from Task 1.
- Produces: nothing new.

- [ ] **Step 1: Check for an existing test file**

```bash
ls internal/fantrax/gs_check_test.go 2>/dev/null; grep -rn "GetScoringPeriodsAndTeams\|standingsURL" internal/fantrax/*_test.go
```

Note what exists. If a test drives `standingsURL` through an `httptest` server, it must keep passing unchanged — the wire payload is byte-identical after this change, so it will.

- [ ] **Step 2: Replace the hand-built envelope**

In `internal/fantrax/gs_check.go`, replace lines 40-61 (the `fullRequest := map[string]interface{}{...}` literal, ending with the `"v": "185.1.0",` line and its closing brace) with:

```go
	fullRequest := auth_client.BuildFullRequest(
		[]auth_client.FantraxMessage{
			{
				Method: "getStandings",
				Data: map[string]string{
					"leagueId": c.leagueID,
					"view":     "SCHEDULE",
				},
			},
		},
		fmt.Sprintf("https://www.fantrax.com/fantasy/league/%s/standings", c.leagueID),
	)
```

This produces the identical seven-key map the literal did. The `// Must match fantraxAPIVersion in auth_client/...` comment block goes away with it — there is no longer a second pin to keep in sync.

- [ ] **Step 3: Replace the raw body read**

Replace lines 83-86:

```go
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read standings response: %w", err)
	}
```

with:

```go
	// ReadBody surfaces an embedded pageError by name. Reading raw here is what
	// made the 2026-08 STALE_CLIENT outage report itself as the far less useful
	// "no response data in standings" below (rosterbot-7i3).
	body, err := auth_client.ReadBody(resp)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read standings response: %w", err)
	}
```

- [ ] **Step 4: Drop the now-unused import and verify the pin is gone**

`io` may now be unused in this file. Check and remove it from the import block if so:

```bash
cd /Users/jnixon/rosterbot && go build ./... && go vet ./...
```

Then assert the acceptance criterion:

```bash
grep -rn '185\.1\.0' --include='*.go' . ; echo "exit=$?"
```

Expected: **no matching lines** (`exit=1` from grep means no match, which is the pass condition).

- [ ] **Step 5: Run the fantrax package tests**

```bash
go test ./internal/fantrax/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/fantrax/gs_check.go && git commit -m "fix(fantrax): use the library's envelope builder and pageError-aware reader

gs_check.go hand-built a /fxpa/req envelope byte-identical to the library's
buildFullRequest, carrying a second copy of the API version pin. Nothing
enforced the two agreed, and twice only one moved — breaking gs-check, recap
and team-values while optimize kept working.

It also read the body with io.ReadAll, bypassing the library's pageError
check, which is why the August outage surfaced from this call as 'no response
data in standings' rather than STALE_CLIENT.

The version now appears exactly once in source, in the fork. Closes the pin
half of rosterbot-7i3."
```

---

## Task 3: Version probe in internal/fantrax

**Files:**
- Create: `internal/fantrax/version_check.go`
- Create: `internal/fantrax/version_check_test.go`

**Interfaces:**
- Consumes: `auth_client.APIVersion`, `auth_client.BuildFullRequest` from Task 1.
- Produces: `fantrax.VersionStatus` (`VersionOK`, `VersionStale`, `VersionUnknown`), its `String() string` method, and `fantrax.CheckAPIVersion(ctx context.Context) (VersionStatus, string, error)`. The `string` return carries the observed `pageError.code` (empty when there was none). Task 4 consumes all of these.

- [ ] **Step 1: Write the failing test**

Create `internal/fantrax/version_check_test.go`. The two JSON bodies below are the **real** responses captured from Fantrax on 2026-08-04 — do not invent substitutes.

```go
package fantrax

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Real Fantrax responses captured 2026-08-04. The version gate is checked ahead
// of auth, which is what makes an unauthenticated probe a clean discriminator:
// a current version gets as far as the login check, a stale one does not.
const (
	staleBody   = `{"pageError":{"onScreen":false,"code":"STALE_CLIENT","text":"Your browser is using an outdated cached version.","title":"App Update Required","forceReload":false}}`
	currentBody = `{"data":{"sDate":1785868118338},"roles":["03"],"pageError":{"onScreen":false,"code":"WARNING_NOT_LOGGED_IN","text":"Sorry, you must be logged in to perform that action."}}`
	noErrorBody = `{"data":{"sDate":1785868118338}}`
	oddBody     = `{"pageError":{"code":"SOME_FUTURE_CODE","text":"who knows"}}`
)

func probeServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withProbeURL(t *testing.T, url string) {
	t.Helper()
	orig := versionProbeURL
	versionProbeURL = url
	t.Cleanup(func() { versionProbeURL = orig })
}

func TestCheckAPIVersion_StaleClient(t *testing.T) {
	srv := probeServer(t, staleBody)
	withProbeURL(t, srv.URL)

	status, code, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionStale {
		t.Errorf("status = %v, want VersionStale", status)
	}
	if code != "STALE_CLIENT" {
		t.Errorf("code = %q, want STALE_CLIENT", code)
	}
}

func TestCheckAPIVersion_CurrentVersionPassesGate(t *testing.T) {
	srv := probeServer(t, currentBody)
	withProbeURL(t, srv.URL)

	status, code, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionOK {
		t.Errorf("status = %v, want VersionOK", status)
	}
	if code != "WARNING_NOT_LOGGED_IN" {
		t.Errorf("code = %q, want WARNING_NOT_LOGGED_IN", code)
	}
}

func TestCheckAPIVersion_NoPageErrorIsOK(t *testing.T) {
	srv := probeServer(t, noErrorBody)
	withProbeURL(t, srv.URL)

	status, _, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionOK {
		t.Errorf("status = %v, want VersionOK", status)
	}
}

// An unrecognized code must NOT be reported as stale. The probe is only allowed
// to page for the one condition it can positively identify; anything else is
// VersionUnknown and the command exits 0.
func TestCheckAPIVersion_UnrecognizedCodeIsUnknownNotStale(t *testing.T) {
	srv := probeServer(t, oddBody)
	withProbeURL(t, srv.URL)

	status, code, err := CheckAPIVersion(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != VersionUnknown {
		t.Errorf("status = %v, want VersionUnknown", status)
	}
	if code != "SOME_FUTURE_CODE" {
		t.Errorf("code = %q, want SOME_FUTURE_CODE", code)
	}
}

func TestCheckAPIVersion_NonOKStatusIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	withProbeURL(t, srv.URL)

	status, _, err := CheckAPIVersion(context.Background())
	if status != VersionUnknown {
		t.Errorf("status = %v, want VersionUnknown", status)
	}
	if err == nil {
		t.Error("expected an error describing the non-200 status")
	}
}

// The probe must send the pinned version — that is the whole point. Assert the
// wire payload rather than trusting the constant is threaded through.
func TestCheckAPIVersion_SendsThePinnedVersion(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(currentBody))
	}))
	t.Cleanup(srv.Close)
	withProbeURL(t, srv.URL)

	if _, _, err := CheckAPIVersion(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("probe sent unparseable JSON: %v", err)
	}
	if sent["v"] != auth_client.APIVersion {
		t.Errorf(`sent "v" = %v, want %q`, sent["v"], auth_client.APIVersion)
	}
}
```

Add `"encoding/json"`, `"io"`, and `"github.com/pmurley/go-fantrax/auth_client"` to the test's import block.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/fantrax/ -run TestCheckAPIVersion -v
```

Expected: FAIL to compile — `undefined: versionProbeURL`, `undefined: CheckAPIVersion`, `undefined: VersionStale`.

- [ ] **Step 3: Write the implementation**

Create `internal/fantrax/version_check.go`:

```go
package fantrax

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pmurley/go-fantrax/auth_client"
)

// VersionStatus is the outcome of probing Fantrax's server-side client-version
// gate with the version this binary is pinned to.
type VersionStatus int

const (
	// VersionOK means the pinned version passed Fantrax's gate.
	VersionOK VersionStatus = iota
	// VersionStale means Fantrax rejected the pinned version outright.
	VersionStale
	// VersionUnknown means the probe could not determine the answer: an
	// unrecognized pageError code, a non-200, or a transport failure.
	VersionUnknown
)

func (s VersionStatus) String() string {
	switch s {
	case VersionOK:
		return "ok"
	case VersionStale:
		return "stale"
	default:
		return "unknown"
	}
}

// versionProbeURL is the Fantrax API endpoint. Var for test overriding.
var versionProbeURL = "https://www.fantrax.com/fxpa/req"

// versionProbeTimeout bounds the single probe request.
const versionProbeTimeout = 20 * time.Second

// CheckAPIVersion asks Fantrax whether the version this binary pins still
// passes its server-side gate. It deliberately does NOT authenticate: the
// version gate is checked ahead of auth, so an unauthenticated getUserInfo
// separates the two cases cleanly —
//
//	stale pin    -> pageError.code == "STALE_CLIENT"
//	current pin  -> pageError.code == "WARNING_NOT_LOGGED_IN" (gate passed)
//
// Verified in both directions against live Fantrax on 2026-08-04.
//
// This validates the local pin rather than re-deriving the upstream value.
// Reading the current version out of Fantrax's web bundle is possible (see the
// runbook on auth_client.APIVersion) but means parsing an unstable Angular
// chunk layout and risks confidently adopting a wrong value; asking whether our
// own constant still works has a yes/no answer and nothing to get wrong.
//
// The returned string is the observed pageError code, empty when there was
// none. An error is returned only for transport/protocol failures, never for
// VersionStale — a stale pin is a successful probe with a bad answer.
func CheckAPIVersion(ctx context.Context) (VersionStatus, string, error) {
	payload := auth_client.BuildFullRequest(
		[]auth_client.FantraxMessage{{
			Method: "getUserInfo",
			Data:   map[string]string{},
		}},
		"https://www.fantrax.com/",
	)

	body, err := json.Marshal(payload)
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("marshal version probe: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, versionProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, versionProbeURL, bytes.NewReader(body))
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("create version probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("send version probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return VersionUnknown, "", fmt.Errorf("version probe returned status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return VersionUnknown, "", fmt.Errorf("read version probe response: %w", err)
	}

	// Parsed here rather than via auth_client.ReadBody, which collapses every
	// pageError code into an error — this is the one caller that must branch on
	// the code itself.
	var envelope struct {
		PageError struct {
			Code string `json:"code"`
		} `json:"pageError"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return VersionUnknown, "", fmt.Errorf("unmarshal version probe response: %w", err)
	}

	switch code := envelope.PageError.Code; code {
	case "STALE_CLIENT":
		return VersionStale, code, nil
	case "", "WARNING_NOT_LOGGED_IN":
		return VersionOK, code, nil
	default:
		return VersionUnknown, code, nil
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/fantrax/ -run TestCheckAPIVersion -v
```

Expected: all six PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fantrax/version_check.go internal/fantrax/version_check_test.go
git commit -m "feat(fantrax): probe whether the pinned API version still passes

One unauthenticated POST separates a stale pin from a current one, because
Fantrax checks the version gate ahead of auth: a stale version answers
STALE_CLIENT, a current one gets as far as WARNING_NOT_LOGGED_IN.

Validates the local constant rather than re-deriving upstream's value from
the web bundle — same protection, nothing to parse, no chance of adopting a
wrong version. An unrecognized code is VersionUnknown, never VersionStale.

Part of rosterbot-7i3."
```

---

## Task 4: The version-check command

**Files:**
- Create: `cmd/version_check.go`
- Create: `cmd/version_check_test.go`
- Modify: `Makefile` (the `run-all` recipe, after the `gs-check` line at ~line 62)

**Interfaces:**
- Consumes: `fantrax.CheckAPIVersion`, `fantrax.VersionOK`, `fantrax.VersionStale`, `fantrax.VersionUnknown` from Task 3; `auth_client.APIVersion` from Task 1.
- Produces: the `version-check` cobra command on `rootCmd`, and `versionCheckResultLine(status fantrax.VersionStatus, code string, err error) (line string, exitErr error)` — a pure function so the exit mapping is testable without a network or a cobra harness.

- [ ] **Step 1: Write the failing test**

Create `cmd/version_check_test.go`:

```go
package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// The exit mapping is the load-bearing policy: the probe may only fail the job
// for the one condition it can positively identify. Everything else exits 0, so
// a Fantrax outage — which already fails every other job and pages through
// them — does not raise a second alert for a cause this probe did not diagnose.
func TestVersionCheckResultLine_ExitPolicy(t *testing.T) {
	tests := []struct {
		name        string
		status      fantrax.VersionStatus
		code        string
		err         error
		wantFailure bool
		wantContain string
	}{
		{
			name:        "stale pin is the only failure",
			status:      fantrax.VersionStale,
			code:        "STALE_CLIENT",
			wantFailure: true,
			wantContain: "STALE_CLIENT",
		},
		{
			name:        "current pin succeeds",
			status:      fantrax.VersionOK,
			code:        "WARNING_NOT_LOGGED_IN",
			wantFailure: false,
		},
		{
			name:        "unrecognized code does not fail the job",
			status:      fantrax.VersionUnknown,
			code:        "SOME_FUTURE_CODE",
			wantFailure: false,
			wantContain: "SOME_FUTURE_CODE",
		},
		{
			name:        "transport error does not fail the job",
			status:      fantrax.VersionUnknown,
			err:         errors.New("dial tcp: connection refused"),
			wantFailure: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, exitErr := versionCheckResultLine(tt.status, tt.code, tt.err)
			if tt.wantFailure && exitErr == nil {
				t.Fatal("expected a non-nil error so the command exits non-zero")
			}
			if !tt.wantFailure && exitErr != nil {
				t.Fatalf("expected exit 0, got error: %v", exitErr)
			}
			if tt.wantContain != "" && !strings.Contains(line+errString(exitErr), tt.wantContain) {
				t.Errorf("output %q + %q missing %q", line, errString(exitErr), tt.wantContain)
			}
		})
	}
}

// The stale message is quoted verbatim into the Pushover by opsalert, which
// takes the last non-empty line of the log. It has to name the version.
func TestVersionCheckResultLine_StaleMessageNamesThePinnedVersion(t *testing.T) {
	_, exitErr := versionCheckResultLine(fantrax.VersionStale, "STALE_CLIENT", nil)
	if exitErr == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(exitErr.Error(), "185.1.0") {
		t.Errorf("stale error %q does not name the pinned version", exitErr.Error())
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cmd/ -run TestVersionCheckResultLine -v
```

Expected: FAIL to compile — `undefined: versionCheckResultLine`.

- [ ] **Step 3: Write the implementation**

Create `cmd/version_check.go`:

```go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/spf13/cobra"
)

var versionCheckCmd = &cobra.Command{
	Use:   "version-check",
	Short: "Check whether the pinned Fantrax API version still passes Fantrax's gate",
	Long: `Probes Fantrax with the client version this binary pins, unauthenticated.

Exits non-zero ONLY when Fantrax rejects the pin outright (STALE_CLIENT), which
is the one condition the probe can positively identify. An unrecognized response
or a transport failure exits 0 with a warning: a Fantrax outage already fails
every other scheduled job and pages through those, and this probe must not raise
a second alert for a cause it did not diagnose.

A non-zero exit is written to the run ledger as FAILED, which the opsnotify
Lambda turns into a Pushover on the first failure, quoting the last log line.`,
	RunE: runVersionCheck,
}

func init() {
	rootCmd.AddCommand(versionCheckCmd)
}

// versionCheckResultLine maps a probe outcome to the line to print and whether
// the command should fail. Split out from runVersionCheck so the exit policy is
// testable without a network or a cobra harness.
//
// Returning a non-nil error is what makes entrypoint.sh record the run as
// FAILED; opsalert.Streak then fires on the first failure and quotes the last
// non-empty log line, so the error text IS the alert body.
func versionCheckResultLine(status fantrax.VersionStatus, code string, err error) (string, error) {
	switch {
	case err != nil:
		return fmt.Sprintf("⚠ version probe inconclusive: %v (pinned %s unverified; not failing the run)",
			err, auth_client.APIVersion), nil
	case status == fantrax.VersionStale:
		return "", fmt.Errorf("STALE_CLIENT: Fantrax rejected pinned API version %s — bump auth_client.APIVersion in the go-fantrax fork (runbook is the doc comment on that constant)",
			auth_client.APIVersion)
	case status == fantrax.VersionUnknown:
		return fmt.Sprintf("⚠ version probe inconclusive: unrecognized pageError code %q (pinned %s unverified; not failing the run)",
			code, auth_client.APIVersion), nil
	default:
		return fmt.Sprintf("Fantrax API version %s OK (gate passed, response code %q)",
			auth_client.APIVersion, code), nil
	}
}

func runVersionCheck(cmd *cobra.Command, args []string) error {
	// No initApp: the probe is deliberately unauthenticated and needs no config,
	// so it keeps working even when credentials or the session cache are broken.
	status, code, err := fantrax.CheckAPIVersion(context.Background())

	line, exitErr := versionCheckResultLine(status, code, err)
	if exitErr != nil {
		// Printed to stderr as well so it lands in the ledger's log_tail as the
		// last non-empty line, which is what opsalert quotes into the Pushover.
		fmt.Fprintln(os.Stderr, exitErr)
		return exitErr
	}
	fmt.Println(line)
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./cmd/ -run TestVersionCheckResultLine -v
```

Expected: all PASS.

- [ ] **Step 5: Add version-check to the run-all smoke target**

In `Makefile`, insert a line into the `run-all` recipe immediately after the `gs-check --dry-run` line:

```makefile
	@echo "=== version-check ===";                        time go run . version-check;                                    echo
```

Match the existing column alignment of the surrounding lines. There is no `--dry-run` flag — the command is read-only by construction.

- [ ] **Step 6: Verify the command runs end to end against live Fantrax**

```bash
go run . version-check; echo "exit=$?"
```

Expected: `Fantrax API version 185.1.0 OK (gate passed, response code "WARNING_NOT_LOGGED_IN")` and `exit=0`.

- [ ] **Step 7: Commit**

```bash
git add cmd/version_check.go cmd/version_check_test.go Makefile
git commit -m "feat(cmd): add version-check, a daily probe of the Fantrax API version pin

Exits non-zero only on STALE_CLIENT. That routes through the existing
run-ledger -> opsalert.Streak -> Pushover path with no new notification code:
Streak fires on the first failure, quotes the last log line (which is the
diagnosis), and sends Recovered once the pin is fixed.

An unrecognized code or a transport failure exits 0. A Fantrax outage already
pages through every other job; this probe only alerts for what it can name.

Part of rosterbot-7i3."
```

---

## Task 5: Schedule the probe

**Files:**
- Modify: `infra/infra.go` (the `jobs` slice, ~line 683, immediately after the `GsCheck` entry)

**Interfaces:**
- Consumes: the `version-check` command from Task 4.
- Produces: a `VersionCheck` EventBridge schedule and its `JOB_SCHEDULES` heartbeat entry.

- [ ] **Step 1: Add the schedule entry**

In `infra/infra.go`, add to the `jobs` slice. Place it **first** in the daily group, before `Prospects`:

```go
		// Asserts the pinned Fantrax API version still passes Fantrax's gate.
		// 10:30 UTC sits ahead of the first daily job (Prospects, 11:00) and well
		// ahead of the hourly Lineup window (14:00-03:00), so a dead pin is known
		// before the day's cascade rather than after it. Exits non-zero on
		// STALE_CLIENT, so the alert rides the ordinary task-failure path.
		{"VersionCheck", "cron(30 10 * * ? *)", jsii.Strings("version-check"), dailyGap},
```

`dailyGap` (26h) is correct: the job runs once a day, so its longest legitimate quiet period is 24h plus slack.

- [ ] **Step 2: Verify the infra module builds**

```bash
cd infra && go build ./... && go vet ./... && cd ..
```

Expected: PASS.

- [ ] **Step 3: Verify the synthesized template contains the new rule**

```bash
cd infra && npx cdk synth -c enableBuild=true 2>/dev/null | grep -c "VersionCheck"; cd ..
```

Expected: a count greater than 0. If `cdk` is unavailable locally, skip this step and note it — CI and the deploy will catch a malformed entry.

- [ ] **Step 4: Commit**

```bash
git add infra/infra.go
git commit -m "feat(infra): schedule version-check daily at 10:30 UTC

Ahead of the first daily job and the hourly Lineup window, so a dead API
version pin is known before the day's cascade rather than after 50+ failed
runs. dailyGap gives it heartbeat coverage too.

Closes the detection half of rosterbot-7i3."
```

**Note for the operator:** this does not take effect until `cdk deploy -c enableBuild=true` runs. Without the `enableBuild` flag the deploy destroys the CodeBuild project.

---

## Task 6: The GS gate reports what it suppressed

**Files:**
- Modify: `internal/optimizer/gs_budget.go:78-276` (`applyGSGate` signature and both suppression paths)
- Modify: `internal/optimizer/pitcher_lineup.go:29-34` (`PitcherResult` gains a field), `:52` (call site)
- Modify: `internal/optimizer/gs_budget_test.go` (extend existing tests, add new ones)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `optimizer.GSGateReport` with fields `Suppressed []SuppressedStart`, `Limit int`, `Used int`, `Remaining int`, and method `SuppressedPts() float64`; `optimizer.SuppressedStart` with fields `PlayerID string`, `Name string`, `ProjectedPts float64`; and `PitcherResult.GateReport GSGateReport`. Tasks 7 and 8 consume these.

- [ ] **Step 1: Write the failing tests**

Append to `internal/optimizer/gs_budget_test.go`:

```go
// The gate already knew which starters it flipped; it just threw the answer
// away, leaving pitcherPipelinesFor to re-derive it by inference. These pin the
// report as the authoritative account.
func TestApplyGSGate_ReportNamesTheSuppressedStarters(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Ace", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
		{Player: fantrax.Player{ID: "p2", Name: "Filler", PosShortNames: "SP"}, ExpectedPts: 5, IsStarter: true},
		{Player: fantrax.Player{ID: "r1", Name: "Reliever", PosShortNames: "RP"}, ExpectedPts: 7, IsStarter: false, HasGame: true},
	}
	budget := &GSBudget{
		Limit:   12,
		Used:    10,
		Today:   date("2026-04-10"),
		WeekEnd: date("2026-04-12"),
		Forecast: []DayForecast{
			{Date: date("2026-04-11"), ConfirmedStarters: []float64{8}},
		},
	}
	// remaining=2, planned=3. Top 2: p1(10), future(8). p2(5) is cut.
	_, report := applyGSGate(scored, budget)

	if len(report.Suppressed) != 1 {
		t.Fatalf("Suppressed = %d entries, want 1: %+v", len(report.Suppressed), report.Suppressed)
	}
	got := report.Suppressed[0]
	if got.PlayerID != "p2" {
		t.Errorf("suppressed PlayerID = %q, want p2", got.PlayerID)
	}
	if got.Name != "Filler" {
		t.Errorf("suppressed Name = %q, want Filler", got.Name)
	}
	if got.ProjectedPts != 5 {
		t.Errorf("suppressed ProjectedPts = %v, want 5", got.ProjectedPts)
	}
	if report.SuppressedPts() != 5 {
		t.Errorf("SuppressedPts() = %v, want 5", report.SuppressedPts())
	}
	if report.Limit != 12 || report.Used != 10 || report.Remaining != 2 {
		t.Errorf("budget echo = %d/%d rem %d, want 12/10 rem 2", report.Used, report.Limit, report.Remaining)
	}
}

// Locked players are exempt from suppression, so they must not appear in the
// report either — otherwise the reported cost includes starts the gate never
// actually declined.
func TestApplyGSGate_ReportExcludesLockedPlayers(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "locked", Name: "Locked SP", PosShortNames: "SP", Locked: true}, ExpectedPts: 9, IsStarter: true},
		{Player: fantrax.Player{ID: "open", Name: "Open SP", PosShortNames: "SP"}, ExpectedPts: 4, IsStarter: true},
	}
	budget := &GSBudget{Limit: 10, Used: 10, Today: date("2026-04-10"), WeekEnd: date("2026-04-12")}

	_, report := applyGSGate(scored, budget)

	for _, s := range report.Suppressed {
		if s.PlayerID == "locked" {
			t.Error("locked player must not appear in the gate report")
		}
	}
	if len(report.Suppressed) != 1 || report.Suppressed[0].PlayerID != "open" {
		t.Errorf("Suppressed = %+v, want exactly the unlocked SP", report.Suppressed)
	}
}

// A budget that covers everything planned suppresses nothing, and the report
// must say so rather than being left zero-valued by accident.
func TestApplyGSGate_AmpleBudgetReportsNoSuppressions(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Ace", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
	}
	budget := &GSBudget{Limit: 12, Used: 0, Today: date("2026-04-10"), WeekEnd: date("2026-04-12")}

	_, report := applyGSGate(scored, budget)

	if len(report.Suppressed) != 0 {
		t.Errorf("Suppressed = %+v, want none", report.Suppressed)
	}
	if report.SuppressedPts() != 0 {
		t.Errorf("SuppressedPts() = %v, want 0", report.SuppressedPts())
	}
	if report.Limit != 12 {
		t.Errorf("Limit = %d, want 12 even with no suppressions", report.Limit)
	}
}

// A nil budget means no GS limit is configured at all — the report must be
// inert, not a zero-limit report that reads as "budget 0/0".
func TestApplyGSGate_NilBudgetReportIsEmpty(t *testing.T) {
	scored := []ScoredPitcher{
		{Player: fantrax.Player{ID: "p1", Name: "Ace", PosShortNames: "SP"}, ExpectedPts: 10, IsStarter: true},
	}
	_, report := applyGSGate(scored, nil)
	if len(report.Suppressed) != 0 || report.Limit != 0 {
		t.Errorf("nil-budget report = %+v, want zero value", report)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/optimizer/ -run TestApplyGSGate -v
```

Expected: FAIL to compile — `applyGSGate` returns one value, not two; `GSGateReport` undefined.

- [ ] **Step 3: Add the report types**

At the top of `internal/optimizer/gs_budget.go`, after the `DayForecast` type (line 29), add:

```go
// GSGateReport records what the weekly game-start budget cost on one date: the
// starts the gate declined, plus the budget state that forced the decision.
//
// The gate has always known this; before rosterbot-i1c it discarded the answer
// and the display layer re-derived an approximation of it by inference.
type GSGateReport struct {
	Suppressed []SuppressedStart
	// Budget state echoed so a consumer can report the cost and its cause
	// together without also carrying the *GSBudget.
	Limit, Used, Remaining int
}

// SuppressedStart is one start the gate declined to spend budget on.
type SuppressedStart struct {
	PlayerID     string
	Name         string
	ProjectedPts float64 // blended pts/game at the moment of suppression
}

// SuppressedPts is the GROSS projected value of the starts the gate declined.
//
// It is NOT a net loss to the week, and must not be subtracted from a realised
// total. The budget is not destroyed by a suppression — it is spent on a
// higher-ranked start instead, which is the entire point of the ranked cut.
//
// What it measures is SP capacity the roster owns and the league will not let
// it deploy: 13 active hitter slots against 6 undifferentiated P slots plus a
// weekly game-start cap mean that above the cap an extra ace start produces
// exactly zero marginal points. A gate that fires regularly is the measurement
// of that surplus.
func (r GSGateReport) SuppressedPts() float64 {
	var total float64
	for _, s := range r.Suppressed {
		total += s.ProjectedPts
	}
	return total
}
```

- [ ] **Step 4: Change applyGSGate to build and return the report**

Change the signature on line 78 to:

```go
func applyGSGate(scored []ScoredPitcher, budget *GSBudget) ([]ScoredPitcher, GSGateReport) {
```

Update the doc comment's final paragraph to note the second return value:

```go
// The returned GSGateReport names every start the gate declined. It is the
// authoritative account — downstream must not re-derive suppression by
// comparing probable-starter membership against IsStarter.
```

Then make these four edits inside the function body:

1. The nil/zero-limit early return (line 79-81) becomes:

```go
	if budget == nil || budget.Limit == 0 {
		return scored, GSGateReport{}
	}
```

2. Immediately after `remaining := budget.Remaining()` (line 83), add the report seed:

```go
	report := GSGateReport{Limit: budget.Limit, Used: budget.Used, Remaining: remaining}
```

3. The `remaining <= 0` sweep (lines 84-96) becomes:

```go
	if remaining <= 0 {
		for i := range scored {
			// Locked players' GS is already decided (either consumed in an
			// active slot or permanently unconsumed on bench) — flipping
			// their IsStarter has no effect on the lineup and only misleads
			// the display and pts calculation.
			if scored[i].Player.Locked {
				continue
			}
			if scored[i].IsStarter {
				report.Suppressed = append(report.Suppressed, SuppressedStart{
					PlayerID:     scored[i].Player.ID,
					Name:         scored[i].Player.Name,
					ProjectedPts: scored[i].ExpectedPts,
				})
			}
			scored[i].IsStarter = false
		}
		return scored, report
	}
```

Note the added `if scored[i].IsStarter` guard: the existing loop flips every unlocked pitcher including ones that were never starters, and only the ones that actually *were* starters represent a declined start.

4. The three remaining `return scored` statements (the empty-`todayStarters` early return at line 113, the budget-covers-everything return at line 134, and the final return at line 275) become `return scored, report`. The final suppression loop (lines 270-274) becomes:

```go
	for i, s := range todayStarters {
		if !keepToday[i] {
			report.Suppressed = append(report.Suppressed, SuppressedStart{
				PlayerID:     scored[s.idx].Player.ID,
				Name:         scored[s.idx].Player.Name,
				ProjectedPts: scored[s.idx].ExpectedPts,
			})
			scored[s.idx].IsStarter = false
		}
	}
	return scored, report
```

- [ ] **Step 5: Carry the report on PitcherResult**

In `internal/optimizer/pitcher_lineup.go`, add a field to `PitcherResult` (lines 29-34):

```go
// PitcherResult describes the pitcher lineup changes the optimizer wants to make.
type PitcherResult struct {
	ToActivate []fantrax.PlayerSlot
	ToBench    []string // player IDs to move to reserve
	Scored     []ScoredPitcher
	// GateReport names the starts the weekly GS budget declined. Zero-valued
	// when no budget was in force for this date.
	GateReport GSGateReport
}
```

Change the call site at line 52 from:

```go
	scored = applyGSGate(scored, gsBudget)
```

to:

```go
	scored, gateReport := applyGSGate(scored, gsBudget)
```

and set `GateReport: gateReport` on the returned `PitcherResult`. Find the construction site:

```bash
grep -n "PitcherResult{" internal/optimizer/pitcher_lineup.go
```

If `scored` is already declared above line 52, use `var gateReport GSGateReport` before it and plain assignment (`scored, gateReport = applyGSGate(...)`) to avoid a shadowing bug.

- [ ] **Step 6: Update the existing tests to the new signature**

Every existing `applyGSGate(scored, budget)` call in `internal/optimizer/gs_budget_test.go` needs a second return. For calls that ignore it:

```bash
grep -n "applyGSGate(" internal/optimizer/gs_budget_test.go
```

Change each `result := applyGSGate(...)` to `result, _ := applyGSGate(...)`. Do not weaken any existing assertion — they all still hold.

- [ ] **Step 7: Run the full optimizer suite**

```bash
go test ./internal/optimizer/... -v 2>&1 | tail -40
```

Expected: every existing test still PASSES, plus the four new ones.

- [ ] **Step 8: Commit**

```bash
git add internal/optimizer/gs_budget.go internal/optimizer/gs_budget_test.go internal/optimizer/pitcher_lineup.go
git commit -m "feat(optimizer): the GS gate reports the starts it suppressed

applyGSGate knew exactly which starters it flipped and discarded the answer,
so the only account of it downstream was an inference in pitcherPipelinesFor.
It now returns a GSGateReport naming each declined start and echoing the
budget state that forced the cut.

SuppressedPts is documented as GROSS, not a net weekly loss: the budget is
spent on a higher-ranked start instead. What it measures is SP capacity the
roster owns and the league's 6 P slots plus weekly cap will not let it deploy.

Part of rosterbot-i1c."
```

---

## Task 7: Consume the report — kill the inference, fix the discount, show it daily

**Files:**
- Modify: `internal/lineuprun/optimize_dates.go:356-433` (`pitcherPipelinesFor` signature and stage 3)
- Modify: `internal/lineuprun/display.go:384-391` (the GS footer)
- Modify: `internal/lineuprun/testdata/board_full.golden` (regenerated)
- Modify: `internal/lineuprun/render_test.go` or `display_test.go` (whichever holds `TestRenderDateResult` — check first)

**Interfaces:**
- Consumes: `optimizer.GSGateReport`, `optimizer.SuppressedStart`, `PitcherResult.GateReport` from Task 6; `optimizer.NonStarterSPDiscount` (existing, value `0.05`).
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Locate the render test**

```bash
grep -rn "func TestRenderDateResult" internal/lineuprun/
```

Note the file. The golden files are `internal/lineuprun/testdata/board_full.golden` and `board_plain.golden`; only `board_full.golden` currently contains a `GS:` line.

- [ ] **Step 2: Replace the inference in pitcherPipelinesFor**

In `internal/lineuprun/optimize_dates.go`, change the function signature (line 360-366) from taking `probableStarters map[string]string, gated bool` to taking the report. Replace:

```go
func pitcherPipelinesFor(
	res optimizer.PitcherResult,
	src projections.PitcherSource,
	scoring fantrax.ScoringWeights,
	probableStarters map[string]string,
	gated bool,
) map[string]*projections.PitcherPipelineDetail {
```

with:

```go
// pitcherPipelinesFor builds the pitcher-side pipeline table: base → blend →
// GS gate → final. Gate attribution comes from res.GateReport, which is the
// gate's own account of what it declined. It used to be re-derived here by
// inference (probable ∧ !IsStarter ∧ gated), which was both fragile and, in
// the case of the discount multiplier, wrong.
func pitcherPipelinesFor(
	res optimizer.PitcherResult,
	src projections.PitcherSource,
	scoring fantrax.ScoringWeights,
) map[string]*projections.PitcherPipelineDetail {
```

Immediately before the `out := make(...)` line (line 377), add the lookup set:

```go
	suppressed := make(map[string]bool, len(res.GateReport.Suppressed))
	for _, s := range res.GateReport.Suppressed {
		suppressed[s.PlayerID] = true
	}
```

Replace stage 3 (lines 419-428) with:

```go
		// Stage 3: GS Gate — the gate's own report says which starts it declined.
		// The suppressed starter carries the optimizer's NonStarterSPDiscount.
		pd.FinalPtsPerGame = pd.BlendedPtsPerGame
		if suppressed[sp.Player.ID] {
			pd.WasGated = true
			pd.FinalPtsPerGame = pd.BlendedPtsPerGame * optimizer.NonStarterSPDiscount
			pd.GateDelta = pd.FinalPtsPerGame - pd.BlendedPtsPerGame
		}
```

This fixes a second bug in the same lines: the literal was `0.10` while `NonStarterSPDiscount` is `0.05`, so the pipeline view reported gated pitchers at double their real post-discount value.

The `spEligible` local is still used to compute `role` above, so leave it. Update the call site:

```bash
grep -n "pitcherPipelinesFor(" internal/lineuprun/*.go
```

Drop the now-unused `probableStarters` and `gated` arguments there. If `gated` was computed solely for this call, delete its computation too.

- [ ] **Step 3: Add the daily display line**

In `internal/lineuprun/display.go`, replace the `if gsBudget != nil` block (lines 384-391) with:

```go
	if gsBudget != nil {
		remaining := gsBudget.Remaining()
		hLines = append(hLines, "")
		hGreen = append(hGreen, false)
		pLines = append(pLines, fmt.Sprintf("GS: %d/%d used (%d rem, %.1f future)",
			gsBudget.Used, gsBudget.Limit, remaining, gsBudget.FutureDemand()))
		pGreen = append(pGreen, false)
		// The gate firing is the measurement of surplus SP: capacity the roster
		// owns that the league's 6 P slots and weekly cap will not let it deploy.
		// Gross projected value, not a net loss — the budget goes to a better start.
		if n := len(dr.pitcherResult.GateReport.Suppressed); n > 0 {
			hLines = append(hLines, "")
			hGreen = append(hGreen, false)
			pLines = append(pLines, fmt.Sprintf("GS gate: %d start%s suppressed (-%.1f proj pts)",
				n, plural(n), dr.pitcherResult.GateReport.SuppressedPts()))
			pGreen = append(pGreen, false)
		}
	}
```

Check whether a `plural` helper already exists in the package:

```bash
grep -rn "func plural" internal/lineuprun/
```

If it does not, add it near the other small helpers in `display.go`:

```go
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
```

`renderDateResult` already receives `dr dateResult` (line 212), so `dr.pitcherResult.GateReport` is in scope with no signature change.

- [ ] **Step 4: Add a golden case that exercises the new line**

The existing `board_full.golden` fixture has a budget but no suppressions, so it will not cover the new branch. In the render test file found in Step 1, add a case that sets a non-empty `GateReport` on the fixture's `pitcherResult`, writing to a new golden `internal/lineuprun/testdata/board_gs_suppressed.golden`. Follow the existing test's exact construction pattern — read it first and mirror it, including how it names and loads its golden file.

- [ ] **Step 5: Run the tests, then regenerate and READ the golden diff**

```bash
go test ./internal/lineuprun/ -run TestRenderDateResult -v
```

If the existing goldens fail because the layout shifted, regenerate:

```bash
go test ./internal/lineuprun/ -run TestRenderDateResult -update
git diff internal/lineuprun/testdata/
```

**Read the diff — it is the review.** `board_full.golden` should be unchanged (no suppressions in that fixture); only the new `board_gs_suppressed.golden` should appear. If `board_full.golden` changed, the new block is emitting a line when it should not — fix it rather than accepting the diff.

- [ ] **Step 6: Run the full package suite**

```bash
go test ./internal/lineuprun/... && go vet ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/lineuprun/optimize_dates.go internal/lineuprun/display.go internal/lineuprun/testdata/ internal/lineuprun/*_test.go
git commit -m "fix(lineuprun): use the gate's report instead of re-deriving suppression

pitcherPipelinesFor inferred gate suppression from (probable AND !IsStarter
AND gated) and applied a 0.10 multiplier where NonStarterSPDiscount is 0.05 —
so the --pipeline view reported gated pitchers at double their real
post-discount value. Both go away: attribution now comes from GateReport and
the multiplier is the constant.

Adds the daily board line showing today's suppressed count and gross forgone
projected points.

Part of rosterbot-i1c."
```

---

## Task 8: Persist to the snapshot and roll up the week

**Files:**
- Modify: `internal/backtest/backtest.go:104-117` (`SnapshotPlayer` gains a field)
- Modify: `internal/lineuprun/snapshot.go:69-88` (populate it)
- Create: `internal/backtest/gs_gate_summary.go`
- Create: `internal/backtest/gs_gate_summary_test.go`
- Modify: `internal/backtest/backtest.go:800-891` (`FormatReport` prints the section) — or `cmd/backtest.go:123` if the report struct has no natural home for it; prefer `FormatReport`
- Modify: `cmd/backtest.go:94-123` (pass the snapshot dir and dates through)

**Interfaces:**
- Consumes: `optimizer.GSGateReport` from Task 6.
- Produces: `backtest.SnapshotPlayer.GSSuppressed bool` (JSON `gs_suppressed,omitempty`); `backtest.GateSummary` with fields `Days int`, `DaysWithSnapshot int`, `SuppressedStarts int`, `SuppressedPts float64`, `ByDate []GateDay`; `backtest.GateDay` with fields `Date time.Time`, `Starts int`, `Pts float64`; and `backtest.SummarizeGSGate(dir string, dates []time.Time) GateSummary`.

- [ ] **Step 1: Write the failing test**

Create `internal/backtest/gs_gate_summary_test.go`:

```go
package backtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestSnapshot(t *testing.T, dir string, date time.Time, pitchers []SnapshotPlayer) {
	t.Helper()
	snap := Snapshot{
		Date:        date.Format("2006-01-02"),
		GeneratedAt: date,
		Pitchers:    pitchers,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(dir, date.Format("2006-01-02")+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestSummarizeGSGate_SumsSuppressedStartsAcrossDays(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
		{PlayerID: "p2", IsPitcher: true, ProjPtsPerGame: 9.0},
	})
	writeTestSnapshot(t, dir, day("2026-04-11"), []SnapshotPlayer{
		{PlayerID: "p3", IsPitcher: true, ProjPtsPerGame: 7.5, GSSuppressed: true},
		{PlayerID: "p4", IsPitcher: true, ProjPtsPerGame: 4.0, GSSuppressed: true},
	})

	got := SummarizeGSGate(dir, []time.Time{day("2026-04-10"), day("2026-04-11")})

	if got.SuppressedStarts != 3 {
		t.Errorf("SuppressedStarts = %d, want 3", got.SuppressedStarts)
	}
	if got.SuppressedPts != 24.0 {
		t.Errorf("SuppressedPts = %v, want 24.0", got.SuppressedPts)
	}
	if got.Days != 2 || got.DaysWithSnapshot != 2 {
		t.Errorf("Days = %d, DaysWithSnapshot = %d, want 2 and 2", got.Days, got.DaysWithSnapshot)
	}
	if len(got.ByDate) != 2 {
		t.Fatalf("ByDate = %d entries, want 2", len(got.ByDate))
	}
	if got.ByDate[0].Starts != 1 || got.ByDate[0].Pts != 12.5 {
		t.Errorf("ByDate[0] = %+v, want 1 start / 12.5 pts", got.ByDate[0])
	}
}

// A missing day is not a zero-suppression day. DaysWithSnapshot is what makes a
// window thinned by failed runs visible instead of reading as a quiet week.
func TestSummarizeGSGate_MissingDayIsNotAZero(t *testing.T) {
	dir := t.TempDir()
	writeTestSnapshot(t, dir, day("2026-04-10"), []SnapshotPlayer{
		{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 12.5, GSSuppressed: true},
	})

	got := SummarizeGSGate(dir, []time.Time{day("2026-04-10"), day("2026-04-11")})

	if got.Days != 2 {
		t.Errorf("Days = %d, want 2 (the window asked for)", got.Days)
	}
	if got.DaysWithSnapshot != 1 {
		t.Errorf("DaysWithSnapshot = %d, want 1", got.DaysWithSnapshot)
	}
	if len(got.ByDate) != 1 {
		t.Errorf("ByDate = %d entries, want only the day that had a snapshot", len(got.ByDate))
	}
}

// Hitters never carry the flag, but guard against a future writer setting it.
func TestSummarizeGSGate_IgnoresHitters(t *testing.T) {
	dir := t.TempDir()
	snap := Snapshot{
		Date:        "2026-04-10",
		GeneratedAt: day("2026-04-10"),
		Hitters:     []SnapshotPlayer{{PlayerID: "h1", ProjPtsPerGame: 5, GSSuppressed: true}},
		Pitchers:    []SnapshotPlayer{{PlayerID: "p1", IsPitcher: true, ProjPtsPerGame: 8, GSSuppressed: true}},
	}
	b, _ := json.Marshal(snap)
	os.WriteFile(filepath.Join(dir, "2026-04-10.json"), b, 0o644)

	got := SummarizeGSGate(dir, []time.Time{day("2026-04-10")})

	if got.SuppressedStarts != 1 || got.SuppressedPts != 8 {
		t.Errorf("got %d starts / %v pts, want 1 / 8 (hitter ignored)", got.SuppressedStarts, got.SuppressedPts)
	}
}

func TestSummarizeGSGate_EmptyWindow(t *testing.T) {
	got := SummarizeGSGate(t.TempDir(), nil)
	if got.SuppressedStarts != 0 || got.Days != 0 || len(got.ByDate) != 0 {
		t.Errorf("empty window = %+v, want zero value", got)
	}
}
```

Before running, confirm the snapshot filename convention `<YYYY-MM-DD>.json` by reading `LoadSnapshot` at `internal/backtest/backtest.go:666`. If it differs, fix `writeTestSnapshot` to match — the test must write what `LoadSnapshot` reads.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/backtest/ -run TestSummarizeGSGate -v
```

Expected: FAIL to compile — `SnapshotPlayer.GSSuppressed` undefined, `SummarizeGSGate` undefined.

- [ ] **Step 3: Add the snapshot field**

In `internal/backtest/backtest.go`, add to `SnapshotPlayer` after `Locked` (line 115):

```go
	// GSSuppressed records that the weekly game-start gate declined this
	// pitcher's start on this date. Written from the gate's own report, not
	// inferred. Present only from 2026-08 forward; earlier snapshots read as
	// false, which is why GateSummary counts days with snapshots separately.
	GSSuppressed bool `json:"gs_suppressed,omitempty"`
```

- [ ] **Step 4: Populate it**

In `internal/lineuprun/snapshot.go`, before the pitcher loop (line 69), build the lookup:

```go
	gsSuppressed := make(map[string]bool, len(dr.pitcherResult.GateReport.Suppressed))
	for _, s := range dr.pitcherResult.GateReport.Suppressed {
		gsSuppressed[s.PlayerID] = true
	}
```

Then add to the `backtest.SnapshotPlayer{...}` literal inside that loop:

```go
			GSSuppressed:   gsSuppressed[sp.Player.ID],
```

- [ ] **Step 5: Write the summary implementation**

Create `internal/backtest/gs_gate_summary.go`:

```go
package backtest

import "time"

// GateSummary aggregates the weekly game-start gate's suppressions across a
// window of days.
//
// The gate runs for today's date only — future dates in a --dates range are
// optimized without it, since each day gets its own run — so a weekly figure
// has to accumulate across the daily projection snapshots.
type GateSummary struct {
	// Days is the size of the window asked for.
	Days int
	// DaysWithSnapshot is how many of those days actually had a snapshot on
	// disk. A missing day is not a zero-suppression day, and the difference is
	// what makes a window thinned by failed runs visible rather than reading as
	// a quiet week.
	DaysWithSnapshot int
	SuppressedStarts int
	// SuppressedPts is GROSS projected value declined, not a net weekly loss —
	// see optimizer.GSGateReport.SuppressedPts.
	SuppressedPts float64
	// ByDate holds only the days that had a snapshot AND at least one
	// suppression, in window order.
	ByDate []GateDay
}

// GateDay is one date's contribution to a GateSummary.
type GateDay struct {
	Date   time.Time
	Starts int
	Pts    float64
}

// SummarizeGSGate reads the projection snapshots for the given dates and totals
// the starts the GS gate declined. Days with no snapshot are counted in Days but
// not DaysWithSnapshot, and contribute nothing.
func SummarizeGSGate(dir string, dates []time.Time) GateSummary {
	sum := GateSummary{Days: len(dates)}
	for _, d := range dates {
		snap, ok := LoadSnapshot(dir, d)
		if !ok {
			continue
		}
		sum.DaysWithSnapshot++

		day := GateDay{Date: d}
		for _, p := range snap.Pitchers {
			if !p.GSSuppressed {
				continue
			}
			day.Starts++
			day.Pts += p.ProjPtsPerGame
		}
		if day.Starts == 0 {
			continue
		}
		sum.SuppressedStarts += day.Starts
		sum.SuppressedPts += day.Pts
		sum.ByDate = append(sum.ByDate, day)
	}
	return sum
}
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
go test ./internal/backtest/ -run TestSummarizeGSGate -v
```

Expected: all four PASS.

- [ ] **Step 7: Render the section in the backtest report**

Read `cmd/backtest.go:55-123` to see how the date window and snapshot dir are resolved. Add a call after the existing `fmt.Print(backtest.FormatReport(report))` on line 123:

```go
	if gate := backtest.SummarizeGSGate(snapshotDir, dates); gate.Days > 0 {
		fmt.Print(backtest.FormatGateSummary(gate))
	}
```

using whatever the local variables for the snapshot directory and the resolved date slice are actually named in that function — read it and use the real names rather than inventing them.

Add `FormatGateSummary` to `internal/backtest/gs_gate_summary.go`:

```go
// FormatGateSummary renders the GS-gate section of the backtest report.
func FormatGateSummary(s GateSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nGS GATE — starts the weekly game-start cap declined\n")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 52))
	if s.DaysWithSnapshot == 0 {
		fmt.Fprintf(&b, "No snapshots on disk for these %d days — nothing to report.\n", s.Days)
		return b.String()
	}
	fmt.Fprintf(&b, "%d start(s) suppressed over %d of %d days, %.1f gross projected pts.\n",
		s.SuppressedStarts, s.DaysWithSnapshot, s.Days, s.SuppressedPts)
	fmt.Fprintf(&b, "Gross, not a net loss: the budget went to a higher-ranked start.\n")
	fmt.Fprintf(&b, "A gate that fires often means the roster owns more SP than 6 P\n")
	fmt.Fprintf(&b, "slots and the weekly cap let it deploy.\n")
	for _, d := range s.ByDate {
		fmt.Fprintf(&b, "  %s  %d start(s)  %.1f pts\n", d.Date.Format("Mon Jan 2"), d.Starts, d.Pts)
	}
	return b.String()
}
```

Add `"fmt"` and `"strings"` to that file's imports.

- [ ] **Step 8: Verify the whole tree**

```bash
go build ./... && go vet ./... && go mod tidy && make test
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/backtest/ internal/lineuprun/snapshot.go cmd/backtest.go
git commit -m "feat(backtest): weekly rollup of GS-gate suppressed starts

The gate runs for today only, so a weekly figure has to accumulate across the
daily runs. The projection snapshot already persists per-pitcher per-day state
and already syncs to S3, so it is the accumulation point — no new store,
schedule or IAM.

GateSummary counts days-with-snapshot separately from days-in-window: a
missing day is not a zero-suppression day, and a week thinned by failed runs
must read as thin rather than as quiet.

Closes rosterbot-i1c."
```

---

## Task 9: Verify end to end, file the follow-up, close the issues

**Files:** none modified except `bd` state.

- [ ] **Step 1: Full build and test sweep**

```bash
go vet ./... && go mod tidy && make build-modules && make test
```

Expected: all PASS.

- [ ] **Step 2: Confirm the acceptance criterion for rosterbot-7i3**

```bash
grep -rn '185\.1\.0' --include='*.go' .
```

Expected: **no output**. The version exists only in the fork.

- [ ] **Step 3: Smoke test**

```bash
make run-all 2>&1 | tail -60
```

Expected: `version-check` reports OK, and no step regresses. This is slow — it hits live upstreams.

- [ ] **Step 4: Verify the idempotency invariant**

Part 2 touched the pitcher path, so CLAUDE.md requires this check:

```bash
go run . optimize --dry-run --dates $(date -u +%Y-%m-%d)
go run . optimize --dry-run --dates $(date -u +%Y-%m-%d)
```

Expected: the second run reports "No changes needed".

- [ ] **Step 5: File the deferred roster-shape follow-up**

```bash
bd create --title="Roster-shape line: hitter vs pitcher value against slot counts and the GS cap" \
  --type=feature --priority=3 \
  --description="Deferred from rosterbot-i1c, which shipped the measurement half (suppressed-start count and gross forgone projected points, daily on the board and weekly in the backtest report via backtest.SummarizeGSGate).

What remains is the analytical half: a line stating total projected hitter value across the 13 active hitter slots against total projected pitcher value across the 6 undifferentiated P slots, with the weekly GS cap alongside, so the structural imbalance is legible next to the measured suppression.

Deferred because comparing hitter value to pitcher value across differently-sized slot pools is a modelling question, not a plumbing one — a naive per-slot average would make any roster with more hitter slots look hitter-heavy by construction. Needs a defensible normalization before it says anything true." \
  --acceptance="A roster-shape line renders alongside the GS-gate summary, and its hitter-vs-pitcher comparison is normalized in a way that is documented and does not reduce to slot-count ratio."
```

Record the returned issue ID.

- [ ] **Step 6: Close both issues**

```bash
bd close rosterbot-7i3 --reason="Version pin collapsed to one site: gs_check.go now uses the fork's exported BuildFullRequest and ReadBody, so grep for the version literal in rosterbot returns nothing. Added version-check, a daily unauthenticated probe that exits non-zero on STALE_CLIENT and alerts via the existing run-ledger/opsalert path. Bundle-scrape auto-detection deliberately not built — validating the local pin has a yes/no answer and nothing to parse."
bd close rosterbot-i1c --reason="applyGSGate returns a GSGateReport naming each declined start; pitcherPipelinesFor consumes it instead of re-deriving suppression by inference (and its 0.10 multiplier is now the real NonStarterSPDiscount 0.05). Daily board shows today's count and gross forgone points; backtest report rolls the week up from a new gs_suppressed snapshot field. Roster-shape line deferred to its own issue."
```

- [ ] **Step 7: Push**

```bash
git pull --rebase && git push -u origin fix/rosterbot-7i3-i1c-version-pin-and-gs-gate-visibility && git status
```

Expected: `git status` shows the branch up to date with its remote. Then open a PR covering both issues.

**Operator note:** the `infra/` change requires `cdk deploy -c enableBuild=true` before `VersionCheck` actually schedules.

---

## Self-Review Notes

**Spec coverage:** Part 1 fork exports → Task 1; gs_check rewrite → Task 2; `CheckAPIVersion` + failure policy → Tasks 3-4; `run-all` line → Task 4 Step 5; EventBridge schedule → Task 5; acceptance grep → Task 2 Step 4 and Task 9 Step 2. Part 2 `GSGateReport` + gross-not-net doc → Task 6; inference removal + 0.05 fix + daily line → Task 7; snapshot field + `SummarizeGSGate` + report section → Task 8; deferred roster-shape issue → Task 9 Step 5; idempotency check → Task 9 Step 4.

**Known unknowns flagged inline rather than guessed:** the exact render-test filename (Task 7 Step 1), the snapshot filename convention (Task 8 Step 1), whether a `plural` helper exists (Task 7 Step 3), and the local variable names in `cmd/backtest.go` (Task 8 Step 7). Each has a command to resolve it before writing code.

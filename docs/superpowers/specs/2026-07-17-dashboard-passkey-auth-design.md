# Dashboard Passkey Auth — Design

**Date:** 2026-07-17
**Status:** Approved (pending spec review)

## Problem

The web dashboard shipped yesterday (#63) authenticates every request with a single shared secret:
`ROSTERBOT_API_TOKEN`, stored as an SSM SecureString and compared with `subtle.ConstantTimeCompare`
in `internal/lineupapi.Handler`. The browser side pastes that token into a login form, stores it
verbatim in `localStorage`, and sends it as `Authorization: Bearer <token>` on every call
(`web/dashboard/api.js`). That was a deliberate v1 simplification — the original design doc says
"No new auth infra (no Cognito, no IP allowlist): reuse the existing `ROSTERBOT_API_TOKEN`."

The gap: it's one long-lived secret that has to be typed/pasted and sits in `localStorage` in
plaintext, readable by anything that can run JS in that origin or read that browser profile. There's
no per-device binding and no revocation short of rotating the one shared token (which logs out every
device at once).

**Goal:** replace the shared-token login with WebAuthn passkeys as the real authentication
mechanism — device-bound credentials, biometric/PIN-gated, nothing long-lived for the browser to
store — while keeping the "no new auth infra" philosophy: no Cognito, no DynamoDB, no API Gateway.

## Non-goals

- Not building multi-user/multi-identity support. This is a private, single-operator console — one
  logical identity, multiple device-bound credentials (phone, laptop, maybe a hardware key).
- Not building username-less/discoverable "just tap your key" login UX beyond what resident keys give
  for free — the server always knows the one identity, so there's no account-discovery problem to
  solve.
- Not removing `ROSTERBOT_API_TOKEN` — it demotes from "the login" to a break-glass/CLI credential
  (see Rollout).
- Not adding per-session revocation infra. The chosen session model (signed stateless cookie) trades
  that away for zero new datastores; see Decisions.

## Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Scope of change | Full replacement of the token as the everyday login (not a convenience wrapper around it) |
| Identity model | One identity, multiple device-bound passkeys |
| Bootstrap (registering the first passkey) | One-time use of the existing `ROSTERBOT_API_TOKEN` to prove ownership, then it stops being the login path |
| Post-login session | Signed stateless HMAC cookie (no server-side session store); accepted trade-off: revoking one stolen device means rotating the secret and re-logging in everywhere, not a per-device kill switch |
| Server-side library | `github.com/go-webauthn/webauthn` (WebAuthn Level 3, actively maintained) |
| Credential storage | JSON blob in the existing state bucket via the existing `ObjectStore` pattern — no new AWS service |

## Architecture

### Backend (`internal/lineupapi`)

New endpoints, added to the existing `Handler` router:

| Route | Auth required | Purpose |
|---|---|---|
| `POST /v1/auth/login/begin` | none (this *is* login) | `wa.BeginLogin`; returns assertion options, sets a short-lived ceremony cookie |
| `POST /v1/auth/login/finish` | none | `wa.FinishLogin`; verifies assertion, updates sign counter, sets the session cookie |
| `POST /v1/auth/register/begin` | active session **or** the legacy bootstrap token | `wa.BeginRegistration` with `WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired)` so the credential is a true synced passkey, not a bare security key |
| `POST /v1/auth/register/finish` | same as begin | `wa.FinishRegistration`; appends the credential to the identity record |
| `GET /v1/auth/passkeys` | active session | list registered credentials (nickname, created, last-used) for a management view |
| `DELETE /v1/auth/passkeys/{id}` | active session | revoke one credential (e.g. a lost phone) |
| `POST /v1/auth/logout` | active session | clears the session cookie |

`authorized()` in `handler.go` is extended to accept **either** a valid signed session cookie **or**
the legacy bearer token (unchanged constant-time compare) — every existing `/v1/lineup/*`, `/v1/runs/*`,
`/v1/jobs/*` route keeps working under either credential with no route-level changes.

**Identity + credential storage:** a single JSON record

```go
type Identity struct {
    WebAuthnID  []byte              // stable random 64 bytes, generated once at bootstrap
    Credentials []webauthn.Credential
}
```

stored at key `webauthn/identity` through the same `ObjectStore` interface `lineupapi` already uses
for lineups/runs/notifications/output (`Get`/`Put`; `Put` is new on the interface, needed here since
this store is read-modify-written, unlike the append-only/producer-writes-once stores). Backed by
`s3lineup` (new small adapter, same package as the existing S3 stores) on AWS and a file-backed store
(mirroring `NewFileStore`) for local `serve`.

**Session cookie:** after `FinishLogin` succeeds, the handler mints
`base64url(payload) + "." + base64url(HMAC-SHA256(payload, secret))` where `payload` is a small JSON
struct `{IssuedAt, ExpiresAt}` (no per-session ID — nothing to look up, which is what makes this
stateless). Set as `rosterbot_session`, `httpOnly`, `Secure`, `SameSite=Strict`, 30-day expiry. The
HMAC secret is a new SSM SecureString, `/rosterbot/DASHBOARD_SESSION_SECRET`, fetched once at cold
start next to the existing token fetch in `lambda/main.go`.

**Ceremony cookie:** `wa.BeginRegistration`/`wa.BeginLogin` both return a `webauthn.SessionData` that
must survive until the matching `Finish*` call. Per the library's documented pattern, it's marshaled
to JSON and set as its own short-lived (5 min) `httpOnly`/`Secure` cookie (`rosterbot_ceremony`) —
not the long-lived session cookie, and not a new datastore.

**Sign counter / clone detection:** `FinishLogin` returns a `CloneWarning` flag and an updated sign
count on the credential; the handler writes the updated `Identity` back to the store after every
successful login. (Note: many platform authenticators, e.g. Touch ID, always report `SignCount == 0`
and never trigger this — it's mainly relevant for roaming/hardware keys.)

### Frontend (`web/dashboard/`)

- **`webauthn.js`** (new) — ArrayBuffer⇄base64url conversion helpers (WebAuthn's binary fields don't
  survive `JSON.stringify` directly) and the two ceremony flows, wrapping
  `navigator.credentials.create()` / `.get()`.
- **`app.js`** — the token-paste login form is replaced by a "Sign in with passkey" button. A
  first-run "set up your first passkey" screen (paste the bootstrap token once) is shown only when
  `GET /v1/auth/passkeys` comes back empty/401-without-cookie.
- **New "Passkeys" panel** — lists registered devices via `GET /v1/auth/passkeys`, with "add another
  passkey" (calls register begin/finish while already logged in) and per-device revoke buttons.
- **`api.js`** — drops `getToken`/`setToken`/`clearToken`/the `Authorization` header entirely; `fetch`
  calls add `credentials: "same-origin"` so the session cookie rides along automatically. `isLoggedIn`
  keeps its existing shape (a cheap authenticated GET, catch 401) — the mechanism under it changes,
  the call site in `app.js` doesn't.

### Infra (`infra/infra.go`)

- One new SSM SecureString parameter, `/rosterbot/DASHBOARD_SESSION_SECRET`, bootstrapped manually
  the same way `ROSTERBOT_API_TOKEN` was (documented in `docs/aws-deployment.md`).
- Lambda IAM: extend the existing `ssm:GetParameter` statement to include the new parameter ARN.
- No new bucket/prefix grant needed — the Lambda already has read/write on the state bucket; the new
  `webauthn/` key just rides the existing grant.
- No new compute, no new managed auth service. Same shape as the original dashboard PR, just a
  different SSM param and a new S3 key.

### Local dev (`serve --web`)

`RPID = "localhost"`, `RPOrigins = ["http://localhost:8080"]`. WebAuthn explicitly treats `localhost`
as a secure context, so Touch ID / Windows Hello work against a plain local HTTP server — real
end-to-end manual testing, no virtual-authenticator mocking required. The session secret is read from
a new `ROSTERBOT_SESSION_SECRET` env var (required, same fail-fast style as the existing
`ROSTERBOT_API_TOKEN` check in `runServe`).

## Data flow

**Bootstrap (one time):** open the dashboard → no credentials exist → "set up your first passkey"
screen → paste `ROSTERBOT_API_TOKEN` → `register/begin` (token accepted as the auth gate) →
`register/finish` → platform authenticator prompts biometric/PIN, creates a resident credential →
appended to `webauthn/identity`. Repeat on other devices, but now gated by an already-valid session
cookie instead of the token.

**Everyday login:** "Sign in with passkey" → `login/begin` returns `allowCredentials` = your stored
credential IDs → browser's platform authenticator prompts biometric/PIN → `login/finish` verifies the
assertion, updates the sign counter, sets the session cookie. Every subsequent `/v1/*` call carries
the cookie automatically.

## Error handling

- `authorized()` rejects if neither the session cookie nor the legacy token verifies — same 401 shape
  the dashboard already special-cases (`clearToken()` on the old client; the new client just re-shows
  the login screen).
- A missing/expired/tampered ceremony cookie on `Finish*` fails the ceremony with a clear "session
  expired, try again" error rather than a generic 500 — WebAuthn ceremonies are short (~30s of user
  interaction), so a 5-minute cookie expiry is generous, not a real UX hit.
- `register/begin`+`finish` reject outright (403) if called with neither a valid session nor the
  bootstrap token — prevents an attacker who doesn't have the token from enrolling their own device
  even if they can reach the endpoint.
- Session-secret rotation (manual, e.g. if a device is lost and per-device revocation isn't available)
  invalidates every outstanding session cookie at once — documented as the accepted recovery path in
  `docs/aws-deployment.md`, consistent with the Decisions table trade-off.

## Testing

- **Handler-level unit tests** (`internal/lineupapi`, following the existing `serve_test.go`/handler
  test style): cookie parsing/signing/expiry logic, `authorized()`'s dual-credential branch, the
  register-gate logic (session-or-token, reject neither), store read/write for `Identity`. The
  cryptographic WebAuthn ceremony itself is exercised through the library's own test suite, not
  re-verified here.
- **End-to-end manual verification** via `serve --web` with a real platform authenticator (Touch ID),
  per CLAUDE.md's UI-change testing rule: bootstrap a passkey, log out, log back in, add a second
  simulated "device" (a second browser profile), revoke it, confirm the revoked credential can no
  longer log in.
- **Idempotency check n/a** — this is a stateful auth flow, not an optimizer pass; the closest
  equivalent is confirming a second `login/begin`+`finish` cycle on an already-registered credential
  behaves identically (no double-registration side effects).

## Rollout

1. Ship the new endpoints, `webauthn.js`, and the Identity store (S3 + local file) with the **old
   token-paste login screen left in place** — dual-running, nothing removed yet.
2. Register real passkeys (phone + laptop) against the deployed dashboard; confirm login end-to-end
   in production.
3. Remove the token-paste login screen from `app.js`/`index.html`. `ROSTERBOT_API_TOKEN` stays wired
   into `authorized()` and `register/begin` (break-glass + recovery-enrollment path) but is no longer
   surfaced anywhere in the normal UI. Recovery needs no separate screen: the "set up your first
   passkey" screen's show condition (`GET /v1/auth/passkeys` comes back empty) reappears on its own
   if every passkey is ever deleted/lost, so the token-entry path is still reachable without restoring
   any removed UI.
4. Update `docs/aws-deployment.md` and the `serve` command's `--help` text to describe the passkey
   flow and the token's demoted break-glass role, per CLAUDE.md's doc-sync rule for new
   commands/flags/auth behavior.

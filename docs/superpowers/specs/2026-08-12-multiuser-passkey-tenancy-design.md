# Multiuser tenancy with passkey auth — design

Date: 2026-08-12
Status: Draft (design settled by interview; not yet decomposed into issues)
Supersedes nothing. Extends `2026-07-17-dashboard-passkey-auth-design.md`, which
built the single-operator identity this replaces.

## Problem

rosterbot is single-tenant by construction. One Fantrax account
(`FANTRAX_USERNAME`/`FANTRAX_PASSWORD`), one team (`FANTRAX_TEAM_ID`), one
WebAuthn identity, one set of S3 prefixes, fifteen singleton EventBridge
schedules. The goal is for other managers in the league to sign in with their
own passkey and have the bot manage their team.

## What the codebase already decides for you

Four findings from reading the tree that constrain the design more than any
preference does.

**Passkey auth exists and is a singleton by construction.** `identity.go`
states it: *"There is exactly one Identity for the whole dashboard — one
operator, multiple device-bound credentials."* `GetIdentity(ctx)` takes no key,
and the session cookie carries only an expiry (`session.go`, `sessionPayload{ExpiresAt}`).
There is no notion of *whose* anywhere in the system. The ceremony plumbing,
however, is sound and reusable — including `ResidentKeyRequirementRequired`,
which matters below.

**Fantrax auth reduces to one long-lived bearer cookie.** `auth_client`'s
`relevantCookies` is a one-element map: `FX_RM`. Measured from the live cookie
cache: `httpOnly`, `secure`, `SameSite=Lax`, 54 bytes, `.fantrax.com`, declared
expiry ~1 year. Being `httpOnly` rules out a bookmarklet; only devtools, a
browser extension, or a real login can obtain it.

**Two tenants cannot share a process.** `auth_client.Client` is
`{http.Client, LeagueID, UseCache}` — no credential field. Every request calls
the package-level `GetCookies()`, which reads a process-global env var or a
package-**const** cache file path. This forces process-per-tenant, and it makes
the `session/` prefix the sharpest artifact in the layout: if two tenants ever
share it, tenant B authenticates *as* tenant A. That is a wrong-roster-write
bug, not a data-leak bug.

**The write path has an actuarial record.** CLAUDE.md documents, in the apply
path specifically: `rosterbot-z3b` (11 days of duplicate applies and a
notification flood), `rosterbot-sza` (cache invalidation silently no-op for four
days), `rosterbot-48z` (locked-player retry masking a no-op as success),
`rosterbot-uv6`/`wd5` (period-axis collapse producing a league-wide false
alert). Each ran for days before detection. Nothing about the code gets worse
under multiuser; the *consequence function* does.

### The reassuring finding

None of the users are commissioners, so `adminMode` is always `false`, and
Fantrax itself rejects any attempt by one session to edit another team's roster
(`edit_roster.go:72`: *"adminMode: true = commissioner editing another team,
false = user editing own team"*). **Fantrax is the enforcement boundary.** The
worst a tenant-scoping bug can do on the write path is get rejected upstream.

## Decisions

| # | Decision | Rationale |
|---|---|---|
| 1 | Full SaaS: bot optimises **and applies** each user's lineup | Product intent |
| 2 | Per-user Fantrax credentials (no commissioner path) | User is not commissioner |
| 3 | Onboarding via **connect form** — user types Fantrax creds on our page | Best UX; devtools paste retained as a seam |
| 4 | **Store the password**, not only `FX_RM` | Buys unattended refresh. ⚠️ Original rationale (weekly re-login) was **measured false** — see "Real Fantrax session lifetime". Re-entry is ~annual |
| 5 | **DynamoDB**, single table, for identity/tenancy | Small, hot, mutable, contended — the inverse of the Cache ADR-0001 governs. Needs conditional writes |
| 6 | **KMS envelope, split roles**: API Lambda `kms:Encrypt` only; ECS task `kms:Decrypt` only. Per-user `EncryptionContext{user_id}` | Public Function URL (`AuthType: NONE`) must not be able to read passwords. CloudTrail then audits every decrypt by user |
| 7 | RP ID stays `*.cloudfront.net` for now | Accepted risk; see Risks |
| 8 | **Admin-minted, single-use, short-TTL enrollment links** — one primitive serving both invite and recovery | No email infra; out-of-band delivery to people you know |
| 9 | **One ECS task per user per job** | Forced by the process-global cookie; also isolates failure. ~$8.60/mo Fargate at 12 tenants (see Cost model) |
| 10 | S3 tenancy as a **Hive segment**: `<prefix>/user=<uid>/…` | `s3blob` keys are prefix-relative, so `statestore` composes the prefix and no key parser changes. Keeps Athena working by adding `user` to partition projection |
| 11 | **Propose-only by default**; auto-apply is per-tenant opt-in | Removes the write-path bug class for anyone who hasn't consented to it |
| 12 | **Strict cross-tenant isolation** — own data only; existing public league views stay public | Tenants are competitors; leaked trade valuations change how the league plays |
| 13 | **Admin (you) can read everything, disclosed at signup** | Pilot cohort is external; blind debugging against the z3b-class bug tail is not viable |
| 14 | **In-app feed only, no push** for v1 | MVP scope |
| 15 | Email stored, unique, **admin-attested and Fantrax-corroborated** (no SES, no challenge) | Verification-by-challenge needs a domain that was declined; out-of-band invite delivery attests identity, and `UserInfo.Email` corroborates it at connect time |
| 16 | Per-tenant ops keying + failure taxonomy + admin Tenants tab | See "Alerting inverts under fan-out" |
| 17 | Credential ladder, **fail-safe, no blind retry** | A retry loop on a bad password locks the user out of their own Fantrax account |
| 18 | **Refactor to N=1 first**, gated on byte-identical `--dry-run` output; then connect, fan-out, pilot | The diff oracle exists only while there is one tenant |
| 19 | **One team per user; one league** (the configured `FANTRAX_LEAGUE_ID`) | Pilot scope |
| 20 | **Invites are minted against a specific team**, and connect must *prove* ownership via `MyTeamIDs` | Closes the claim loop from both ends; see "Team binding" |

## Alerting inverts under fan-out (must land in the same commit)

`internal/opsalert` keys both of its functions on `r.Command` alone:

- `Streak(recs, command)` filters `r.Command != command` (`streak.go:105`) and
  walks newest-first, where a SUCCESS breaks the streak. Under fan-out, tenant B
  failing *every hour* returns `None`, because eleven other tenants' successes
  are interleaved in the same command's records.
- `Overdue` keeps `newest[r.Command]` (`heartbeat.go:115`), so "Lineup ran
  recently" is true if **any** tenant ran. A tenant whose task never launches is
  invisible permanently.

`Overdue` exists precisely because of rosterbot-ys8 — *"a job that never
launches… produces perfect silence, which is indistinguishable from perfect
health."* Fan-out reintroduces that blindness one level down, invisibly: the
code compiles, runs, and reports green. Re-key both to `(command, user_id)`.

## Schema

DynamoDB, single table, `PK`/`SK`.

```
USER#<uid>          PROFILE            display_name, email, email_attested_by,
                                       email_attested_at, role(admin|member),
                                       status, auto_apply=false, ring,
                                       token_version, created_at
USER#<uid>          CRED#<cred_id>     public_key, sign_count, aaguid,
                                       transports, nickname, last_used_at
USER#<uid>          FANTRAX            creds_ct(KMS), fx_rm_ct(KMS), team_id,
                                       fantrax_user_id, conn_status,
                                       last_verified_at, last_error_class
CRED#<cred_id>      USER               uid           ← non-discoverable lookup
EMAIL#<lower>       USER               uid           ← uniqueness (cond. write)
TEAM#<team_id>      USER               uid           ← one claimant per team
ENROLL#<sha256>     TOKEN              uid, team_id, email, expires_at (TTL), used_at
```

**No `league_id` column.** One league means the league is the deployment's
`FANTRAX_LEAGUE_ID`, and a per-user copy of a value that is constant by
definition only creates a way for them to disagree.

**`FANTRAX` is a singleton item per user**, not `FANTRAX#<team_id>` — one team
per user, so the sort key carries no information.

**`TEAM#<team_id>` is a uniqueness constraint, not a convenience.** Without it,
two users can claim the same team, and both tenants' hourly jobs then optimise
and apply to the same roster — fighting each other every hour, each seeing the
other's changes as drift to correct. Enforce with a conditional write on claim.

**`uid` *is* the WebAuthn user handle** — the 64 random bytes
`newWebAuthnUserID()` already mints. Because the code requires discoverable
credentials, the authenticator returns that handle at login, so "who is this?"
is a direct key-get with **no index**. `CRED#` exists only for the
non-discoverable fallback.

**`ListActiveTenants` for the cron fan-out is a `Scan`**, deliberately. At this
scale one Scan is a single request and cheaper than maintaining a GSI. Revisit
at four digits.

**Uniqueness and lost updates** use conditional writes. Note the latent bug this
fixes: `handleAuthRegisterFinish` does `GetIdentity → append → PutIdentity` with
no compare-and-set, and login read-modify-writes the same record for the sign
counter. With one operator that is benign; with N users on multiple devices a
concurrent login and registration **silently loses a passkey**.

## Connect flow

The split-role IAM decomposes the flow along the existing plumbing:

1. Browser POSTs Fantrax credentials to the API Lambda (passkey session required).
2. Lambda encrypts under the CMK with `EncryptionContext{user_id}`, writes
   ciphertext to Dynamo, sets `conn_status=pending`, launches a `connect` ECS task.
3. Task decrypts (only role that can), drives chromedp, captures `FX_RM`, writes
   it back encrypted, sets `conn_status=verified|failed`.
4. Browser polls `GET /v1/runs/{id}/progress`, which already exists.

The password never enters an ECS task override (which CloudTrail logs), never
enters CloudFront access logs (POST bodies are not logged), and KMS records the
encryption context rather than the plaintext. Request-body logging must stay off.

### Team binding — both ends must agree

An invite is minted **against a specific team**: `ENROLL#` carries `team_id` and
`email`. The `connect` task then proves the other end, using data Fantrax
already returns:

- `Login()` populates `UserInfo` with `UserID`, `Email` and `VerificationStatus`
  (`models/user_info.go`).
- `TeamRosterResponse.MyTeamIDs []string` (`models/team_roster_response.go:29`)
  lists the teams the authenticated session owns.

Assertions at connect, all failing closed:

1. `invite.team_id ∈ MyTeamIDs` — this account genuinely owns the invited team.
2. `TEAM#<team_id>` unclaimed, or claimed by this same `uid` — conditional write.
3. `UserInfo.Email == invite.email` — corroborates the admin attestation.
   Mismatch is a warning surfaced to the admin, not a hard failure: a manager may
   legitimately use a different address with Fantrax than the one you invited.

This is what makes decision 15 defensible. Admin attestation alone proves only
that *you believe* Dave owns team 7. Assertion 1 is Fantrax stating that the
credentials presented control team 7, and assertion 3 is Fantrax stating which
address that account registered. Neither is a challenge-response, but together
they are a stronger claim than a click-through link, which proves mailbox
control and nothing about team ownership at all.

## Credential ladder

```
FX_RM → 401 → re-login with stored creds → failure →
    conn_status = needs_reconnect
    STOP all applies for that tenant       (fail safe, not fail open)
    in-app banner + admin alert
    DO NOT retry until the user reconnects
```

The no-retry rule is the load-bearing part. `FX_RM` rejected means the session
aged out and re-login is correct. Re-login rejected means the password is wrong,
and retrying is actively harmful — repeated failed logins are what trigger
account lockout and Cloudflare bot-blocking, so an hourly retry across 14
runs/day would lock a pilot out of their own Fantrax account using credentials
they handed you.

## Job taxonomy

Fan-out applies to roughly half the schedules. The expensive, rate-limited
upstreams are all on the singleton side, so fan-out multiplies cheap work only —
and avoids becoming 12× the load on FanGraphs, which would get an IP blocked and
take down every tenant at once.

| Per-tenant | Singleton |
|---|---|
| `Lineup` (hourly, ×14/day) | `VersionCheck`, `GsCheck`, `Waivers` |
| `Grade`, `Backtest`, `Shadow` | `Transactions`, `Claims`, `Recap` |
| `Prospects`, `ProjectionSite` | `Archive`, `TeamValues` |

## Data exposure to fix

Per CLAUDE.md, `report/*` is **world-readable** through CloudFront's default
behavior — that is why the Trades tab was deliberately placed on `/v1/*`. Once
`model.json` and `gap.json` become per-tenant, publishing them means publishing
each manager's projection grades and *how many points they leave on their
bench*, to the open internet. Those must move behind `/v1/*` as tenant-scoped
endpoints. `value.json` is legitimately league-wide and stays.

## Sequencing

Ordered so that the correctness oracle is used before it expires.

- **Phase 0 — latent bugs, fixable today at N=1.** Re-key `opsalert`; add
  optimistic concurrency to identity writes; move per-tenant artifacts off the
  public `report/` path.
- **Phase 1 — identity substrate at N=1.** Dynamo table, CMK, split IAM.
  Migrate the singleton `Identity` to `USER#<uid>` with yourself as tenant 1.
  Session cookie gains `sub` + `token_version`. Enrollment-link primitive.
  Route authz: tenant-scoped vs admin-only.
- **Phase 2 — tenant state at N=1.** `layout.Artifact.PerTenant`, prefix
  composition in `statestore`, backfill existing state to `user=<jon>/`, Athena
  partition projection gains `user`.
- **Phase 3 — Fantrax connection.** Connect form, ECS `connect` task, ladder,
  per-tenant session isolation.
- **Phase 4 — fan-out.** Cron Lambda enumerates tenants; per-tenant ledger,
  progress, notifications; propose-only default.
- **Phase 5 — pilot ops.** Admin Tenants tab, failure taxonomy routing, signup
  disclosure.

**Gate on phases 1–2:** build a binary from the last good commit into the
scratchpad and diff `--dry-run` output for `--pipeline`, `--matchup` and an
explicit `--dates`, per CLAUDE.md's `internal/lineuprun` verification note —
identical stream redirection on both, run back-to-back. A correct tenancy
refactor is byte-identical for a single tenant. This oracle does not exist once
a second tenant is onboarded.

## Risks accepted

**RP ID on `*.cloudfront.net`.** A passkey is bound to its RP ID forever; there
is no migration. If the `DashboardCdn` distribution is ever *replaced* rather
than updated — and this stack already has documented circular-dependency
contortions around exactly that distribution — the domain changes and every
enrolled passkey dies at once, with the admin enrollment link as the only
recovery. `cloudfront.net` is also on the Public Suffix List, so the RP ID must
be the full host and `app.`/`api.` splits are permanently foreclosed.
*Mitigations:* treat the `DashboardCdn` logical ID as load-bearing
infrastructure, and alarm on any change to the SSM `DASHBOARD_RP_ID` value —
that change *is* the mass-invalidation event, and otherwise you learn about it
from twelve people who cannot log in.

**Password custody.** Stored credentials rescue the *expiry* case only. A user
who changes their Fantrax password invalidates `FX_RM` **and** your stored copy
simultaneously; both paths require a reconnect. What custody buys is unattended
refresh; what it costs is that a breach of the Dynamo table plus the CMK yields
live third-party account credentials. The split-role IAM is the control that
makes the public API surface not part of that blast radius.

**Fantrax Terms of Service.** Storing and using another person's credentials to
act on their behalf is very likely contrary to Fantrax's ToS regardless of
consent. Not evaluated here; worth reading before onboarding pilots.

## Scope: pilot

One team per user, one league, a small cohort of known managers, propose-only by
default. Two consequences worth stating so they are not mistaken for oversights:
`FANTRAX_IL_SLOTS` / `FANTRAX_MINORS_SLOTS` / scoring weights / slot config stay
**deployment-level constants**, not per-tenant config; and there is no
self-service signup, no team switcher, and no league picker. Each of those is a
deliberate deferral that becomes real work if the pilot expands to other leagues.

## Measured: real Fantrax session lifetime

The `STATE_BUCKET` has versioning enabled, which makes the re-login history
directly observable:

```
1201 versions of session/.fantrax_cookie_cache.json
      1 distinct ETag              ← content has never changed
first seen 2026-06-16T03:17:24Z    (cutover to Fargate)
measured   2026-08-12              (57 days, 1201 syncs, 0 re-mints)
FX_RM declared expiry 2027-07-21   (343 days out)
```

**The chromedp login has run exactly once, ever.** The re-login path is not a
weekly event; on this evidence it is roughly annual.

This retires the argument originally recorded for decision 4. `layout.Session`'s
`MaxAge: 7 * Day` was cited as evidence that sessions churn weekly — it is not;
it is a staleness check on the *artifact sync*, which happens every task run.
Decision 4 (store the password) therefore rests only on avoiding a **once-a-year**
re-entry, weighed against holding recoverable third-party credentials. Left as
decided, but the premise is now measured rather than assumed, and it is a
reasonable thing to revisit before Phase 3.

Method is reproducible for any artifact: `aws s3api list-object-versions`, group
by ETag, take the earliest occurrence of the current one.

## Resolved: session revocation

Sessions stay stateless HMAC, with the payload gaining `sub` (the uid) and `ver`
(an integer), verified against `USER#<uid>.token_version`. Revoking every device
for a user is an increment of that field; `SessionSecret` rotation remains the
nuclear option that logs out everyone.

The Dynamo read this implies is **not** additional cost on most routes — every
tenant-scoped handler must load the user record anyway to scope its query. It is
genuinely additional only on passthrough routes (`/v1/trades`), where it is one
`GetItem` against a table with a dozen items. Caching it is premature.

## Resolved: rate limiting

Two distinct exposures, and the second is the dangerous one.

**Unauthenticated:** the whole `/v1/auth/` prefix bypasses `isAuthed`
(`handler.go:91-94`) and sits behind a Function URL with `AuthType: NONE`.
Per-IP token bucket in the Dynamo table already being added, with a TTL
attribute. Roughly thirty lines and no new spend. AWS WAF would be less code but
costs ~$6-8/mo — comparable to the entire Fargate bill for this pilot, which is
poor value at twelve users.

**Authenticated:** the connect endpoint is the *other* path to Fantrax account
lockout, and unlike the cron path it is user-triggered. It needs a hard per-user
cap (5 attempts/hour) for exactly the reason decision 17 forbids blind retry —
repeated failed logins are what trigger lockout and Cloudflare bot-blocking.
Both limiters exist to protect the user's Fantrax account, not our infrastructure.

## Resolved: cost model

Task is 1 vCPU / 2 GB ARM64 (`infra.go:137-140`). Per-tenant per-day: Lineup 14,
Grade 1, Shadow 1, Prospects 1, ProjectionSite 1, Backtest ~0.14 → ~18 runs.
At ~$0.0395/task-hour (Graviton) and ~2 min average duration:

| Item | Monthly, 12 tenants |
|---|---|
| Fargate (~18 runs/day/tenant × 2 min) | ~$8.60 |
| DynamoDB on-demand + storage | ~$1 |
| KMS (1 CMK + requests) | ~$1 |
| S3 (extra per-tenant prefixes) | ~$1-2 |
| **Total** | **~$12/mo** |

Two notes. The hourly Lineup job is **77% of the task count**, so it dominates
any optimisation. And the 2 GB allocation exists for chromedp — which the
measurement above shows runs about once a year. Steady-state tasks never launch
Chrome, so a smaller task definition for non-connect work (0.5 vCPU / 1 GB)
roughly halves the bill; the `connect` task keeps the large size.

Inactive tenants should be excluded from fan-out by `status`, not merely by a
failed run, so a parked account costs nothing.

## Closed: Fantrax 2FA

Not investigated, by decision. Not resolvable from public sources anyway —
Fantrax's help site returns 403 to automated fetches and web search has no
coverage. Two weak indications it is at least not mandatory: `models.UserInfo`
carries no 2FA field (`VerificationStatus`/`VerificationRequired` are *email*
verification), and `GetCookiesWithBrowser` does a straight form login with a
60-second timeout and no branch for any challenge, which has run unattended for
57 days.

The design does not depend on the answer. The connect task emits a distinct
`login_challenge_or_timeout` error class whenever the flow completes without
yielding `FX_RM`, and the devtools-paste path remains a seam even though it is
not surfaced in the UI. A 2FA-enabled pilot therefore shows up as a named,
diagnosable onboarding failure with a manual workaround, rather than a mystery.

## Tracking

Epic `rosterbot-crq`. Phase 0 filed as `rosterbot-crq.1` (opsalert re-keying),
`.2` (identity lost update), `.3` (report/ exposure) — all three are real bugs at
N=1 and are independent of the rest of the epic.

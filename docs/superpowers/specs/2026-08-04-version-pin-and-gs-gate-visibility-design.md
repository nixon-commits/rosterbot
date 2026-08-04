# Single Fantrax version pin + GS-gate visibility

**Date:** 2026-08-04
**Issues:** rosterbot-7i3 (Fantrax API version has two pin sites and no detection), rosterbot-i1c (Surface GS-gate suppressed starts)

Two independent changes, batched into one branch. They share no code. If the
cross-repo half of 7i3 stalls, i1c can be split out unchanged.

---

## Part 1 — rosterbot-7i3: one pin, plus a daily assertion that it still holds

### Problem

The Fantrax `/fxpa/req` client version is pinned in two places that must move
together, with nothing enforcing agreement:

1. fork `github.com/nixon-commits/go-fantrax`, `auth_client/fantrax_client.go`
   — `const fantraxAPIVersion` (unexported)
2. rosterbot `internal/fantrax/gs_check.go` — the one envelope we build
   ourselves rather than through the library

When only the fork moves, `GetScoringPeriodsAndTeams` alone starts returning
`STALE_CLIENT`, breaking `gs-check`, `recap` and `team-values` while `optimize`
keeps working — a confusing partial outage. Two full outages so far: 2026-07-01
(12 failed runs) and 2026-08-01..08-03 (53+ failed runs).

### Findings that shaped the design

**The two sites do not differ for any reason.** The fork already has
`buildFullRequest(msgs, refUrl)` (`auth_client/fantrax_client.go:62`),
unexported. `gs_check.go`'s hand-built map is byte-identical to what it returns
— same seven keys, same values. The issue's design note guessed the wrapper
"likely just needs a msgs shape the wrapper didn't expose"; it does not.
`gs_check.go` already builds its `msgs` as `[]auth_client.FantraxMessage`. The
envelope was hand-rolled only because the helper is lowercase.

**There is a second, unlogged copy of the same mistake.** `gs_check.go:83` calls
`io.ReadAll` directly, bypassing the fork's `readBody`, which is the function
that inspects the embedded `pageError`. That is why the August outage surfaced
from this call as `no response data in standings` rather than `STALE_CLIENT`.
Half of what made the partial outage confusing was the wrong error message, not
only the split pin.

**Detection is much cheaper than the issue assumed.** The issue's option B
proposed scraping `https://www.fantrax.com/` → `main-<HASH>.js` → its 151
`chunk-*.js` files → regex for `{name:"fantrax",version:"N.N.N"}`, and worried
that a wrong auto-detected value would be harder to debug than a stale
constant. That risk belongs to *discovering the replacement value*. Asking
Fantrax whether the current pin still passes is a different question with a
yes/no answer and no value to get wrong. Verified live on 2026-08-04, one
unauthenticated POST, both directions:

| pinned `v` | `pageError.code` |
|---|---|
| `185.1.0` (current) | `WARNING_NOT_LOGGED_IN` |
| `181.0.0` (known stale) | `STALE_CLIENT` |

The version gate is checked ahead of auth, which is what makes the
unauthenticated probe a clean discriminator. The 151-chunk scrape stays a human
recovery step, documented in the fork's existing runbook comment; it is not
built.

### Fork changes (`github.com/nixon-commits/go-fantrax`)

Three exports, no behavior change. Local checkout at `/Users/jnixon/go-fantrax`,
remote `fork`.

| change | why |
|---|---|
| `fantraxAPIVersion` → `APIVersion` (exported) | the probe must read the pin it validates |
| `buildFullRequest` → `BuildFullRequest` | gs_check stops hand-rolling an identical envelope |
| `readBody` → `ReadBody` | gs_check gets the `pageError` detection it currently bypasses |

The existing runbook doc comment on the constant (how to find the new value,
how to confirm it before shipping) moves with it unchanged — it remains the
recovery procedure.

Six internal call sites reference `fantraxAPIVersion` (`fantrax_client.go`,
`get_player_pool.go`, `get_team_service_time.go`, `get_team_roster.go`,
`get_transactions.go` ×2, `edit_roster.go`) and update to the new name.

Then tag, push to `fork`, and bump the `replace` pseudo-version in rosterbot's
`go.mod`.

### rosterbot changes

**`internal/fantrax/gs_check.go`** — envelope becomes
`auth_client.BuildFullRequest(msgs, refURL)`; `io.ReadAll` becomes
`auth_client.ReadBody(resp)`. Drops roughly 20 lines and the version literal. A
future `STALE_CLIENT` from this call reports itself by name.

**New `internal/fantrax/version_check.go`**

```go
type VersionStatus int

const (
    VersionOK VersionStatus = iota
    VersionStale
    VersionUnknown
)

// CheckAPIVersion asks Fantrax whether the pinned client version still passes
// its server-side gate, without authenticating.
func CheckAPIVersion(ctx context.Context) (VersionStatus, string, error)
```

POSTs `getUserInfo` with `auth_client.APIVersion` and reads `pageError.code`:

- `STALE_CLIENT` → `VersionStale`
- `WARNING_NOT_LOGGED_IN`, or no `pageError` → `VersionOK`
- anything else → `VersionUnknown`, carrying the observed code in the returned
  string

The endpoint URL is a package `var` for test override, matching the existing
`standingsURL` / `schedule` package convention.

**New `cmd/version_check.go`** — `rosterbot version-check`. The failure policy
is stated once, and is the load-bearing part:

| probe result | exit | reasoning |
|---|---|---|
| `STALE_CLIENT` | **1** | the pin is genuinely dead; page immediately |
| `WARNING_NOT_LOGGED_IN` / no error | 0 | pin passed the gate |
| unrecognized code, network error, non-200 | **0** + warning to stderr | a Fantrax outage already fails every other job and pages through them; the probe must not raise a second alert for a cause it did not diagnose |

The `VersionUnknown` → exit 0 rule is what keeps this from becoming a noise
source. The probe is only allowed to page for the one condition it can
positively identify.

**Alerting rides the existing path — no new notification code.** `entrypoint.sh`
captures the real exit code through the pipe, writes a `FAILED` run-ledger
record with `--log-file /tmp/rosterbot.log`, then exits `rc`. `opsalert.Streak`
returns `Started` on the first failure after a success, and the message body
quotes the last non-empty line of `log_tail`. So printing the diagnosis as the
final line puts `STALE_CLIENT: pinned 185.1.0 rejected by Fantrax` directly in
the Pushover. `Recovered` fires automatically once the pin is fixed.

**`infra/infra.go`** — add to the schedule table (~line 681):

```go
{"VersionCheck", "cron(30 10 * * ? *)", jsii.Strings("version-check"), dailyGap},
```

10:30 UTC sits ahead of the first daily job (Prospects, 11:00) and well ahead of
the hourly Lineup window (14:00–03:00), so a bad pin is known before the day's
cascade. The table feeds `JOB_SCHEDULES`, so heartbeat coverage
(`opsalert.Overdue`) comes along for free. Moving to 6-hourly later is a
one-line change.

**`Makefile`** — append `version-check` to the `run-all` recipe, per the
standing rule for new top-level commands.

### Testing

`internal/fantrax/version_check_test.go` drives an `httptest` server through the
override `var`, using the two real response bodies captured on 2026-08-04 as
fixtures, plus an unrecognized-code case and a network-error case. Assert the
status mapping, not the exit code — the exit mapping is asserted separately in
`cmd`.

### Acceptance criterion

`grep -rn '185\.1\.0'` across rosterbot returns nothing. The version appears
exactly once in source, in the fork.

---

## Part 2 — rosterbot-i1c: the gate reports; nothing re-derives it

### Problem

`applyGSGate` (`internal/optimizer/gs_budget.go:78`) knows exactly which
starters it flipped to `IsStarter=false`, and discards it. The league config
makes surplus SP structurally hard to deploy — 13 active hitter slots against 6
undifferentiated P slots plus a weekly game-start cap — so the gate firing
regularly *is* the signal that a roster owns more SP than the league permits it
to deploy. That signal is currently computed and thrown away.

### Finding that shaped the design

The signal is not quite discarded — it is re-derived, badly.
`pitcherPipelinesFor` (`internal/lineuprun/optimize_dates.go:419-428`)
reconstructs suppression by inference: *was probable* ∧ `!IsStarter` ∧ *gated*.
It feeds only the `--pipeline` debug view. It also computes the gated value as
`BlendedPtsPerGame * 0.10`, while `NonStarterSPDiscount` is **0.05**
(`internal/optimizer/pitcher_lineup.go:19`) — so that view currently reports
gated pitchers at double their real post-discount value. Same lines, so the fix
lands here.

### Gate returns its decision

`applyGSGate` gains a second return value, carried on `PitcherResult`:

```go
// GSGateReport records what the weekly game-start budget cost this date.
type GSGateReport struct {
    Suppressed             []SuppressedStart
    Limit, Used, Remaining int
}

type SuppressedStart struct {
    PlayerID, Name string
    ProjectedPts   float64 // blended pts/game at the moment of suppression
}

func (r GSGateReport) SuppressedPts() float64
```

Both early-return paths populate it: the `remaining <= 0` sweep (every unlocked
SP suppressed) and the ranked cut. The no-budget and budget-covers-everything
paths return a zero report.

**`SuppressedPts` is gross, not a net loss to the week, and the doc comment must
say so.** The budget is not destroyed — it is spent on a higher-ranked start
instead. What the number measures is SP capacity the roster owns and the league
will not let it deploy, which is exactly the dead-capital question the issue
asks. Without the caveat stated, a later reader would subtract it from the
week's realised total and be wrong.

### Removes the inference

`pitcherPipelinesFor` stops reconstructing `WasGated` and looks it up in the
authoritative report. Its `* 0.10` becomes `optimizer.NonStarterSPDiscount`.

### Daily surface

`internal/lineuprun/display.go:384-389` already prints the GS line. Add a second
line, only when suppressions occurred:

```
GS: 5/12 used (7 rem, 2.4 future)
GS gate: 2 starts suppressed (-24.3 proj pts)
```

### Weekly rollup rides the snapshot

The gate runs for today's date only (future dates in a `--dates` range are
optimized without it, since each day gets its own run), so any weekly figure has
to accumulate across the daily runs. The projection snapshot already persists
per-pitcher per-day state and already syncs to S3 under `backtest/`, so it is
the accumulation point — no new store, schedule, or IAM.

- `backtest.SnapshotPlayer` gains `GSSuppressed bool \`json:"gs_suppressed,omitempty"\``.
- `internal/lineuprun/snapshot.go`'s `buildSnapshot` already holds
  `dr.pitcherResult`, so populating it is a field copy.
- New pure function in `internal/backtest`:

```go
type GateSummary struct {
    Days             int
    SuppressedStarts int
    SuppressedPts    float64
    ByDate           []GateDay
}

func SummarizeGSGate(dir string, dates []time.Time) GateSummary
```

Reads existing snapshots via the existing `LoadSnapshot(dir, date)`, counts
pitchers with `GSSuppressed`, sums their `ProjPtsPerGame`.

- The `backtest` command prints the section. It already defaults to the last
  completed matchup week and accepts `--dates`, so the weekly window costs
  nothing new.

The rollup reuses the report's existing stale/missing day accounting, so a week
thinned by failed runs shows as thin rather than as a low suppression count.

Snapshots carry `gs_suppressed` only from the day this ships forward. Days
before that read as zero suppressions, which is indistinguishable from a real
zero. The `Days` count in `GateSummary` is what makes a thin window visible.

### Testing

- `internal/optimizer/gs_budget_test.go` — extend the existing tests to assert
  report contents. The ladder tests
  (`TestApplyGSGate_FractionalEstimateDoesNotOverSuppressToday`,
  `TestApplyGSGate_EstimatedLadderPricesWholeUnitsFullAndTailMarginally`)
  already construct the right scenarios; they gain assertions on which players
  the report names. Add a case pinning that locked players never appear in the
  report, matching the existing suppression exemption.
- `SummarizeGSGate` — unit test over snapshots written to a temp dir, including
  a missing-day case.
- `internal/lineuprun` golden test for the new display line. Regenerate with
  `go test ./internal/lineuprun/ -run TestRenderDateResult -update` and **read
  the diff** — `board_full.golden` carries the existing GS line.

### Deferred

The issue's closing "ideally also a roster-shape line: hitter vs pitcher value
against slot counts and the cap" is filed as its own bd issue. Comparing hitter
value to pitcher value across differently-sized slot pools is a modelling
question, not a plumbing one, and this change produces the measurement that
would feed it.

---

## Risks

- The fork push and pseudo-version bump re-stale the nested modules
  (`lambda/`, `opsnotify/`, `infra/` pull the root via `replace ../`), so
  `make build-modules` is mandatory before merge. CI runs it, and `make build`
  runs it locally.
- `applyGSGate` is unexported and called from exactly one place
  (`pitcher_lineup.go:52`), so its signature change stays inside the package.
  `OptimizePitcherLineup`'s own signature is unchanged — `PitcherResult` gains a
  field. The two external callers (`backtest/strategy_replay.go:156`,
  `backtest/backtest.go:227`) pass a nil budget, get a zero report, and need no
  edit.
- Golden-file changes must be read, not blindly regenerated.
- `infra` changes require `cdk deploy -c enableBuild=true` — without the flag
  the deploy destroys the CodeBuild project.

## Verification

- `go vet ./...`, `go mod tidy`, `make build-modules`, `make test`
- `make run-all` — exercises `version-check` and the optimizer end-to-end
- Idempotency check per CLAUDE.md: run `optimize --dry-run` twice against the
  same date and confirm the second run reports no changes, since Part 2 touches
  the pitcher path

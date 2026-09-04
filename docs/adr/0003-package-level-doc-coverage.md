# 3. Doc coverage is package-level and test-enforced

Date: 2026-09-04
Status: Accepted

## Context

`internal/backtest/gs_gate_summary.go` shipped (commits 68d89b4, e3d1a3e,
2026-08-05, rosterbot-i1c) with zero CLAUDE.md mention. Nothing noticed until
a later change (rosterbot-hx5) tried to anchor a new paragraph to
`GateSummary`/`SummarizeGSGate`/`FormatGateSummary`/`GSGateReport` and found
those symbols undefined anywhere in CLAUDE.md — the review that caught it was
manual, not mechanical. CLAUDE.md is the file every session loads as context
(the root `CLAUDE.md` and the `claude.ai/code` guidance both point at it), so
a gap there compounds: later work anchors to undefined terms, as this one did.

Measured at HEAD (before this fix): every directory under `internal/` holding
a `.go` file, compared against mentions of its repo-relative path in
`CLAUDE.md`, `README.md`, and `docs/**/*.md`. Excluding `testdata` directories
and genuine test-double/contract packages (`identitytest`, `cachetest`,
`s3blobtest`, `ddbusertest`, and similar — see below), exactly five packages
had no mention at all: `internal/archive/s3archive`,
`internal/backtest/s3backtest`, `internal/lineupapi/jobwire`,
`internal/lineupapi/kmscreds`, and `internal/teams`.

## Decision

Add a test — `TestEveryInternalPackageHasDocCoverage` in
`internal/statestore/layout/doccoverage_test.go` — that fails `go test ./...`
whenever a package under `internal/` has no mention of its path in
`CLAUDE.md`, `README.md`, or any `docs/**/*.md` file. It runs as part of the
ordinary root-module test suite; no new CI job or wiring is needed.

It lives beside `producer_test.go`, which already reads `infra/infra.go` off
disk to cross-check two hand-maintained lists (`layout.All()` vs
`infra.go`'s `perTenantJobs`) for the same reason this test exists: two things
that must agree, with nothing making them, previously drifted silently.

**The check is package-level, not symbol-level.** CLAUDE.md is already near
its practical size budget (see "Cutting an over-limit CLAUDE.md" in
project memory) and documents at the granularity of "here is what this
package does and why", not one paragraph per exported function — a
symbol-level gate would be far noisier than the convention the repo actually
follows, would demand a doc edit for routine internal refactors, and would
train people to add a throwaway sentence per symbol rather than writing the
kind of paragraph this file is actually good at. Package-level catches
exactly the rosterbot-i1c failure mode (a whole feature, and its file,
landing with no entry anywhere) at effectively zero false-positive cost: the
measured baseline above found only 5 gaps across ~85 non-test package
directories, and none was a false positive.

**Exemption rule.** A package is exempt only when BOTH:
1. its directory name ends in the literal suffix `test`, AND
2. it is never imported from a non-`_test.go` file anywhere in the repo
   (across all four Go modules — root, `lambda/`, `opsnotify/`, `infra/` —
   plus `cmd/`).

Naming alone is not enough. `internal/backtest` and
`internal/backtest/s3backtest` both end in the letters "test" purely because
`backtest` is the feature's own name, not because they are test-support
packages — and both are real production code, `s3backtest` being the S3
adapter for `backtest.SnapshotStore` (mirroring `s3store`/`s3ndjson`), which
is exactly one of the five originally-undocumented packages this bead
backfilled. A plain suffix match would have silently exempted it forever,
which is precisely the kind of gap this test exists to catch. Checking actual
import usage is what separates that from a genuine contract/test-double
package (`identitytest`, `cachetest`, `ddbusertest`, `s3blobtest`,
`enrollmenttest`, `infralistertest`, `notificationtest`, `outputtest`,
`progresstest`, `pushdevicetest`, `usertest` — all confirmed at HEAD to be
imported only from `_test.go` files, never from production code).

**Why a Go test and not the `doc-drift-check` skill.** The repo already has a
`doc-drift-check` Claude Code skill, but a skill is something an agent
chooses to invoke in an interactive session — it is not wired into CI and
provides no enforcement against a PR that never triggers it (exactly the
rosterbot-i1c path: a change landed with no session invoking any doc-review
skill). A `go test` runs unconditionally on every `make lint`/CI pass with no
opt-in step, which is the property this gate needs.

**Scope decision: `cmd/` command coverage is left to the existing manual
convention, not a second mechanism.** CLAUDE.md's "Commands" and "README"
sections already state the convention ("When adding new commands, flags, or
env vars... update README.md") and a companion convention already exists and
is enforced for one adjacent case: `make run-all`'s comment instructs
appending any new `cmd/<x>.go` registered on `rootCmd` to the smoke-test
recipe, which is a stronger forcing function than a comment alone since a
missing entry means the new command is never exercised by the canonical
end-to-end smoke test. A `cmd/*.go` ↔ `README.md` `Use:`-string coverage test
was considered (mirroring this one) and deferred: unlike `internal/`
packages, every `cmd/<x>.go` is small, human-reviewed on every PR by
necessity (cobra registration is visible in the diff), and the repo has not
measured a case of a command shipping with zero README mention the way
`gs_gate_summary.go` shipped with zero CLAUDE.md mention. If that changes, a
symmetrical `TestEveryCommandHasAReadmeMention` belongs beside this one.

## Consequences

- Adding a new `internal/` package with no doc mention now fails `go test
  ./...` immediately, naming the package and the three places it may be
  documented, rather than surfacing later as a stale-anchor bug in an
  unrelated change.
- The five packages this bead found were backfilled in CLAUDE.md at the
  points where their siblings are already documented (the `internal/s3blob`
  S3-shim family for `s3archive`/`s3backtest`, the `jobwire`/`progress`
  pairing, the KMS decrypt mention in the connect-routing paragraph for
  `kmscreds`, and the `sameTeam`/HKB club-comparison bullet for `teams`).
- A package that is a genuine test double must keep its directory name ending
  in the literal suffix `test` (the existing repo-wide convention) AND must
  never be imported from non-test code — if it starts being imported from
  production code, the gate will require documenting it, which is the
  correct outcome (it would no longer be pure test support).
- `cmd/` coverage stays on the manual README-update convention, decided here
  rather than left unstated.

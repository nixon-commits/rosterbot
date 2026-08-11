# Stale critical-branch alert: page when a safety-critical fix sits unmerged

**Issue:** rosterbot-v4l
**Date:** 2026-08-11

## Problem

During the 2026-08-01T13:01Z → 2026-08-03T17:01Z Fantrax STALE_CLIENT outage
(rosterbot-7rl, ~54 failed task runs), not one Pushover alert fired. The
task-failure alerting infra that should have caught it (`OpsNotify` Lambda +
`TaskFailRule`, from rosterbot-naz) simply wasn't live yet.

rosterbot-v4l's original framing was "merged but undeployed" — CD failed to
pick up a merged commit. Git history disproves that framing: `infra/infra.go`
on `main` had zero references to `TaskFailRule`/`OpsNotify` until the merge
commit `7d54b29` landed at 2026-08-03T17:03 UTC (confirmed by diffing
`infra.go` at the last pre-outage deploy, `649774c`). CloudFormation's
`CREATE_COMPLETE` followed four minutes later — CD (`cdk deploy` in the
CodeBuild buildspec, live since `4d625eb`, well before this incident) worked
correctly. The actual gap: the branch `fix/rosterbot-naz-task-failure-alerting`
carried the finished fix from 2026-07-31T21:54 UTC (first commit) /
2026-08-01T18:34 UTC (last commit) but sat unmerged into `main` until
2026-08-03T17:03 UTC — roughly two days, spanning the entire outage.

So the two remedies rosterbot-v4l originally proposed both miss:
- **"Auto-deploy on merge"** already exists and evidently works.
- **"Deployed-stack-vs-main drift check"** would have reported zero drift the
  whole outage — `main` and the deployed stack agreed with each other the
  entire time; both were missing the code, because it was still on a branch.

The real gap is a finished, safety-critical fix sitting unmerged with nothing
watching for that. This is worth generalizing rather than one-off fixing,
per the ticket's own framing: "nothing today flags 'alerting infra is stale
relative to main' the way `make check-pins` flags a stale nested go.mod."

A second finding shapes the design: this repo does not consistently merge
through GitHub PRs. A survey of the 40 most recent first-parent commits on
`main` found 14 carry a PR reference (`(#N)`); the rest — including the
`rosterbot-naz` merge itself — are direct `git merge` commits pushed straight
to `main`, with no corresponding open PR (`gh pr list` found none for
`fix/rosterbot-naz-task-failure-alerting`). A check built on the GitHub PR API
would have missed the exact incident it's meant to catch. It has to watch
**branches**, not PRs.

## Decision

A new GitHub Actions scheduled workflow,
`.github/workflows/stale-critical-branch.yml`, checks every remote branch
against `main` for un-landed **content** changes to a short list of
safety-critical paths, and pages Pushover when one has sat that way past a
threshold.

```
schedule: cron('0 */6 * * *')   # matches HeartbeatRule's cadence
workflow_dispatch:              # manual runs
```

### Detection

For each `origin/*` branch except `main`:

1. `git diff --name-only origin/main...origin/$branch -- infra/ internal/opsalert/ opsnotify/`
   — if empty, skip.
2. `git log -1 --format=%ct origin/main..origin/$branch -- <those paths>` —
   newest commit unique to the branch touching those paths.
3. `age = now - that commit's timestamp`. `age > STALE_HOURS` (default 24,
   env var) → add `(branch, age, files)` to the stale list.

**Content diff, not commit-ancestry, is the load-bearing choice.** A survey of
this repo's current branches found several (`fix/rosterbot-0gm-*`,
`fix/rosterbot-abd-*`, `feat/trades-tab`, …) that show as permanently "ahead"
of `main` by commit count after a squash-merge — different SHAs, same
content, forever. Gating on `git diff --name-only` against the specific paths
sidesteps that: once a squash-merged branch's critical-path content matches
`main`, the diff is empty and it's correctly ignored, with no ancestry
bookkeeping. Verified against the live repo: `ops-alerting-hardening` (a real,
still-divergent branch touching 12 files across `infra/`, `internal/opsalert/`,
`opsnotify/`) shows a non-empty diff and a last-touch timestamp of
2026-08-04T00:28 UTC — ~7.5 days stale as of this writing — so the check has
an immediate true positive on first run.

### Alerting

Non-empty stale list → one Pushover push (`PUSHOVER_USER_KEY` /
`PUSHOVER_APP_TOKEN`, both already present as GH Actions repo secrets from
the pre-AWS-migration era — no new secrets) listing each branch, hours
stale, and touched files. The job then exits non-zero so the Actions tab also
shows red. Empty list → silent exit 0, matching this repo's alert-only-on-
problem convention (e.g. `gs-check`'s clean no-op).

No dedup/throttle in v1: a branch that stays stale re-alerts every 6h. This
is deliberate — the situation is low-frequency and fully user-resolvable
(merge or delete the branch), so repeat nagging is the intended forcing
function, not noise. A once-daily throttle is a cheap follow-up if it proves
annoying in practice.

### Scope

Critical paths default to `infra/ internal/opsalert/ opsnotify/` — the two
subsystems this incident actually involved (CDK infra, the alerting Lambda).
Expressed as a workflow env var so extending the list later is a one-line
edit, not a script change.

### Where this runs, and why not AWS

The ECS/Fargate side ships only the built Go binary — no git checkout, no
GitHub credential. Doing this check there would mean minting a GitHub PAT,
storing it in SSM, and granting a task read access to clone the repo at
runtime: real new attack surface for a hygiene check. GitHub Actions has
free repo access via the built-in token and `actions/checkout`, and the
Pushover secrets this needs are already sitting unused in the repo's GH
secrets. This sits alongside `ci.yml`/`codeql.yml` as a repo-hygiene check —
not a reintroduction of the scheduled *jobs* (`optimize`, `waivers`, …) that
were deliberately moved off GHA in the AWS migration; those depend on
Fantrax credentials and heavy compute, this doesn't.

## Testing

No Go code changes, so no `go test` coverage. The workflow's shell logic is
hard to unit test in isolation; verification is:

1. A local dry run of the detection script against the live repo (already
   done above — confirms `ops-alerting-hardening` fires, confirms the
   already-squash-merged branches correctly don't).
2. `workflow_dispatch` a real run after merging, and confirm the Pushover
   push lands (paging the user's phone once, as an end-to-end check — same
   pattern rosterbot-naz used to verify the AWS alerting path in production).
3. Confirm the job exits 0 with no push once `ops-alerting-hardening` is
   either merged or deleted.

## Out of scope

- A GitHub PR-based check (bots like `stale`) — doesn't cover the ~65% of
  recent merges here that never went through a PR.
- Deployed-stack-vs-`main` drift detection — still a legitimate independent
  safety net for a *different* failure mode (CD itself silently breaking on
  a real merge), but doesn't address what actually happened here. Not part
  of this change; the user explicitly deferred it in favor of the
  stale-branch guard alone.
- Extending critical-path scope beyond `infra/`, `internal/opsalert/`,
  `opsnotify/` (e.g. `.github/workflows/`, `entrypoint.sh`) — easy to add
  later via the env var if warranted; not justified by this incident.

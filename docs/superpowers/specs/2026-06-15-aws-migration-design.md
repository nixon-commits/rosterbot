# AWS Migration Design — rosterbot off GitHub Actions

**Date:** 2026-06-15
**Status:** Approved design, pending implementation plan
**Goal:** Move scheduled compute and storage off GitHub Actions onto AWS, learning the mainstream "scheduled containers" pattern, for ~$3–5/month.

## Motivation

rosterbot today is a Go binary whose 8 subcommands run on GitHub Actions cron schedules, caching state in the GHA cache and deploying a recap site to GitHub Pages. Two pain points and one goal drive this migration:

1. **Learning** — get hands-on with the canonical AWS way to run scheduled containers (ECS Fargate + EventBridge Scheduler), CDK, S3, ECR, CodeBuild.
2. **Storage ceiling** — the GitHub Actions cache is capped at **10 GB per repo** and evicts entries, so the warm `.cache/` keeps getting cold across workflows. S3 has no practical size cap.
3. **Get off GHA runners** — fully, including CI builds.

This is a **packaging-and-scheduling migration, not a rewrite**. No Go application logic changes. Scoring, optimizer, projection blending, and Pushover notifications are untouched.

## Non-goals

- Rewriting any Go application code or subcommand behavior.
- Kubernetes / EKS (rejected: ~$73/month control-plane charge, not free tier, massive overkill for cron jobs).
- AWS Lambda (rejected: chromedp/headless-Chrome does not fit cleanly; 15-min cap risks `recap-site`).
- Multi-region, autoscaling, or high-availability concerns (single-region, single-task-per-run is correct here).

## Decisions (locked)

| Area | Decision | Rationale |
|---|---|---|
| Compute | **ECS Fargate** (not EKS/Lambda) | Full container = chromedp/Chrome just works; no control-plane cost |
| Scheduling | **EventBridge Scheduler** | Native cron + timezone support; RunTask with per-schedule command override |
| Storage / state | **S3 sync** (down on start, up on exit) | Cheapest, no networking setup, removes 10 GB GHA cap; S3 is the "managed storage" |
| Recap site | **S3 + CloudFront** | HTTPS + CDN; teaches S3 static hosting |
| Image build | **AWS CodeBuild** (fully off GHA) | Zero GitHub Actions; teaches AWS CI; free tier covers it |
| Infra as code | **AWS CDK in Go** | Reproducible, matches repo language, `cdk destroy` for cost control while learning |
| Secrets | **SSM Parameter Store (SecureString)** | Free standard params vs. Secrets Manager $0.40/secret/month |
| Networking | **Default VPC, public subnet, public IP, no NAT** | Tasks reach internet + ECR without a ~$32/month NAT Gateway |
| Task size | **One task definition, 1 vCPU / 2 GB** | Chrome + recap aggregation need headroom; cost diff negligible for short runs |

## Architecture

```
                    ┌─────────────────────────────────────────┐
                    │  EventBridge Scheduler (8 schedules)     │
                    │  cron/rate, native timezone support      │
                    └───────────────┬─────────────────────────┘
                                    │ RunTask (command override)
                                    ▼
   ┌──────────┐   pull image   ┌─────────────────────────────┐
   │   ECR    │◄───────────────│  ECS Fargate task           │
   │ (image)  │                │  1 task def, 1vCPU/2GB       │
   └────▲─────┘                │  image = Go binary + chromium│
        │ push                 │                             │
   ┌────┴─────┐                │  entrypoint wrapper:         │
   │ CodeBuild│                │   1. s3 sync ↓ (warm state) │
   │ (on push)│                │   2. run rosterbot <cmd>    │
   └──────────┘                │   3. s3 sync ↑ (save state) │
                               └──────┬────────────┬─────────┘
                       state sync ↕   │            │  notify
                                      ▼            ▼
                            ┌──────────────┐  ┌──────────┐
                            │  S3: state   │  │ Pushover │
                            │  cache/      │  └──────────┘
                            │  session/    │   recap-site writes →
                            │  claims/     │  ┌─────────────────────┐
                            └──────────────┘  │ S3 site + CloudFront │
                                              └─────────────────────┘
       Secrets: SSM Parameter Store (SecureString) → injected as task env
```

**One image, one task definition, eight schedules.** Each EventBridge schedule launches the same Fargate task with a different `command` override, a 1:1 port of the existing 8 workflows.

## Components

### 1. Container image

Multi-stage Docker build:
- **Builder stage:** `golang:1.26` → `go build -o rosterbot .`
- **Runtime stage:** Debian-slim + `chromium` (for chromedp headless login) + the `rosterbot` binary + the AWS CLI (for the entrypoint sync) + the entrypoint wrapper script.

Pushed to **ECR** tagged `:latest` and `:<git-sha>`. Fargate default 20 GB ephemeral disk is ample for the ~18 MB binary + Chrome + 49 MB cache.

### 2. Entrypoint wrapper (state sync)

A thin shell script, the container `ENTRYPOINT`, keeping S3 *out* of the Go code so the binary stays cloud-agnostic and tests stay hermetic:

```sh
#!/bin/sh
set -e
aws s3 sync "s3://$STATE_BUCKET/cache/"   ./.cache/         --quiet || true
aws s3 sync "s3://$STATE_BUCKET/session/" ./.fantrax-cache/ --quiet || true
aws s3 sync "s3://$STATE_BUCKET/claims/"  ./.waivers/       --quiet || true

./rosterbot "$@"      # command supplied by EventBridge override
rc=$?

aws s3 sync ./.cache/         "s3://$STATE_BUCKET/cache/"   --quiet || true
aws s3 sync ./.fantrax-cache/ "s3://$STATE_BUCKET/session/" --quiet || true
aws s3 sync ./.waivers/       "s3://$STATE_BUCKET/claims/"  --quiet || true
exit $rc
```

Sync failures are non-fatal (`|| true`) — consistent with the existing cache philosophy that all cache I/O errors are non-fatal. The `last-claims.json` cursor lives at the repo root today; the wrapper must also sync that file (see Open Items).

### 3. S3 state bucket layout

One private bucket, three prefixes, organized around **who writes what**:

| Prefix | Holds | Writers | Race safety |
|---|---|---|---|
| `cache/` | the whole `.cache/` (the 49 MB warm cache, now uncapped) | every job | **Benign** — fully regenerable; worst case a redundant upstream fetch, never data loss |
| `session/` | `.fantrax-cache/` chromedp cookie | first job to log in each day | Benign — any valid cookie works |
| `claims/` | `.waivers/claims/` ledger + relocated `last-claims.json` cursor | **only `claims`** | **Safe** — single-writer; the only irreplaceable mutable state can never be clobbered |

**Critical correctness fix:** by default the cursor is `.cache/last-claims.json` (`internal/claims/cursor.go:11`) — i.e. *inside* the shared, every-job-writes `cache/` prefix, **not** the single-writer `claims/` prefix. Left there, a non-`claims` job that downloaded a stale `.cache` could write back an old cursor and clobber a freshly-advanced one → duplicate claim delivery. `CursorPath` is already configurable (`internal/claims/types.go:75`), so the migration **must** override it to `.waivers/last-claims.json` (or anywhere under `.waivers/`) so the cursor rides the single-writer `claims/` prefix. With that override, the sole piece of precious mutable state is written by exactly one job and the shared bucket needs no locking. Regenerable cache races are otherwise tolerable. The two same-time jobs (`transactions`, `claims`, both 14:00 UTC today) should also be nudged ~15 min apart to keep cache write-back clean.

### 4. EventBridge schedules (port of the 8 workflows)

| Schedule | Command override | Cadence (today) |
|---|---|---|
| lineup | `optimize --matchup --archive-projections` | hourly during active window (6 AM–7 PM PT) |
| prospects | `prospects` | daily 11:00 UTC |
| gs-check | `gs-check` | daily 12:00 UTC |
| waivers | `waivers` | daily 13:00 UTC |
| transactions | `transactions` | daily 14:00 UTC (nudge ±15 min from claims) |
| claims | `claims` | daily 14:00 UTC (nudge ±15 min from transactions) |
| recap | `recap-site --out dist` then publish to S3 | Mondays 11:00 UTC |
| backtest | `backtest` then `backtest --recency-experiment` | Mondays 12:00 UTC |

EventBridge Scheduler supports native timezones, so the lineup active-window crons can be expressed directly in `America/Los_Angeles` instead of split UTC ranges.

### 5. Recap site (S3 + CloudFront)

`recap-site --out dist` writes the static site locally; the wrapper (for the recap schedule) uploads `dist/` to a dedicated **site bucket**, fronted by **CloudFront** for HTTPS + CDN. Pushover link points at the CloudFront URL.

### 6. Build pipeline (CodeBuild)

- **CodeConnections** GitHub link triggers **CodeBuild** on push to `main`.
- `buildspec.yml`: docker build → push `:latest` + `:<git-sha>` to ECR.
- Free tier: 100 build-minutes/month covers this comfortably.

### 7. Infra as code (CDK in Go)

One CDK app provisions: ECR repo, S3 state bucket, S3 site bucket + CloudFront distribution, ECS cluster + Fargate task definition + IAM task/execution roles (S3 RW on its prefixes, SSM read, ECR pull, CloudWatch write), the 8 EventBridge schedules + scheduler IAM role, CodeBuild project + CodeConnections source, CloudWatch log groups. `cdk deploy` / `cdk destroy` manage the full stack.

### 8. Secrets

`FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`, `GS_MAX`, `GS_MIN`, `PUSHOVER_*` → **SSM Parameter Store SecureString**, injected into the task definition via ECS `secrets` (env at runtime). No secret values in the image or in CDK source.

## What changes vs. stays the same

**Unchanged:** all Go code, every subcommand, scoring/optimizer/blending logic, Pushover notifications, the `.cache` TTL scheme, the claims cursor/ledger format.

**Changes:**
- GHA job-summary markdown (prospects/waivers/backtest) → **CloudWatch Logs**.
- Recap deploy: GitHub Pages → **S3 + CloudFront**.
- Secrets: GHA repo secrets → **SSM Parameter Store**.
- Auth in CI: GHA chromedp login each day → still chromedp, but cookie persisted in `s3://.../session/`.

## Cost estimate

| Service | Monthly |
|---|---|
| Fargate (hourly lineup + daily jobs, 1vCPU/2GB, short runs) | ~$2–4 |
| S3 (state + site, < 1 GB, modest requests) | pennies |
| CloudFront (low traffic) | pennies |
| CodeBuild / SSM standard params / EventBridge | $0 (free tier) |
| **Total** | **~$3–5** |

NAT Gateway deliberately avoided (would have been ~$32/month alone).

## Open items for the implementation plan

- **`last-claims.json` relocation** — RESOLVED: default is `.cache/last-claims.json` (shared prefix, unsafe). Plan must set `CursorPath` to `.waivers/last-claims.json` so it rides the single-writer `claims/` prefix. Confirm how `CursorPath` is wired from the `claims` command (flag vs env) when writing the plan.
- **Schedule stagger** — pick the exact offset for `transactions` vs `claims` (proposed: transactions 14:00, claims 14:20 UTC).
- **lineup active-window cron** — translate the current two UTC cron ranges into a single `America/Los_Angeles` EventBridge expression.
- **Task size validation** — confirm 1 vCPU / 2 GB is enough for Chrome + the `recap-site` full-season parallel aggregation; bump if OOM.
- **ECR image lifecycle policy** — keep last N `:<git-sha>` tags to bound storage.
- **Decommission order** — keep GHA workflows running in parallel until AWS runs are verified, then delete `.github/workflows/*` and the Pages deploy.
```

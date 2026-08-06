# AWS Deployment Runbook

rosterbot runs on AWS (account `476646938644`, region `us-west-1`) as ECS Fargate tasks
launched by EventBridge schedules. Infra is AWS CDK (Go) under `infra/`. See the design
spec `docs/superpowers/specs/2026-06-15-aws-migration-design.md` for rationale.

## Architecture (deployed)

- **ECR** `rosterbot` — container image (Go binary + chromium + aws-cli), ARM64.
- **ECS Fargate** — one task definition (`bot` container, 1 vCPU / 2 GB, ARM64/Graviton).
  Each run syncs state to/from S3 via `entrypoint.sh`, then runs `rosterbot <command>`.
- **EventBridge rules** — 14 schedule rules (1:1 port of the old GitHub Actions crons, UTC, plus `ProjectionSite`, `Archive`, `TeamValues`, `Shadow`, `VersionCheck`), plus two notification rules (`BuildNotifyRule`, `TaskFailRule`) that both target the `OpsNotify` Lambda
- **S3 state bucket** (`infrastack-statebucket…`) — prefixes `cache/`, `session/`, `claims/`, `backtest/` (projection snapshots, synced by the entrypoint), `analysis/grades/` (Graded Snapshots, NDJSON, written by `grade`), `analysis/team-values/` (Team Value Store, NDJSON, written daily by `team-values`), `lineup/` (read-only API JSON, published per-key by the hourly `optimize` run), `athena-results/`.
- **Lineup + control API** — a Go Lambda (`LineupApi`) behind a **Function URL** (output `LineupApiUrl`). Routes: `GET /v1/lineup/today` (from `lineup/today.json`), `GET /v1/runs` + `GET /v1/runs/{id}` (the run ledger under `runledger/`, split out from `runs/` in rosterbot-432 so listing no longer pages past per-run output blobs), and `POST /v1/jobs/{name}` (launches the existing Fargate task via `ecs:RunTask`, command overridden, `RUN_TRIGGER=manual`). Auth is a Bearer token in SSM (`/rosterbot/ROSTERBOT_API_TOKEN`), enforced in the function (Function URL auth type `NONE`). IAM is least-privilege: read `lineup/*`+`runledger/*`+`runs/*` (the last for per-run captured output), `ssm:GetParameter` on the token, `ecs:RunTask` on the task def, `iam:PassRole` on the task/execution roles. Tasks it launches use a dedicated egress-only SG (`TaskSg`) in the default VPC's public subnets. See the README "Lineup HTTP API" section for the contract.

  Auth accepts either a signed session cookie (set by a successful passkey login, `/v1/auth/*`
  routes) or the legacy Bearer token — the token is no longer surfaced in the dashboard's normal UI
  after the first passkey is registered, but stays wired in as a break-glass/recovery credential (see
  "Passkey auth" below).
- **Run ledger** — `entrypoint.sh` writes one JSON object per run to `runledger/<invTs>-<taskId>.json` (start = `RUNNING`, end = `SUCCESS`/`FAILED` with exit code + a log tail on failure) via the internal `rosterbot run-ledger` command. Until rosterbot-432 this lived under `runs/`, shared with per-run output blobs (`runs/<id>/output.json`), so listing had to page past however many of those existed; the ledger now has its own prefix. The inverted-timestamp key prefix sorts newest-first, so `GET /v1/runs` is a cheap, bounded list scoped to `runledger/` alone. Covers scheduled and API-triggered runs alike (`RUN_TRIGGER` distinguishes `schedule` vs `manual`).
- **Analysis Store** — Athena workgroup `rosterbot`, Glue table `rosterbot_analysis.grades` (partition projection on `dt`, no crawler). Query model accuracy with SQL, e.g. `SELECT bucket, avg(abs(diff)) mae FROM rosterbot_analysis.grades WHERE dt >= '2026-06-01' GROUP BY bucket;`.
- **Retention** — the state bucket has versioning **enabled**, so `cache/` overwrites are retained as noncurrent versions (cache history). `backtest/` and `analysis/` are append-only and never expired. Nothing in the stack deletes analysis data; a cost-control lifecycle rule to expire old noncurrent `cache/` versions can be added later if needed.
  The `cache/` prefix is written **per-key, live by the bot** via `cache.Store` (the s3 adapter,
  selected when `STATE_BUCKET` is set) — not bulk-synced by the entrypoint. `session/` (chromedp
  cookie) and `claims/` (ledger+cursor) are still bulk-synced by `entrypoint.sh`. Clear the cache
  with `aws s3 rm s3://<state-bucket>/cache/ --recursive`.
- **S3 site bucket** (`SITE_BUCKET`) + **CloudFront** (`https://d3g6t1hhf4o9r6.cloudfront.net`) — recap site. `entrypoint.sh` invalidates the distribution (`SITE_CF_DIST_ID`) after each sync so a fresh render isn't masked by the CDN cache TTL.
- **Recap readership logs** (`RecapLogBucket`, name in the `RecapLogBucketName` stack output) — `SiteCdn` writes CloudFront standard access logs to the `recap/` prefix. The recap site is public and unauthenticated, so these logs are the *only* signal that anyone reads it: they record when each page was fetched, the client IP, the URI, and the edge location (a free coarse geo proxy — CloudFront has no country field in standard logs; that needs standard logging v2 or an IP→geo lookup at query time). Cookies are excluded. Objects expire after **90 days** (`ExpireRecapAccessLogs`), so this prefix can't become the unbounded leak the `cache/` rule exists to prevent. **`DashboardCdn` is deliberately not logged** — it's passkey-gated and out of scope. Query via Glue table `rosterbot_analysis.recap_access_logs` (same Athena workgroup as `grades`; unpartitioned, since CloudFront writes flat `recap/<dist-id>.YYYY-MM-DD-HH.<hash>.gz` keys rather than a Hive `dt=` layout — at this traffic volume a full-prefix scan is a rounding error):

  ```sql
  -- who read which recap page, most recent first
  SELECT log_date, log_time, client_ip, edge_location, uri_stem
  FROM rosterbot_analysis.recap_access_logs
  WHERE status = 200 AND uri_stem LIKE '%.html'
  ORDER BY log_date DESC, log_time DESC LIMIT 100;

  -- distinct readers per day
  SELECT log_date, count(DISTINCT client_ip) readers, count(*) views
  FROM rosterbot_analysis.recap_access_logs
  WHERE uri_stem LIKE '%.html' GROUP BY log_date ORDER BY log_date DESC;
  ```

  Only the first 14 of CloudFront's ~33 log fields are declared in the table; the SerDe ignores trailing fields, and 14 reaches everything above. Extending means **appending** columns in CloudFront's documented field order — the existing positions must not be reordered, since the TSV mapping is positional.

  The same logs also drive the dashboard's **Views** tab without going through Athena: `projection-site` reads them directly (`internal/recaplog`, via the `RECAP_LOG_BUCKET` env var and a read-only `recap/*` grant on the task role) and writes `views.json` alongside `model.json`/`value.json`. Athena remains for ad-hoc questions; the tab is the standing view.
- **Projection-accuracy dashboard + team-value tracker, folded into the private dashboard** — `model.json` (projection accuracy) and `value.json` (team HKB-value tracker) are written per-run by `projection-site` (`internal/report`/`internal/valuereport` produce the aggregated `Model`, serialized straight to JSON — no server-side HTML render) via `entrypoint.sh` sync, published under `DASHBOARD_BUCKET`'s `report/` key prefix (`DASHBOARD_CF_DIST_ID` invalidated after publish) rather than a standalone bucket+CDN — the same bucket/distribution that serves the passkey-gated dashboard SPA (`DashboardBucket`/`DashboardCdn`). Exposed inside the SPA as the "Projections", "Value" and "Views" nav tabs, which `fetch()` `/report/model.json` / `/report/value.json` / `/report/views.json` directly and render natively client-side (vendored Chart.js) — no iframe. The JSON files themselves remain fetchable by direct URL without a passkey session, same exposure as before the JSON migration — this was a rendering-path change, not an access-control change. The old standalone `ReportBucket`/`ReportCdn` (formerly at the `ReportUrl` stack output) has been retired. The `TeamValues` schedule (`cron(30 14 * * ? *)`) writes today's `analysis/team-values/` partition before `ProjectionSite` (15:00 UTC) renders `value.json`.
- **S3 dashboard bucket** (`DashboardBucket`) + **CloudFront** (`DashboardCdn`, URL in `DashboardUrl` stack output) — the private control-panel web UI (`web/dashboard/`, static, no build step). One distribution serves both surfaces: its default behavior serves the static files from `DashboardBucket`; an additional `/v1/*` behavior proxies straight to the `LineupApi` Function URL (`CachePolicy.CACHING_DISABLED`, `OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER` so the `Authorization` header passes through), making the browser's calls same-origin with zero CORS configuration anywhere. CodeBuild deploys the stack first, reads `DashboardBucketName` and `DashboardCdnId` from its outputs, then syncs `web/dashboard/` and invalidates the distribution on every push to `main` — including the first build that creates those resources.
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
- **SSM Parameter Store** (`/rosterbot/*`, SecureString) — all secrets, injected as task env.
- **CodeBuild** — on push to `main`, builds + pushes the image to ECR, then runs **`cdk deploy -c enableBuild=true`** so infrastructure changes (schedules, task defs, IAM, Lambda) ship on merge — not just the image — before publishing the dashboard (reading the deploy's own outputs, see above) and launching the `projection-site` task (`ecs:RunTask` via `taskDef.GrantRun`, reusing `TaskSg` + public subnets) so it re-renders immediately with the new image. Before the `cdk deploy` step existed, a PR touching `infra/` merged green but its infra change sat undeployed until someone ran `cdk deploy` by hand (this is what left the `Archive` schedule inert for ~25h); deploying first also means a broken infra change now fails the build before the dashboard/projection-site steps run, rather than shipping around it. The `enableBuild=true` in the buildspec is mandatory — without it the deploy would delete the CodeBuild project running it. cdk works through the bootstrap roles, so the build role is granted only `sts:AssumeRole` on `cdk-hnb659fds-*`. The build host ships Go 1.20 + no cdk CLI, so the `install` phase adds Go 1.25 (`GOTOOLCHAIN=auto` upgrades further if a go.mod requires it) and the pinned cdk CLI. Gated by `enableBuild`.
- **Ops notifications** — three EventBridge rules target one Lambda (`OpsNotify`, built from `opsnotify/`), which reads `PUSHOVER_USER_KEY` / `PUSHOVER_API_TOKEN` from SSM and posts to the personal ops channel at priority 0.
  - `BuildNotifyRule` matches the `Build` project's `CodeBuild Build State Change` events for `SUCCEEDED` / `FAILED` / `STOPPED`. This catches every failure phase (install/pre_build/build/deploy) — a buildspec curl would miss install/pre_build failures since `post_build` never runs then. Created only under `-c enableBuild=true`; the first build that introduces the rule won't notify itself, every subsequent build does.
  - `TaskFailRule` matches `ECS Task State Change` for any task in the cluster reaching `STOPPED` (rosterbot-naz). Whether that stop was a *failure* is decided in Go, not by the event pattern — see `internal/opsalert`. The Lambda derives a failure streak from the run ledger and pushes only on transitions: first failure, third consecutive, and recovery. A stopped task with no ledger record at all never reached the entrypoint's final write (OOM, image-pull, SIGKILL) and always alerts.
  - `HeartbeatRule` fires every 6 hours at `:15` and asks the opposite question: has each job run *at all* (rosterbot-ys8)? `TaskFailRule` is reactive, so a job that never launches — disabled rule, broken cron, unreachable cluster — emits no event and produces silence that reads exactly like health. The rule carries a constant input (`detail-type: "Rosterbot Heartbeat"`) since a scheduled rule has no detail-type of its own. Per-job tolerances live beside the crons in `infra/infra.go` and reach the Lambda via the `JOB_SCHEDULES` env var — 13h for the hourly `Lineup` (its real worst case is the 11h overnight gap, not 1h), 26h for the daily jobs, 8d for the weekly `Recap`/`Backtest`. It alerts once per outage, not once per tick. **Gated with the job schedules**, so `-c schedulesEnabled=false` pauses it too: a heartbeat that pages about jobs someone deliberately stopped is the alert that teaches people to ignore the channel.
  - **The `OpsNotify` function itself is created unconditionally** — only `BuildNotifyRule` sits behind `enableBuild`, because job-failure alerting must survive a stack deployed without that flag.
  - **Alerts are deduplicated across deliveries** (rosterbot-chs) through small marker objects under the state bucket's `opsalert/` prefix — the one prefix `OpsNotify` may write, and the only write grant it holds. EventBridge can deliver an event twice and async-invoke retries on error, and since the verdict is derived from a ledger that does not change in between, a repeat would push the identical alert again. A 30-day lifecycle rule expires the prefix, far past any retry horizon. Every failure of the marker store degrades to a duplicate alert, never to a missing one.

## Common operations

```bash
cd infra
export JSII_SILENCE_WARNING_UNTESTED_NODE_VERSION=1

cdk diff                              # preview changes
cdk deploy --require-approval never   # apply (schedules stay disabled)
```

Run a job by hand (networking IDs from the default VPC; substitute the cluster name from
`cdk deploy` outputs):

```bash
aws ecs run-task --region us-west-1 \
  --cluster <ClusterName> --task-definition InfraStackTask --launch-type FARGATE \
  --network-configuration 'awsvpcConfiguration={subnets=[<publicSubnet>],securityGroups=[<defaultSG>],assignPublicIp=ENABLED}' \
  --overrides '{"containerOverrides":[{"name":"bot","command":["waivers","--dry-run"]}]}'
```

Tail logs: `aws logs tail <LogGroupName> --region us-west-1 --follow`

## Deploy the lineup API (one-time prep)

The lineup Lambda lives in its own module (`lambda/`) and is built by a CDK
GoFunction, which is in the `awscdklambdagoalpha` package. Both need a one-time
fetch before the first `cdk deploy`:

```bash
cd lambda && go mod tidy                                   # resolve aws-lambda-go + sdk
cd ../infra && go get github.com/aws/aws-cdk-go/awscdklambdagoalpha/v2@latest && go mod tidy
aws ssm put-parameter --name /rosterbot/ROSTERBOT_API_TOKEN --type SecureString --value '<token>' --overwrite
cdk deploy --require-approval never                        # grab LineupApiUrl from the outputs
```

GoFunction bundles the Lambda with local Go (cross-compiles to ARM64), so Docker
isn't required on the synth host as long as Go is installed.

## Update a secret

```bash
aws ssm put-parameter --name /rosterbot/PUSHOVER_API_TOKEN --type SecureString --value '...' --overwrite
```

## Ship a new image

- **Automated (preferred):** push to `main` → CodeBuild builds + pushes `:latest` + `:<sha>`.
- **Manual (the auto-mode classifier blocks Claude from doing this — run it yourself):**
  ```bash
  aws ecr get-login-password --region us-west-1 | docker login --username AWS --password-stdin 476646938644.dkr.ecr.us-west-1.amazonaws.com
  docker build -t 476646938644.dkr.ecr.us-west-1.amazonaws.com/rosterbot:latest .
  docker push 476646938644.dkr.ecr.us-west-1.amazonaws.com/rosterbot:latest
  ```
  Tasks pull `:latest` on next run.

---

## Pending one-time steps (not yet done)

### A. Clear the new-account block (REQUIRED before any task runs)

New AWS accounts get an automated identity/billing review that returns
`BlockedException: Your account is currently blocked` on `RunTask`. To clear:

1. Check the root-account email for an AWS identity/payment verification message; complete it.
2. Sign in to the console and resolve any yellow "verify"/"activate" banner.
3. If neither: Support Center → Create case → **Account and billing** → "Account blocked /
   activation". Basic support is free.
4. Confirm a valid payment method (Billing → Payment preferences).

Verify cleared by re-running any `aws ecs run-task` above (expect a task ARN, not an error).

### B. Enable CodeBuild (GitHub → AWS)

CodeBuild's GitHub webhook source needs a one-time source credential:

1. AWS console → CodeBuild → **Create project** (or Settings) → connect to GitHub via OAuth
   **once** (this stores the source credential account-wide), then cancel the wizard. *Or*
   import a GitHub PAT: `aws codebuild import-source-credentials --server-type GITHUB --auth-type PERSONAL_ACCESS_TOKEN --token <PAT>`.
2. Deploy with the build project enabled:
   ```bash
   cd infra && cdk deploy -c enableBuild=true --require-approval never
   ```
3. Push a commit to `main`; confirm a build appears in CodeBuild and a new image lands in ECR
   (`aws ecr describe-images --repository-name rosterbot --region us-west-1`).

### C. Cutover — flip AWS on, retire GitHub Actions

Do this only after the block is cleared and at least `optimize`, `waivers`, `claims` have been
hand-run on Fargate and verified (compare their Pushover output to the GHA twins).

1. **Parallel-run check (2–3 days):** hand-run each job, watch CloudWatch + Pushover.
2. **Atomic swap** — enable AWS schedules and retire GHA in the same change window:
   ```bash
   cd infra && cdk deploy -c schedulesEnabled=true -c enableBuild=true --require-approval never
   ```
   ```bash
   git rm .github/workflows/lineup.yml .github/workflows/prospects.yml \
          .github/workflows/gs-check.yml .github/workflows/transactions.yml \
          .github/workflows/waivers.yml .github/workflows/claims.yml \
          .github/workflows/recap.yml .github/workflows/backtest.yml
   ```
3. **Turn off GitHub Pages** (Settings → Pages → Source: None) — recap now serves from CloudFront.
4. **Update docs** — point `README.md` / `CLAUDE.md` GHA sections at this runbook.
5. Confirm the first scheduled AWS run of each job fires and notifies as expected.

> Post-cutover, schedules are **ENABLED by default** — a plain `cdk deploy` keeps the 8 jobs
> running. To pause everything, deploy with `-c schedulesEnabled=false` (explicit kill switch).
> CodeBuild stays absent unless `-c enableBuild=true`.

## Cost control while idle

`cd infra && cdk destroy` tears down everything except the state bucket (RETAIN) and ECR. The
SSM params and state survive, so a later `cdk deploy` brings it back. Destroy to stop the few
dollars/month between experiments.

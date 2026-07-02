# AWS Deployment Runbook

rosterbot runs on AWS (account `476646938644`, region `us-west-1`) as ECS Fargate tasks
launched by EventBridge schedules. Infra is AWS CDK (Go) under `infra/`. See the design
spec `docs/superpowers/specs/2026-06-15-aws-migration-design.md` for rationale.

## Architecture (deployed)

- **ECR** `rosterbot` — container image (Go binary + chromium + aws-cli), ARM64.
- **ECS Fargate** — one task definition (`bot` container, 1 vCPU / 2 GB, ARM64/Graviton).
  Each run syncs state to/from S3 via `entrypoint.sh`, then runs `rosterbot <command>`.
- **EventBridge rules** (×9) — 1:1 port of the old GitHub Actions crons (UTC), plus `ProjectionSite`
  (`cron(0 15 * * ? *)`, ~90 min after `grade`). Gated by the `schedulesEnabled` CDK context flag;
  **disabled by default** so AWS doesn't double-fire while the GHA workflows still exist.
- **S3 state bucket** (`infrastack-statebucket…`) — prefixes `cache/`, `session/`, `claims/`, `backtest/` (projection snapshots, synced by the entrypoint), `analysis/grades/` (Graded Snapshots, NDJSON, written by `grade`), `lineup/` (read-only API JSON, published per-key by the hourly `optimize` run), `athena-results/`.
- **Lineup + control API** — a Go Lambda (`LineupApi`) behind a **Function URL** (output `LineupApiUrl`). Routes: `GET /v1/lineup/today` (from `lineup/today.json`), `GET /v1/runs` + `GET /v1/runs/{id}` (the run ledger under `runs/`), and `POST /v1/jobs/{name}` (launches the existing Fargate task via `ecs:RunTask`, command overridden, `RUN_TRIGGER=manual`). Auth is a Bearer token in SSM (`/rosterbot/ROSTERBOT_API_TOKEN`), enforced in the function (Function URL auth type `NONE`). IAM is least-privilege: read `lineup/*`+`runs/*`, `ssm:GetParameter` on the token, `ecs:RunTask` on the task def, `iam:PassRole` on the task/execution roles. Tasks it launches use a dedicated egress-only SG (`TaskSg`) in the default VPC's public subnets. See the README "Lineup HTTP API" section for the contract.
- **Run ledger** — `entrypoint.sh` writes one JSON object per run to `runs/<invTs>-<taskId>.json` (start = `RUNNING`, end = `SUCCESS`/`FAILED` with exit code + a log tail on failure) via the internal `rosterbot run-ledger` command. The inverted-timestamp key prefix sorts newest-first, so `GET /v1/runs` is a single `MaxKeys` list. Covers scheduled and API-triggered runs alike (`RUN_TRIGGER` distinguishes `schedule` vs `manual`).
- **Analysis Store** — Athena workgroup `rosterbot`, Glue table `rosterbot_analysis.grades` (partition projection on `dt`, no crawler). Query model accuracy with SQL, e.g. `SELECT bucket, avg(abs(diff)) mae FROM rosterbot_analysis.grades WHERE dt >= '2026-06-01' GROUP BY bucket;`.
- **Retention** — the state bucket has versioning **enabled**, so `cache/` overwrites are retained as noncurrent versions (cache history). `backtest/` and `analysis/` are append-only and never expired. Nothing in the stack deletes analysis data; a cost-control lifecycle rule to expire old noncurrent `cache/` versions can be added later if needed.
  The `cache/` prefix is written **per-key, live by the bot** via `cache.Store` (the s3 adapter,
  selected when `STATE_BUCKET` is set) — not bulk-synced by the entrypoint. `session/` (chromedp
  cookie) and `claims/` (ledger+cursor) are still bulk-synced by `entrypoint.sh`. Clear the cache
  with `aws s3 rm s3://<state-bucket>/cache/ --recursive`.
- **S3 site bucket** (`SITE_BUCKET`) + **CloudFront** (`https://d3g6t1hhf4o9r6.cloudfront.net`) — recap site. `entrypoint.sh` invalidates the distribution (`SITE_CF_DIST_ID`) after each sync so a fresh render isn't masked by the CDN cache TTL.
- **S3 report bucket** (`REPORT_BUCKET`) + **CloudFront** (`ReportCdn`, URL in `ReportUrl` stack output) — projection-accuracy dashboard. Written per-run by `projection-site` via `entrypoint.sh` sync; the entrypoint then invalidates the distribution (`REPORT_CF_DIST_ID`). Served from its own CDN distribution, distinct from the recap site.
- **SSM Parameter Store** (`/rosterbot/*`, SecureString) — all secrets, injected as task env.
- **CodeBuild** — on push to `main`, builds + pushes the image to ECR, launches the `projection-site` task (`ecs:RunTask` via `taskDef.GrantRun`, reusing `TaskSg` + public subnets) so the dashboard re-renders immediately with the new image, then runs **`cdk deploy -c enableBuild=true`** so infrastructure changes (schedules, task defs, IAM, Lambda) also ship on merge — not just the image. Before this, a PR touching `infra/` merged green but its infra change sat undeployed until someone ran `cdk deploy` by hand (this is what left the `Archive` schedule inert for ~25h). The `enableBuild=true` in the buildspec is mandatory — without it the deploy would delete the CodeBuild project running it. cdk works through the bootstrap roles, so the build role is granted only `sts:AssumeRole` on `cdk-hnb659fds-*`. The build host ships Go 1.20 + no cdk CLI, so the `install` phase adds Go 1.25 (`GOTOOLCHAIN=auto` upgrades further if a go.mod requires it) and the pinned cdk CLI. Gated by `enableBuild`.

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

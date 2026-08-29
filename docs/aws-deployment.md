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
- **Run ledger** — `entrypoint.sh` writes one JSON object per run to `runledger/<invTs>-<taskId>.json` (start = `RUNNING`, end = `SUCCESS`/`FAILED` with exit code + a log tail on failure) via the internal `rosterbot run-ledger` command. Until rosterbot-432 this lived under `runs/`, shared with per-run output blobs (`runs/<id>/output.json`), so listing had to page past however many of those existed; the ledger now has its own prefix. The inverted-timestamp key prefix sorts newest-first, so `GET /v1/runs` is a cheap, bounded list scoped to `runledger/` alone. Covers scheduled and API-triggered runs alike (`RUN_TRIGGER` distinguishes `schedule` vs `manual`). `RUN_USER_ID` → `run-ledger --user` tags the record with the tenant that ran it; it is unset today, so `user_id` is omitted from every record and reads as the single pre-fan-out tenant. It exists because `internal/opsalert` keys both its streak and its heartbeat decisions on `(command, user_id)`: under per-tenant fan-out every tenant runs the *same command string* into this one ledger, and without the tag a permanently-failing tenant grades healthy on its neighbours' successes while a tenant whose task stops launching is invisible (rosterbot-crq.1).
- **Analysis Store** — Athena workgroup `rosterbot`, Glue table `rosterbot_analysis.grades` (partition projection over `user`/`dt`/`system`, no crawler). `user` is an **injected** projection — tenant ids are opaque WebAuthn handles no enum could enumerate, and an enum would need a stack deploy per signup — so every query must name the tenant in its `WHERE` clause, which is a constraint worth having: it makes an accidental cross-tenant scan impossible to write by omission. Query model accuracy with SQL, e.g.:

  ```sql
  SELECT bucket, avg(abs(diff)) mae
  FROM rosterbot_analysis.grades
  WHERE "user" = '<tenant-id>' AND dt >= '2026-07-01'
  GROUP BY bucket;
  ```

  **The earliest grade days are invisible to Athena and visible to the dashboard — the two disagreeing there is expected, not a bug (rosterbot-h33).** The `grade` command landed 2026-06-16 (`ee79192`) and the `system=` path segment landed 2026-06-30 (`adf153c`, which changed `internal/analysis/grades.go` and this table's template in one commit), so every day graded in that 14-day window was written to `analysis/grades/dt=YYYY-MM-DD/grades.ndjson` with **no `system=` segment**. Those 14 days now exist in *two* places and the difference matters to anything you do about them: the original bare-`dt=` objects (last written 2026-06-30) and the tenant-scoped copies the rosterbot-crq.11 backfill made at `analysis/grades/user=<uid>/dt=YYYY-MM-DD/grades.ndjson` (written 2026-08-14). The bare copies are **frozen orphans** — nothing writes them any more — so they are the wrong copy to read, to measure, or to act on. Neither copy carries a `system=` segment, which is why both are invisible to Athena. Partition projection builds candidate S3 locations by expanding `storage.location.template` (`…/user=${user}/dt=${dt}/system=${system}/`, `infra/infra.go`) across the three projected key ranges, so a key missing a segment matches no generated location and is not a partition of this table at all — never scanned, by any query, at any date range. Adding catalog partitions by hand does not help either: with `projection.enabled=true` Athena reads the template and ignores the catalog's partition metadata, so `MSCK REPAIR TABLE` has nothing to repair. Enumerate the affected objects with:

  ```bash
  aws s3 ls s3://<state-bucket>/analysis/grades/ --recursive | grep -v 'system='
  ```

  That listing also returns the pre-tenant bare `dt=` orphans the rosterbot-crq.11 backfill left behind, which lack `user=` as well. Those being unmatched is *correct* — they duplicate the tenant-scoped copies, and reading both double-counts every row (3508 → 7016 measured, see `docs/analytics-stores.md`) — so do not relax the template to match the un-tenanted path.

  The Go path does read the legacy days, which is why the two surfaces disagree rather than both being short: `internal/analysis`'s reader walks every `grades.ndjson` under the prefix and `SystemAndOriginFromKey` attributes a key with no `system=` segment to `LegacySystem` — the string `depthcharts-ros`, the system the bot actually ran then — marking the row `Legacy` (`internal/analysis/grades.go`, pinned by `grades_test.go`/`reader_test.go`). So `projection-site` → `reports/model.json` → the dashboard's **Projections** tab counts those days in its detail views. Only the head-to-head system comparison drops them (`dropLegacy`, `internal/report/aggregate.go`), and for an unrelated reason: they carry the incumbent's own system string but predate every challenger, so pooled they credited depthcharts-ros with **14 days at MAE 4.8915 (n=298)** against its real 5.1007, dragging the reported figure to 5.0457 (measured in production 2026-08-14). Those 14 days are exactly the set Athena cannot see — 2026-06-16 through 06-29 inclusive.

  Practical consequence: an Athena `COUNT(*)` or MAE over a window reaching back into June undercounts against the dashboard's detail views by those days. Nothing downstream silently inherits the shortfall — there are no automated Athena consumers, only interactive queries — which is why this is a documentation call rather than a production bug. For a question that genuinely needs the full season, read through the Go path instead (`DIAG_GRADES=<dir> go test -tags diag -run TestDiagCompare -v ./internal/report/`, pointed at the tenant prefix, not `analysis/grades/`). Neither is done, and the two remediations differ in both cost and risk:

  - **Widening the template** to match a segment-less key is a `infra/infra.go` change and therefore a CDK deploy. It is also the option to avoid: the template would then match the frozen bare-`dt=` orphans as well, and reading those beside the tenant-scoped copies double-counts every row.
  - **Backfilling** is *not* a deploy. A copy landing at `analysis/grades/user=<uid>/dt=YYYY-MM-DD/system=depthcharts-ros/grades.ndjson` matches the existing template verbatim, so Athena picks it up with no stack change at all — it is an `aws s3 cp` from the tenant-scoped copy (**not** from the frozen orphan).

  **But do not backfill without deciding what it does to the system comparison first.** `Legacy` is derived from the KEY, not stored in the row: `SystemAndOriginFromKey` returns `legacy=false` for any key carrying a `system=` segment. So a backfilled object becomes indistinguishable from a real capture, `dropLegacy` stops excluding it, and `rankSystems` silently re-pools exactly the 14 pre-challenger days described above — moving depthcharts-ros' reported MAE back toward 5.0457 from its real 5.1007. That is a reported model-accuracy number changing with nothing in the diff to explain it, which is a worse outcome than the visible gap this section documents. If the backfill is ever done, `Legacy` has to stop being key-derived first.
- **Retention** — the state bucket has versioning **enabled**, so `cache/` overwrites are retained as noncurrent versions (cache history). `backtest/` and `analysis/` are append-only and never expired. Nothing in the stack deletes analysis data; a cost-control lifecycle rule to expire old noncurrent `cache/` versions can be added later if needed.
  The `cache/` prefix is written **per-key, live by the bot** via `cache.Store` (the s3 adapter,
  selected when `STATE_BUCKET` is set) — not bulk-synced by the entrypoint. `session/` (chromedp
  cookie) and `claims/` (ledger+cursor) are still bulk-synced by `entrypoint.sh`. Clear the cache
  with `aws s3 rm s3://<state-bucket>/cache/ --recursive`.
- **S3 site bucket** (`SITE_BUCKET`) + **CloudFront** (`SiteCdn`, served at **https://recaps.rosterbot.dev**; the default `d3g6t1hhf4o9r6.cloudfront.net` name still works and is **deliberately kept**, as the `SiteCdnDefaultUrl` output — the asymmetry with the dashboard's deprecated default name is a decision, not an oversight. That one was retired because it had become a trap: after rosterbot-jloe.4 moved the RP ID off it, it served a login page that rendered perfectly and then refused the passkey. This site is public, unauthenticated and has no passkey dimension, so its old name is a working bookmark and a 301 would add a CloudFront viewer-request function to remove nothing. Retiring it later is cheap: mirror the dashboard's allowlist-of-canonical-hosts 301 on `SiteCdn`) — recap site. `entrypoint.sh` invalidates the distribution (`SITE_CF_DIST_ID`) after each sync so a fresh render isn't masked by the CDN cache TTL.
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

  The same logs also drive the dashboard's **Views** tab without going through Athena: `projection-site` reads them directly (`internal/recaplog`, via the `RECAP_LOG_BUCKET` env var and a read-only `recap/*` grant on the task role) and publishes the views report alongside the model and gap ones. Athena remains for ad-hoc questions; the tab is the standing view.
- **Projection-accuracy dashboard + team-value tracker, folded into the private dashboard** — `projection-site` writes both halves per run (`internal/report`/`internal/lineupgap`/`internal/recaplog`/`internal/valuereport`/`internal/dynasty` produce the aggregated models, serialized straight to JSON — no server-side HTML render), but they are **published to two different places, by who may read them**:
  - **Private** — the projection-accuracy model, the lineup gap, and recap readership go to `STATE_BUCKET`'s `reports/` prefix (`reports/{model,gap,views}.json`, written directly by the bot through `statestore.ReportsStore()`, not via the `entrypoint.sh` site sync). No CloudFront distribution fronts the state bucket, so the only route to those bytes is the passkey-gated `GET /v1/reports/{name}`, which passes the stored bytes through untouched (`internal/lineupapi/reports.go`, same contract as `GET /v1/trades`). The Lambda gets a read-only `reports/*` grant. **This is an access-control change** (rosterbot-crq.3): these three used to sit under `report/` on the dashboard bucket, world-readable through CloudFront's default behavior. That was the operator's own performance data on an open URL at N=1, and becomes per-manager data on an open URL the moment the league is multi-tenant — where the tenants are competitors in one league, so it is a game-integrity problem, not only a privacy one.
  - **Public** — `value.json` (team HKB-value tracker) and `football.json` (dynasty football standings) are written to `./report` and published via `entrypoint.sh` sync under `DASHBOARD_BUCKET`'s `report/` key prefix (`DASHBOARD_CF_DIST_ID` invalidated after publish) rather than a standalone bucket+CDN — the same bucket/distribution that serves the passkey-gated dashboard SPA (`DashboardBucket`/`DashboardCdn`). Both are league-wide standings that every manager can already read off Fantrax and Sleeper, so publishing them discloses nothing.

  Exposed inside the SPA as the "Projections", "Value" and "Views" nav tabs, which render natively client-side (vendored Chart.js) — no iframe. The old standalone `ReportBucket`/`ReportCdn` (formerly at the `ReportUrl` stack output) has been retired. The `TeamValues` schedule (`cron(30 14 * * ? *)`) writes today's `analysis/team-values/` partition before `ProjectionSite` (15:00 UTC) renders `value.json`.

  **Cutover note:** the three old public objects are removed by the next `ProjectionSite` run without any manual step. `publishSite` mirrors `./report` with `--delete` scoped to the `report/` prefix, and `./report` no longer contains them, so `report/model.json`, `report/gap.json` and `report/views.json` are deleted as orphans and the following CloudFront invalidation clears the edge caches. Until that run completes, the stale copies remain readable — verify with an unauthenticated `curl` after the first post-deploy run.
- **S3 dashboard bucket** (`DashboardBucket`) + **CloudFront** (`DashboardCdn`, served at **https://rosterbot.dev** and **https://dash.rosterbot.dev**, the latter also the `DashboardUrl` output; the default cloudfront.net name is **deprecated** — the SPA 301s from it to the apex, and it remains as the `DashboardCdnDefaultUrl` output only because `/v1/*` still answers there, which is the break-glass if the apex alias or the us-east-1 cert ever fails. See Passkey auth below) — the private control-panel web UI (`web/dashboard/`, static, no build step). One distribution serves both surfaces: its default behavior serves the static files from `DashboardBucket`; an additional `/v1/*` behavior proxies straight to the `LineupApi` Function URL (`CachePolicy.CACHING_DISABLED`, `OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER` so the `Authorization` header passes through), making the browser's calls same-origin with zero CORS configuration anywhere. CodeBuild deploys the stack first, reads `DashboardBucketName` and `DashboardCdnId` from its outputs, then syncs `web/dashboard/` and invalidates the distribution on every push to `main` — including the first build that creates those resources.
- **Passkey auth** — the dashboard's real login is WebAuthn (`internal/lineupapi/webauthn.go`,
  library `github.com/go-webauthn/webauthn`). One `Identity` record (a stable user handle + every
  registered passkey) lives at `webauthn/identity.json` in the state bucket. Sessions are a
  stateless HMAC-signed cookie — no session datastore — signed with a new SSM SecureString,
  `/rosterbot/DASHBOARD_SESSION_SECRET`, bootstrapped the same way as the token:
  `aws ssm put-parameter --name /rosterbot/DASHBOARD_SESSION_SECRET --type SecureString --value '<random 48+ bytes>'`.
  The RP config is **not** passed to the Lambda as a value. `DashboardCdn`'s origin is the Lambda's
  own Function URL, so a direct `apiFn.AddEnvironment(RP_ID, dashboardDist.DistributionDomainName())`
  is a genuine CloudFormation cycle — deferring the call until after the distribution is constructed
  does not help, because it defers when the Go code runs, not the resource graph. The stack instead
  publishes the values into SSM (`/rosterbot/DASHBOARD_RP_ID`, `/rosterbot/DASHBOARD_RP_ORIGIN`) and
  hands the Lambda only the parameter *names* (`RP_ID_PARAM`/`RP_ORIGIN_PARAM`), which are plain
  strings carrying no reference; the Lambda resolves them at cold start. `DASHBOARD_CF_DIST_ID`
  follows the same pattern for the same reason.

  Both RP parameters name **`rosterbot.dev`**, the apex, since rosterbot-jloe.6 — `RP_ID` is the
  apex alone, while `RP_ORIGIN` lists the apex *and* `dash.rosterbot.dev`. The asymmetry is
  deliberate: the RP ID is the identity a credential is bound to (and an apex-scoped credential
  works from any subdomain), whereas the origin is the concrete URL a ceremony came from, and there
  are genuinely two — a browser on `dash.` reports itself, while an iOS native ceremony has no
  browser origin and reports `https://<rpId>`.

  The **cloudfront.net** name is on neither list. It held the RP ID until rosterbot-jloe.4, which
  left it serving a dashboard nobody could sign into — the page rendered, the ceremony failed at the
  biometric prompt. It is now deprecated: the SPA 301s from it to the apex (see the viewer-request
  function in `infra/infra.go`). `/v1/*` deliberately still answers there; see below.

  **Recovering when a passkey is lost.** Not the bootstrap screen: it only appears when
  `login/begin` 404s (no identity has *ever* been registered), and the API-token path through it was
  removed in rosterbot-crq.9 — a user whose identity exists but whose credentials are gone never
  sees it. `rosterbot invite` cannot help either; it always creates a *new* user and fails on the
  email/team uniqueness claim. The one working path is the bearer-token break-glass, which
  authenticates as an admin with no user id before any store read and is indifferent to the RP ID:

  ```bash
  ROSTERBOT_API_TOKEN=$(aws ssm get-parameter --region us-west-1 \
    --name /rosterbot/ROSTERBOT_API_TOKEN --with-decryption --query Parameter.Value --output text)
  curl -X POST -H "Authorization: Bearer $ROSTERBOT_API_TOKEN" \
    https://<DashboardUrl>/v1/tenants/<user-id>/recovery
  ```

  Redeem the returned link on the origin the RP config currently names.
- **SSM Parameter Store** (`/rosterbot/*`, SecureString) — all secrets, injected as task env.
- **CodeBuild** — on push to `main`, builds + pushes the image to ECR, then runs **`cdk deploy --all -c enableBuild=true`** so infrastructure changes (schedules, task defs, IAM, Lambda) ship on merge — not just the image — before publishing the dashboard (reading the deploy's own outputs, see above) and launching the `projection-site` task (`ecs:RunTask` via `taskDef.GrantRun`, reusing `TaskSg` + public subnets) so it re-renders immediately with the new image. Before the `cdk deploy` step existed, a PR touching `infra/` merged green but its infra change sat undeployed until someone ran `cdk deploy` by hand (this is what left the `Archive` schedule inert for ~25h); deploying first also means a broken infra change now fails the build before the dashboard/projection-site steps run, rather than shipping around it. The `--all` is mandatory too, and for a blunter reason: the app has held more than one stack since `InfraCertStack` (the us-east-1 rosterbot.dev certificate), and cdk refuses a bare `cdk deploy` on a multi-stack app — it exits 1, so every push to `main` fails at this step until the flag is there. Nothing local catches that; `go build`/`go vet`/`go test`/`make build-modules`/`make check-pins` are all green and a developer running `cdk deploy --all` by hand sees it work, so it surfaces only in CodeBuild after merge (`infra/buildspec_test.go` now pins it). The `enableBuild=true` is mandatory — without it the deploy would delete the CodeBuild project running it. cdk works through the bootstrap roles, so the build role is granted only `sts:AssumeRole` on `cdk-hnb659fds-*`. The build host ships Go 1.20 + no cdk CLI, so the `install` phase adds Go 1.25 (`GOTOOLCHAIN=auto` upgrades further if a go.mod requires it) and the pinned cdk CLI. Gated by `enableBuild`. Because this fires on `main` only, **a `cdk deploy` run from a feature branch is undone by the next merge to `main`** — see the warning under *Common operations* before deploying from a branch.
- **Ops notifications** — three EventBridge rules target one Lambda (`OpsNotify`, built from `opsnotify/`), which reads `PUSHOVER_USER_KEY` / `PUSHOVER_API_TOKEN` from SSM and posts to the personal ops channel at priority 0.
  - `BuildNotifyRule` matches the `Build` project's `CodeBuild Build State Change` events for `SUCCEEDED` / `FAILED` / `STOPPED`. This catches every failure phase (install/pre_build/build/deploy) — a buildspec curl would miss install/pre_build failures since `post_build` never runs then. Created only under `-c enableBuild=true`; the first build that introduces the rule won't notify itself, every subsequent build does.
  - `TaskFailRule` matches `ECS Task State Change` for any task in the cluster reaching `STOPPED` (rosterbot-naz). Whether that stop was a *failure* is decided in Go, not by the event pattern — see `internal/opsalert`. The Lambda derives a failure streak from the run ledger and pushes only on transitions: first failure, third consecutive, and recovery. A stopped task with no ledger record at all never reached the entrypoint's final write (OOM, image-pull, SIGKILL) and always alerts.
  - `HeartbeatRule` fires every 6 hours at `:15` and asks the opposite question: has each job run *at all* (rosterbot-ys8)? `TaskFailRule` is reactive, so a job that never launches — disabled rule, broken cron, unreachable cluster — emits no event and produces silence that reads exactly like health. The rule carries a constant input (`detail-type: "Rosterbot Heartbeat"`) since a scheduled rule has no detail-type of its own. Per-job tolerances live beside the crons in `infra/infra.go` and reach the Lambda via the `JOB_SCHEDULES` env var — 13h for the hourly `Lineup` (its real worst case is the 11h overnight gap, not 1h), 26h for the daily jobs, 8d for the weekly `Recap`/`Backtest`. It alerts once per outage, not once per tick. **Gated with the job schedules**, so `-c schedulesEnabled=false` pauses it too: a heartbeat that pages about jobs someone deliberately stopped is the alert that teaches people to ignore the channel.
  - **The `OpsNotify` function itself is created unconditionally** — only `BuildNotifyRule` sits behind `enableBuild`, because job-failure alerting must survive a stack deployed without that flag.
  - **Alerts are deduplicated across deliveries** (rosterbot-chs) through small marker objects under the state bucket's `opsalert/` prefix — the one prefix `OpsNotify` may write, and the only write grant it holds. EventBridge can deliver an event twice and async-invoke retries on error, and since the verdict is derived from a ledger that does not change in between, a repeat would push the identical alert again. A 30-day lifecycle rule expires the prefix, far past any retry horizon. Every failure of the marker store degrades to a duplicate alert, never to a missing one.

## Two dependencies that live outside the code

Both are prerequisites no `cdk deploy` recreates. If either disappears, deploys fail in a way
that does not name the real cause.

- **us-east-1 is bootstrapped** (`CDKToolkit`, created 2026-08-18). The stack runs in us-west-1,
  but CloudFront reads a viewer certificate from us-east-1 and nowhere else, so `InfraCertStack`
  lives there — and CDK requires each *region* bootstrapped separately. That region had never been
  used in this account before rosterbot.dev, so it was bootstrapped by hand:
  `cdk bootstrap aws://476646938644/us-east-1`. It brings its own staging bucket and ECR repo, so a
  small line item in an otherwise-unused region is expected, not drift. Delete it and every
  `InfraCertStack` deploy fails on `BootstrapVersionValidation`; because the certificate is a
  cross-region reference, `InfraStack` fails with it.
- **The rosterbot.dev hosted zone** (`Z07503721VYCRE63MNQ5V`) was auto-created by the Route 53
  registrar at registration, with matching NS delegation. `infra/domain.go` **imports** it via
  `HostedZone_FromHostedZoneAttributes` — by attributes rather than `FromLookup`, so synth needs no
  credentials and writes nothing to `cdk.context.json`. CDK must never *create* a zone for this
  domain: a second zone gets its own nameservers that the registrar does not delegate to, so records
  written into it are simply ignored while DNS keeps resolving through the original.
  `TestCertStack_ImportsTheZoneRatherThanCreatingOne` pins the created-zone count at zero.

  Note the two kinds of Route 53 record here behave differently under a deploy, which is worth
  knowing before diagnosing a partial outage. The `_<hash>` ACM validation CNAMEs are written by ACM
  itself (`Validation: FromDns(zone)` only stamps the `HostedZoneId` into the certificate's
  `DomainValidationOptions`), so they are outside CloudFormation's resource graph and no deploy
  removes them. The four alias records are ordinary `AWS::Route53::RecordSet` resources in
  `InfraStack` and follow that stack exactly — which is why a deploy from a revision lacking them
  deletes all four while the validation CNAMEs sit untouched.

## Common operations

> ### ⚠️ A `cdk deploy` from a feature branch is TEMPORARY — the next push to `main` reverts it
>
> **Mechanism.** The CodeBuild webhook fires on a push to `main` and nothing else —
> `FilterGroup_InEventOf(EventAction_PUSH).AndBranchIs(jsii.String("main"))`
> (`infra/infra.go:1066-1068`) — and that build runs `cdk deploy --all` from **main's** `infra/`.
> Within any stack main also declares, CloudFormation deletes the resources your branch added,
> because main's template does not declare them. Correct behaviour: main is the source of truth.
> The problem is that it is invisible, and that it is triggered by someone else's unrelated merge.
>
> **The inverse hazard, for a branch that adds a whole STACK.** `--all` deploys the stacks in
> *main's* app, so a stack that exists only on your branch is not in that set and is never
> touched — it is not reverted, it **survives as an orphan**, drifting and billing, with nothing
> in this repo that lists it. Do not read the paragraph above as "main cleans up after me": it
> cleans up inside stacks it knows about, and knows nothing about the rest. The app already holds
> two stacks (`InfraStack`, `InfraCertStack`), so this is a reachable shape, not a hypothetical.
> If you deployed a branch-only stack, `cdk destroy <StackName>` it yourself before you move on.
>
> **Observed.** 2026-08-18 (rosterbot-jloe.2/.3): four Route 53 alias records for
> `dash`/`recaps.rosterbot.dev` were deployed from a branch and verified live — HTTP 200, valid
> TLS, requests in the CloudFront access log at 14:45:41Z. At **14:45:58Z, 17 seconds later**, a
> build for an unrelated main commit (`ecee15d`) deployed main's infra and deleted all four. Both
> hostnames went NXDOMAIN. Nothing was wrong with either deploy. This is not a window you can
> outrun by being quick.
>
> **What it costs you if you forget.** The failure is silent and inverted: a thing you verified
> stops working later, for a reason unconnected to anything you did. Someone debugging that
> without knowing this behaviour will reasonably suspect DNS propagation, the registrar, or their
> own CDK — none of which are involved. So: **"I deployed it and verified it live" is not evidence
> that a change is deployed.**
>
> **What to do instead.**
>
> - **To preview a change:** `cdk diff --all -c enableBuild=true`. The context flag is not
>   optional. `diff` synthesizes the same template `deploy` would, and the `Build` project exists
>   only inside the `enableBuild` branch (`infra/infra.go:1060`), so omitting it makes the diff
>   report the CodeBuild project running CI as being destroyed — observed, and it buries whatever
>   you were actually trying to read.
> - **To ship a change:** merge it to `main`. That build is the only one that deploys durably.
>   Nothing else does.
> - **To verify something live from a branch:** deploy **with the context flag**, check it
>   *immediately*, and treat the result as a dry run that expires at the next merge to `main` —
>   then re-verify after your branch lands, because that is a different deploy.
>   ```bash
>   cd infra && cdk deploy --all -c enableBuild=true --require-approval never
>   ```
>   **`-c enableBuild=true` is not optional here, and leaving it off is worse than the hazard this
>   whole box is about.** The `Build` project and its webhook exist only inside that context branch
>   (`infra/infra.go:1060-1189`), so a deploy without it *deletes CI*. There is then no build on
>   the next push to `main` — the merge that this box promises will revert your branch state never
>   runs, your branch state becomes permanent, and every subsequent merge silently deploys nothing
>   until someone notices and re-deploys by hand. **Nothing goes red**, and the one check written
>   for exactly this failure is removed by the same deploy: `BuildDriftRule` and the Lambda's
>   `BUILD_PROJECT` env var both live under `if buildProject != nil` (`infra/infra.go:1645-1677`),
>   so they go when the project does. Even were the rule left standing, `handleDrift` logs
>   `check disabled` on an unset `BUILD_PROJECT` and returns nil (`opsnotify/drift.go:177-180`) —
>   blindness is deliberately never a finding there, so it cannot report its own removal.
> - **To find out what is actually deployed right now** (rather than what you last deployed):
>   ```bash
>   aws codebuild list-builds-for-project --project-name <BuildProject> --region us-west-1
>   aws codebuild batch-get-builds --region us-west-1 --ids <id> \
>     --query 'builds[].[buildStatus,resolvedSourceVersion,initiator]'
>   ```
>   `resolvedSourceVersion` names the commit whose `infra/` is live.
>
> There is **deliberately no gate** stopping a branch deploy — no ancestor check, no refusal
> (`grep -rn is-ancestor infra/ buildspec.yml` returns nothing, by design). Branch deploys are how
> infra changes get verified before merge, so the fix for this hazard is this warning rather than
> a block. Tracked as **rosterbot-d5ne**.

```bash
cd infra
export JSII_SILENCE_WARNING_UNTESTED_NODE_VERSION=1

# -c enableBuild=true on BOTH: the Build project running CI exists only under that context flag,
# so a deploy without it deletes CI and a diff without it reports CI as being destroyed.
cdk diff   --all -c enableBuild=true                              # preview changes
cdk deploy --all -c enableBuild=true --require-approval never     # apply
```

From a feature branch, that `deploy` is temporary — see the warning above.

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
cdk deploy --all -c enableBuild=true --require-approval never   # grab LineupApiUrl from the outputs
```

Run that from `main`: without `-c enableBuild=true` it deletes the CodeBuild project, and from a
branch it is reverted by the next merge — see the warning under *Common operations*.

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
   cd infra && cdk deploy --all -c enableBuild=true --require-approval never
   ```
   Run this from `main`; from a branch it is reverted by the next merge (see *Common operations*).
3. Push a commit to `main`; confirm a build appears in CodeBuild and a new image lands in ECR
   (`aws ecr describe-images --repository-name rosterbot --region us-west-1`).

### C. Cutover — flip AWS on, retire GitHub Actions

Do this only after the block is cleared and at least `optimize`, `waivers`, `claims` have been
hand-run on Fargate and verified (compare their Pushover output to the GHA twins).

1. **Parallel-run check (2–3 days):** hand-run each job, watch CloudWatch + Pushover.
2. **Atomic swap** — enable AWS schedules and retire GHA in the same change window:
   ```bash
   cd infra && cdk deploy --all -c schedulesEnabled=true -c enableBuild=true --require-approval never
   ```
   From `main` — a branch deploy of this is reverted by the next merge (see *Common operations*).
   ```bash
   git rm .github/workflows/lineup.yml .github/workflows/prospects.yml \
          .github/workflows/gs-check.yml .github/workflows/transactions.yml \
          .github/workflows/waivers.yml .github/workflows/claims.yml \
          .github/workflows/recap.yml .github/workflows/backtest.yml
   ```
3. **Turn off GitHub Pages** (Settings → Pages → Source: None) — recap now serves from CloudFront.
4. **Update docs** — point `README.md` / `CLAUDE.md` GHA sections at this runbook.
5. Confirm the first scheduled AWS run of each job fires and notifies as expected.

> Post-cutover, schedules are **ENABLED by default** — a plain `cdk deploy --all` keeps the 8 jobs
> running. To pause everything, deploy with `-c schedulesEnabled=false` (explicit kill switch).
> `enableBuild` is the opposite polarity and reads as a trap now that CodeBuild is live: it
> defaults **off**, so a deploy omitting `-c enableBuild=true` does not leave CI absent, it
> **deletes** it. Always pass it.

## Cost control while idle

`cd infra && cdk destroy --all` tears down everything except the state bucket (RETAIN) and ECR. The
SSM params and state survive, so a later `cdk deploy --all -c enableBuild=true` brings it back
(without the flag it comes back without CI). Destroy to stop the few dollars/month between
experiments, and do the restore from `main` — see the warning under *Common operations*.

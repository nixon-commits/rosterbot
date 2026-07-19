# Consolidate projection/value dashboards into the private dashboard

Status: approved
Date: 2026-07-19

## Problem

The projection-accuracy dashboard (`internal/report`) and the team HKB-value
tracker (`internal/valuereport`) currently live on their own S3 bucket +
CloudFront distribution (`ReportBucket`/`ReportCdn`), rendered daily by the
`projection-site` command and linked from the private dashboard SPA
(`web/dashboard/`) only as an external "Projections ↗" link to a separate
domain. The recap site (`SiteBucket`/`SiteCdn`) is intentionally a third,
fully separate public site and is out of scope here — it stays exactly as is.

The goal: fold projection accuracy + team value into the centralized private
dashboard (the passkey-gated SPA at `DashboardBucket`/`DashboardCdn`) as
first-class tabs, and retire the now-redundant `ReportBucket`/`ReportCdn`.

## Decisions

1. **Reuse the existing Go-rendered static HTML as-is.** `internal/report` and
   `internal/valuereport` keep producing self-contained HTML (embedded
   Chart.js, no client-side data fetch). No business logic moves to the
   Lambda API or client JS — that would duplicate `report.Aggregate` /
   `valuereport.BuildModel`'s MAE/bias/RMSE/trend computation for no benefit.
2. **Retire `ReportBucket`/`ReportCdn` outright** once the new path ships —
   no parallel-run period. The old public CloudFront URL
   (`https://d3lfzksum77fj7.cloudfront.net`) stops working.
3. **New tabs render via `<iframe>`** inside the SPA shell (`#projections`,
   `#value` hash routes), so header/nav stay visible and it reads as one app,
   rather than plain links that navigate away from the shell.
4. **No new access-control enforcement.** The static `report/index.html` /
   `report/value.html` files remain fetchable by direct URL without a passkey
   session, same as the SPA's own `index.html`/`app.js` are today (only `/v1/*`
   API calls are session-gated). This is not a regression — the old
   `ReportCdn` URL is fully public today too — just a consolidation, not a
   security hardening. Explicitly deferred; revisit only if the pages should
   actually become private.

## Architecture

`projection-site` is unchanged in behavior: it still writes
`./report/index.html` and `./report/value.html` locally. What changes is the
*publish target* — `entrypoint.sh`'s `sync-up` step (via `cmd/sync.go`) now
mirrors `./report` into `DASHBOARD_BUCKET` under a `report/` key prefix
instead of into a dedicated `REPORT_BUCKET` at the bucket root. Final URLs
become `https://<dashboard-domain>/report/index.html` and
`https://<dashboard-domain>/report/value.html` — the same CloudFront domain
that serves the SPA and proxies `/v1/*` to the Lambda API.

The dashboard SPA gains two hash-routes (`#projections`, `#value`) that each
render a full-height `<iframe src="/report/...">` into the view root. The nav
loses its external "Projections ↗" link (replaced by internal tabs); "Recap ↗"
is untouched — recap stays external and public, unaffected by this change.

## Component changes

**`infra/infra.go`**
- Remove `reportBucket`, `reportDist`, the `ReportBucketName`/`ReportUrl`
  `CfnOutput`s, the `REPORT_BUCKET`/`REPORT_CF_DIST_ID` container env vars,
  `reportBucket.GrantReadWrite(...)`, and `reportDist`'s entry in the
  invalidation-policy ARN list.
- Add `dashboardBucket.GrantReadWrite(taskDef.TaskRole(), nil)` — the bot task
  role currently has no write grant on `DashboardBucket` (only the CodeBuild
  `project` role does, for the SPA's own static assets).
- Add a `cloudfront:CreateInvalidation` policy statement scoped to
  `dashboardDist`'s ARN for the task role.
- Capture the bot container's `ContainerDefinition` (currently the return
  value of `taskDef.AddContainer(...)` is discarded) so `DASHBOARD_CF_DIST_ID`
  can be attached via `.AddEnvironment(...)` after `dashboardDist` exists later
  in the file — mirrors the existing `apiFn.AddEnvironment` pattern used for
  `RP_ID`/`RP_ORIGIN` for the same circular-dependency reason (`dashboardDist`
  is built from `apiFn`'s Function URL, so anything derived from
  `dashboardDist` must be added after both exist). `DASHBOARD_BUCKET` has no
  such ordering constraint (`dashboardBucket` is created up front) and goes
  directly into the container's initial `Environment` map.

**`cmd/sync.go`**
- `publishSite` gains a `prefix` parameter. Publishing into `DashboardBucket`
  must use `"report/"`, not `""` — the mirror is a `--delete` sync
  (`s.Up(..., del=true)`), and `internal/statesync`'s `deleteOrphans` is
  confirmed scoped to the given `prefix`, so an empty prefix against the
  dashboard bucket would treat the SPA's own root-level files (`index.html`,
  `app.js`, etc.) as orphans and delete them. The `dist` → `SiteBucket` call
  keeps `prefix=""` (unchanged, still a bucket-root site).
- The `report` publish call switches from `REPORT_BUCKET`/`REPORT_CF_DIST_ID`
  to `DASHBOARD_BUCKET`/`DASHBOARD_CF_DIST_ID`, prefix `"report/"`.

**`web/dashboard/index.html`**
- Replace `<a href="https://d3lfzksum77fj7.cloudfront.net" ...>Projections
  ↗</a>` with `<a href="#projections">Projections</a>` and add
  `<a href="#value">Value</a>`. The "Recap ↗" external link is untouched.

**`web/dashboard/app.js`**
- Add `"#projections"` and `"#value"` entries to `ROUTES`.

**New `web/dashboard/reportview.js`**
- A small `renderIframe(root, src)` helper (full-height `<iframe>`, no
  border) plus `renderProjections`/`renderValue` wrappers calling it with
  `/report/index.html` / `/report/value.html`.

**Docs**
- `README.md`: drop the `ReportUrl` CDK-output mention from the
  projection-accuracy dashboard bullet; update the "published from `./report`
  to `REPORT_BUCKET`" line to describe the dashboard-hosted path; add a
  one-line local-dev note (`projection-site --out web/dashboard/report` makes
  the iframe tabs work against `serve`'s `localhost:8080` too — `cmd/serve.go`
  already serves all of `web/dashboard/` as static files, so no code change is
  needed there).
- `docs/aws-deployment.md`: rewrite the "S3 report bucket" bullet describing
  `REPORT_BUCKET`/`ReportCdn` to describe the consolidated setup under
  `DashboardBucket`/`DashboardCdn`.
- `CLAUDE.md`: update the `internal/report`/`internal/valuereport` sections'
  mentions of "own S3 bucket `REPORT_BUCKET`" / "separate `REPORT_BUCKET` S3
  bucket" to reflect the new publish target.

## Testing / verification

- `cmd/sync_test.go` (if present) or new coverage for `publishSite`'s prefix
  parameter — verify a dashboard-bucket publish only deletes orphans under
  `report/`, never touching root-level keys (this is really exercising
  `internal/statesync`'s existing `TestUp_WithDelete_RemovesOnlyOrphansUnderPrefix`
  guarantee from the call site, not re-testing statesync itself).
- `go build ./...`, `go vet ./...`, `go mod tidy`.
- CDK: `cdk synth` to confirm the stack still synthesizes after bucket/env
  changes, before `cdk deploy -c enableBuild=true` (per the project's
  standing CDK deploy-flag rule).
- Manual: after deploy, hit `<dashboard-domain>/report/index.html` and
  `.../report/value.html` directly, then confirm the `#projections`/`#value`
  tabs render them in an iframe inside the logged-in SPA shell.
- This step involves deleting real AWS resources (`ReportBucket`'s contents,
  `ReportCdn`) on `cdk deploy` — confirm with the user immediately before
  running that deploy, per the standing rule on hard-to-reverse actions.

## Out of scope

- Recap site (`SiteBucket`/`SiteCdn`) — unchanged.
- Any change to `internal/report` or `internal/valuereport`'s rendering logic.
- Access-control hardening of the report/value static pages (see Decision 4).

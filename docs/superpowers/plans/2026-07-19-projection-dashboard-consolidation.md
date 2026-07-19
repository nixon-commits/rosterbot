# Projection Dashboard Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fold the projection-accuracy dashboard and team-value tracker into the private passkey-gated dashboard SPA as new tabs, and retire the standalone `ReportBucket`/`ReportCdn`.

**Architecture:** `projection-site` keeps rendering `report/index.html` and `report/value.html` exactly as today (no changes to `internal/report`/`internal/valuereport`). The publish target moves from a dedicated `ReportBucket` to the existing `DashboardBucket` under a `report/` key prefix, so the pages live at `<dashboard-domain>/report/index.html` and `/report/value.html`. The dashboard SPA gains two hash-routes (`#projections`, `#value`) that render those URLs in an `<iframe>` inside the existing shell.

**Tech Stack:** Go (CDK Go bindings, Cobra CLI), vanilla JS (no build step, no test framework) for the dashboard SPA.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-19-projection-dashboard-consolidation-design.md`.
- No changes to `internal/report` or `internal/valuereport` rendering logic.
- Recap site (`SiteBucket`/`SiteCdn`) is untouched.
- No new access-control enforcement on `/report/*` — same exposure as today's `ReportCdn` (spec Decision 4).
- `cmd/sync.go`'s `publishSite` for the dashboard bucket MUST use prefix `"report/"`, never `""` — it's a `--delete` mirror and an empty prefix would delete the SPA's own `index.html`/`app.js` (confirmed scoping in `internal/statesync.deleteOrphans`, see `internal/statesync/statesync_test.go:TestUp_WithDelete_RemovesOnlyOrphansUnderPrefix`).
- CDK verification is `cdk synth`, not Go unit tests — `infra/infra_test.go` is an empty commented-out example file; there is no live test harness for the CDK stack in this repo.
- `cmd/sync.go` has no existing test file and none is added here — `statesync.Syncer` requires real AWS config with no fake seam at the `cmd` layer; the logic it wires (`Up`/`deleteOrphans`) is already tested in `internal/statesync`. This matches the pre-existing pattern (no `cmd/sync_test.go` exists today despite `publishSite` already having non-trivial behavior).
- `web/dashboard/` has no test framework or build step (verified: no `package.json`, no test files anywhere under `web/`). Verification for dashboard changes is manual: run `go run . serve` and exercise it in a browser, per CLAUDE.md's UI-change rule.
- **This plan MUST be executed on a feature branch off `main`, never directly on `main`** (standing rule from prior session feedback). Reason: pushing to `main` triggers CodeBuild, which automatically runs `cdk deploy -c enableBuild=true` (`docs/aws-deployment.md`) — and this plan's infra change deletes `ReportBucket` (`RemovalPolicy_DESTROY` + `AutoDeleteObjects`) and its `ReportCdn` CloudFront distribution. Landing on a feature branch + PR means that destructive deploy only fires on merge to `main`, giving a real checkpoint. **Do not merge the PR without the user's explicit go-ahead — flag clearly in the PR description that merging destroys `ReportBucket`'s contents and the `ReportCdn` distribution.**

---

### Task 1: Add a prefix parameter to `publishSite` and repoint the report publish at the dashboard bucket

**Files:**
- Modify: `cmd/sync.go:83-110`

**Interfaces:**
- Produces: `publishSite(ctx context.Context, s *statesync.Syncer, dir, bucket, distID, prefix string)` — the new signature every call site must use.

- [ ] **Step 1: Modify `publishSite`'s signature and body to accept and use a `prefix` param**

Replace lines 92-110 of `cmd/sync.go`:

```go
// publishSite mirrors a local site dir into a bucket under prefix (with
// --delete scoped to that prefix) and invalidates its CloudFront
// distribution. No-op when the dir or bucket is absent. prefix must be ""
// for a bucket-root site (recap's SiteBucket) and a non-empty, trailing-
// slash prefix (e.g. "report/") for a site sharing a bucket with other
// content (the dashboard SPA's own files) — an empty prefix against a
// shared bucket would treat the SPA's files as orphans and delete them,
// since Up's --delete pass only spares keys under the given prefix.
func publishSite(ctx context.Context, s *statesync.Syncer, dir, bucket, distID, prefix string) {
	if bucket == "" {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // nothing rendered this run
	}
	if err := s.Up(ctx, bucket, prefix, dir, true); err != nil {
		warn("publish %s: %v", dir, err)
		return
	}
	if distID != "" {
		if err := s.Invalidate(ctx, distID); err != nil {
			warn("invalidate %s: %v", distID, err)
		}
	}
}
```

- [ ] **Step 2: Update both call sites in `runSyncUp`**

Replace lines 83-88 of `cmd/sync.go`:

```go
	// Publish the static sites when present (recap-site writes ./dist,
	// projection-site writes ./report). recap-site.go publishes a full
	// bucket-root mirror with --delete; projection-site's output is
	// published under a "report/" prefix inside the shared dashboard
	// bucket instead (so the SPA's own files aren't touched by the
	// --delete pass) — followed by a CloudFront invalidation so the new
	// pages aren't masked by the distribution's cache TTL.
	publishSite(ctx, s, "./dist", os.Getenv("SITE_BUCKET"), os.Getenv("SITE_CF_DIST_ID"), "")
	publishSite(ctx, s, "./report", os.Getenv("DASHBOARD_BUCKET"), os.Getenv("DASHBOARD_CF_DIST_ID"), "report/")
```

- [ ] **Step 3: Build and vet**

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/sync.go
git commit -m "$(cat <<'EOF'
Publish projection-site output under the dashboard bucket, not a standalone bucket

publishSite now takes an explicit key prefix so a site can share a bucket
with other content (the dashboard SPA) without its --delete pass treating
that content as orphaned.
EOF
)"
```

---

### Task 2: Retire `ReportBucket`/`ReportCdn` in CDK, grant the bot task role access to `DashboardBucket`/`DashboardCdn`

**Files:**
- Modify: `infra/infra.go`

**Interfaces:**
- Consumes: env vars `DASHBOARD_BUCKET`, `DASHBOARD_CF_DIST_ID` (matches Task 1's `os.Getenv` calls in `cmd/sync.go`).
- Produces: the bot container now has write access to `DashboardBucket` and permission to invalidate `DashboardCdn`.

- [ ] **Step 1: Remove the `reportBucket` declaration**

In `infra/infra.go`, delete lines 69-73:

```go
	// Projection-accuracy dashboard bucket (private; served via its own CDN).
	reportBucket := awss3.NewBucket(stack, jsii.String("ReportBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
	})

```

- [ ] **Step 2: Remove the `ReportBucketName` output**

Delete line 90:

```go
	awscdk.NewCfnOutput(stack, jsii.String("ReportBucketName"), &awscdk.CfnOutputProps{Value: reportBucket.BucketName()})
```

- [ ] **Step 3: Remove `reportDist` and its `CfnOutput`**

Delete lines 120-126 (the `reportDist` distribution):

```go
	reportDist := awscloudfront.NewDistribution(stack, jsii.String("ReportCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(reportBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
	})
```

Delete lines 189-191 (the `ReportUrl` output):

```go
	awscdk.NewCfnOutput(stack, jsii.String("ReportUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), reportDist.DistributionDomainName()}),
	})
```

- [ ] **Step 4: Remove `reportBucket`/`reportDist` from the task role grants and invalidation policy**

Delete line 137:

```go
	reportBucket.GrantReadWrite(taskDef.TaskRole(), nil)
```

Change line 144 from:

```go
		Resources: &[]*string{cfArn(dist), cfArn(reportDist)},
```

to:

```go
		Resources: &[]*string{cfArn(dist)},
```

(This statement now only covers `SiteCdn`; the dashboard distribution's invalidation permission is added separately in Step 6, after `dashboardDist` exists.)

- [ ] **Step 5: Remove `REPORT_BUCKET`/`REPORT_CF_DIST_ID` container env vars and capture the container definition**

Change lines 153-179 from:

```go
	taskDef.AddContainer(jsii.String("bot"), &awsecs.ContainerDefinitionOptions{
		Image: awsecs.ContainerImage_FromEcrRepository(repo, jsii.String("latest")),
		Logging: awsecs.LogDriver_AwsLogs(&awsecs.AwsLogDriverProps{
			LogGroup:     logGroup,
			StreamPrefix: jsii.String("run"),
		}),
		Environment: &map[string]*string{
			"STATE_BUCKET":        stateBucket.BucketName(),
			"SITE_BUCKET":         siteBucket.BucketName(),
			"REPORT_BUCKET":       reportBucket.BucketName(),
			"SITE_CF_DIST_ID":     dist.DistributionId(),
			"REPORT_CF_DIST_ID":   reportDist.DistributionId(),
			"CLAIMS_CURSOR_PATH":  jsii.String(".waivers/last-claims.json"),
			"GS_TRACKING_ENABLED": jsii.String("true"),
		},
		Secrets: &map[string]awsecs.Secret{
			"FANTRAX_USERNAME":     secret("FANTRAX_USERNAME"),
			"FANTRAX_PASSWORD":     secret("FANTRAX_PASSWORD"),
			"FANTRAX_LEAGUE_ID":    secret("FANTRAX_LEAGUE_ID"),
			"FANTRAX_TEAM_ID":      secret("FANTRAX_TEAM_ID"),
			"FANTRAX_IL_SLOTS":     secret("FANTRAX_IL_SLOTS"),
			"FANTRAX_MINORS_SLOTS": secret("FANTRAX_MINORS_SLOTS"),
			"PUSHOVER_USER_KEY":    secret("PUSHOVER_USER_KEY"),
			"PUSHOVER_GROUP_KEY":   secret("PUSHOVER_GROUP_KEY"),
			"PUSHOVER_API_TOKEN":   secret("PUSHOVER_API_TOKEN"),
		},
	})
```

to:

```go
	botContainer := taskDef.AddContainer(jsii.String("bot"), &awsecs.ContainerDefinitionOptions{
		Image: awsecs.ContainerImage_FromEcrRepository(repo, jsii.String("latest")),
		Logging: awsecs.LogDriver_AwsLogs(&awsecs.AwsLogDriverProps{
			LogGroup:     logGroup,
			StreamPrefix: jsii.String("run"),
		}),
		Environment: &map[string]*string{
			"STATE_BUCKET":        stateBucket.BucketName(),
			"SITE_BUCKET":         siteBucket.BucketName(),
			"DASHBOARD_BUCKET":    dashboardBucket.BucketName(),
			"SITE_CF_DIST_ID":     dist.DistributionId(),
			"CLAIMS_CURSOR_PATH":  jsii.String(".waivers/last-claims.json"),
			"GS_TRACKING_ENABLED": jsii.String("true"),
		},
		Secrets: &map[string]awsecs.Secret{
			"FANTRAX_USERNAME":     secret("FANTRAX_USERNAME"),
			"FANTRAX_PASSWORD":     secret("FANTRAX_PASSWORD"),
			"FANTRAX_LEAGUE_ID":    secret("FANTRAX_LEAGUE_ID"),
			"FANTRAX_TEAM_ID":      secret("FANTRAX_TEAM_ID"),
			"FANTRAX_IL_SLOTS":     secret("FANTRAX_IL_SLOTS"),
			"FANTRAX_MINORS_SLOTS": secret("FANTRAX_MINORS_SLOTS"),
			"PUSHOVER_USER_KEY":    secret("PUSHOVER_USER_KEY"),
			"PUSHOVER_GROUP_KEY":   secret("PUSHOVER_GROUP_KEY"),
			"PUSHOVER_API_TOKEN":   secret("PUSHOVER_API_TOKEN"),
		},
	})
```

(`DASHBOARD_CF_DIST_ID` is added in Step 6 below via `botContainer.AddEnvironment`, once `dashboardDist` exists — same circular-dependency shape as `apiFn`'s `RP_ID`/`RP_ORIGIN`.)

- [ ] **Step 6: Grant the task role dashboard bucket write + invalidation, and set `DASHBOARD_CF_DIST_ID`**

Immediately after the existing `dashboardDist` block (originally lines 273-288, unchanged — only what follows it is new), add:

```go
	// The bot task (projection-site) publishes report/index.html + report/value.html
	// under DashboardBucket's "report/" prefix, so it needs write access here too
	// (previously it only wrote to its own now-removed ReportBucket), plus
	// permission to invalidate DashboardCdn after publishing.
	dashboardBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("cloudfront:CreateInvalidation"),
		Resources: &[]*string{cfArn(dashboardDist)},
	}))
	botContainer.AddEnvironment(jsii.String("DASHBOARD_CF_DIST_ID"), dashboardDist.DistributionId())
```

- [ ] **Step 7: Verify the stack still synthesizes**

Run: `cd infra && cdk synth >/dev/null && echo SYNTH_OK`
Expected: `SYNTH_OK` printed, no errors. (This does not touch AWS — `synth` only renders the CloudFormation template locally.)

- [ ] **Step 8: Commit**

```bash
cd /Users/jnixon/rosterbot
git add infra/infra.go
git commit -m "$(cat <<'EOF'
Retire ReportBucket/ReportCdn; grant bot task role access to DashboardBucket

Projection-site now publishes into the dashboard bucket's report/ prefix
instead of a standalone bucket+CDN. Deploying this removes ReportBucket
and ReportCdn from the account.
EOF
)"
```

---

### Task 3: Add Projections/Value tabs to the dashboard SPA

**Files:**
- Create: `web/dashboard/reportview.js`
- Modify: `web/dashboard/app.js`
- Modify: `web/dashboard/index.html`

**Interfaces:**
- Produces: `renderProjections(root)`, `renderValue(root)` — view functions matching the existing `render<Name>(root)` signature used by `renderLineup`/`renderJobs`/`renderRuns`/`renderPasskeys` in `app.js`'s `ROUTES` map.

- [ ] **Step 1: Create `web/dashboard/reportview.js`**

```js
// reportview.js — thin wrapper views for the projection-accuracy and
// team-value pages. Both are self-contained static HTML rendered by the Go
// `projection-site` command (internal/report, internal/valuereport) and
// published under this same CloudFront distribution's "report/" prefix, so
// they're just embedded here rather than re-implemented as API-backed views.
function renderIframe(root, src) {
  const iframe = document.createElement("iframe");
  iframe.src = src;
  iframe.style.width = "100%";
  iframe.style.height = "calc(100vh - 4rem)";
  iframe.style.border = "0";
  root.appendChild(iframe);
}

export function renderProjections(root) {
  renderIframe(root, "/report/index.html");
}

export function renderValue(root) {
  renderIframe(root, "/report/value.html");
}
```

- [ ] **Step 2: Wire the two routes into `app.js`**

In `web/dashboard/app.js`, change the import block (lines 1-8):

```js
import { api, ApiError } from "./api.js";
import { registerPasskey, loginWithPasskey } from "./webauthn.js";
import { renderLineup } from "./lineup.js";
import { renderJobs } from "./jobs.js";
import { renderRuns } from "./runs.js";
import { renderPasskeys } from "./passkeys.js";
import { renderProjections, renderValue } from "./reportview.js";
```

Change the `ROUTES` map (lines 10-15):

```js
const ROUTES = {
  "#lineup": renderLineup,
  "#jobs": renderJobs,
  "#runs": renderRuns,
  "#passkeys": renderPasskeys,
  "#projections": renderProjections,
  "#value": renderValue,
};
```

- [ ] **Step 3: Update the nav in `index.html`**

In `web/dashboard/index.html`, change the `<nav>` block (lines 32-39):

```html
    <nav>
      <a href="#lineup">Lineup</a>
      <a href="#jobs">Jobs</a>
      <a href="#runs">Runs</a>
      <a href="#projections">Projections</a>
      <a href="#value">Value</a>
      <a href="#passkeys">Passkeys</a>
      <a href="https://d3g6t1hhf4o9r6.cloudfront.net" target="_blank" rel="noopener">Recap ↗</a>
    </nav>
```

- [ ] **Step 4: Manually verify locally**

```bash
go run . optimize --dry-run --publish-lineup
go run . projection-site --out web/dashboard/report
ROSTERBOT_API_TOKEN=test ROSTERBOT_SESSION_SECRET=test-secret go run . serve
```

Open `http://localhost:8080/`, set up a passkey (or log in if already set up), then click the "Projections" and "Value" nav tabs. Expected: each loads the corresponding page in an iframe inside the shell (header/nav stay visible), matching what `report/index.html` / `report/value.html` render standalone.

Clean up the local render output afterward so it isn't accidentally committed:

```bash
rm -rf web/dashboard/report
```

- [ ] **Step 5: Commit**

```bash
git add web/dashboard/reportview.js web/dashboard/app.js web/dashboard/index.html
git commit -m "$(cat <<'EOF'
Add Projections/Value tabs to the dashboard SPA

Both render the existing Go-rendered static pages (published under this
distribution's report/ prefix) in an iframe inside the shell, replacing
the external "Projections ↗" link to the now-retired ReportCdn.
EOF
)"
```

---

### Task 4: Update docs to describe the consolidated dashboard

**Files:**
- Modify: `README.md`
- Modify: `docs/aws-deployment.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- None (documentation only).

- [ ] **Step 1: Update `README.md`'s projection-accuracy dashboard bullet**

Find (around line 17):

```markdown
- **Projection-accuracy dashboard** — Daily-updating self-contained HTML dashboard reading from the Analysis Store grades; shows scorecard + 30-day trend, per-position MAE breakdown, calibration chart, and worst-miss table, with auto-generated insights. Live URL is the CDK `ReportUrl` output.
```

Replace with:

```markdown
- **Projection-accuracy dashboard** — Daily-updating self-contained HTML dashboard reading from the Analysis Store grades; shows scorecard + 30-day trend, per-position MAE breakdown, calibration chart, and worst-miss table, with auto-generated insights. Served as the "Projections" tab inside the private dashboard (the CDK `DashboardUrl` output), at `<dashboard-domain>/report/index.html`.
```

- [ ] **Step 2: Update the `report`-publish line in `README.md`**

Find (around line 355):

```markdown
The projection dashboard is published from `./report` to `REPORT_BUCKET` by `entrypoint.sh`.
```

Replace with:

```markdown
The projection dashboard and team-value tracker are published from `./report` into `DASHBOARD_BUCKET`'s `report/` prefix by `entrypoint.sh` — same bucket/CloudFront distribution as the private dashboard SPA, at `<dashboard-domain>/report/index.html` and `/report/value.html`.

For local preview, render into the dashboard's own static dir so `go run . serve` serves it too: `rosterbot projection-site --out web/dashboard/report` (delete `web/dashboard/report/` afterward so it isn't committed).
```

- [ ] **Step 3: Update `docs/aws-deployment.md`'s report bucket bullet**

Find (around line 30):

```markdown
- **S3 report bucket** (`REPORT_BUCKET`) + **CloudFront** (`ReportCdn`, URL in `ReportUrl` stack output) — projection-accuracy dashboard (`index.html`) plus the team HKB-value tracker (`value.html`, cross-linked), both written per-run by `projection-site` via `entrypoint.sh` sync; the entrypoint then invalidates the distribution (`REPORT_CF_DIST_ID`). Served from its own CDN distribution, distinct from the recap site. The `TeamValues` schedule (`cron(30 14 * * ? *)`) writes today's `analysis/team-values/` partition before `ProjectionSite` (15:00 UTC) renders `value.html`.
```

Replace with:

```markdown
- **Projection-accuracy dashboard + team-value tracker, folded into the private dashboard** — `index.html` (projection accuracy) and `value.html` (team HKB-value tracker, cross-linked) are written per-run by `projection-site` via `entrypoint.sh` sync, published under `DASHBOARD_BUCKET`'s `report/` key prefix (`DASHBOARD_CF_DIST_ID` invalidated after publish) rather than a standalone bucket+CDN — the same bucket/distribution that serves the passkey-gated dashboard SPA (`DashboardBucket`/`DashboardCdn`). Exposed inside the SPA as the "Projections" and "Value" nav tabs (each an `<iframe>` onto `/report/index.html` / `/report/value.html`); the pages themselves remain fetchable by direct URL without a passkey session, same exposure as before the consolidation — this was a consolidation, not an access-control change. The old standalone `ReportBucket`/`ReportCdn` (formerly at the `ReportUrl` stack output) has been retired. The `TeamValues` schedule (`cron(30 14 * * ? *)`) writes today's `analysis/team-values/` partition before `ProjectionSite` (15:00 UTC) renders `value.html`.
```

- [ ] **Step 4: Update `CLAUDE.md`'s `internal/report` architecture note**

In `CLAUDE.md`, find this sentence in the `internal/report` paragraph:

```markdown
The daily `ProjectionSite` EventBridge schedule (`cron(0 15 * * ? *)`, ~90 min after `grade`) runs `projection-site`, which reads from the Analysis Store, calls `report.Render`, and writes `./report/index.html`, which `entrypoint.sh` syncs to the separate `REPORT_BUCKET` S3 bucket (fronted by its own `ReportCdn` CloudFront distribution, distinct from the recap's `SITE_BUCKET`).
```

Replace with:

```markdown
The daily `ProjectionSite` EventBridge schedule (`cron(0 15 * * ? *)`, ~90 min after `grade`) runs `projection-site`, which reads from the Analysis Store, calls `report.Render`, and writes `./report/index.html`, which `entrypoint.sh` syncs under `DASHBOARD_BUCKET`'s `report/` key prefix — the same bucket/`DashboardCdn` CloudFront distribution as the private dashboard SPA (`web/dashboard/`), where it's surfaced as the "Projections" tab. Distinct from the recap's `SITE_BUCKET`, which remains its own separate site.
```

In the `internal/valuereport` paragraph, find:

```markdown
`cmd/projection-site.go`'s `renderValueSite` reads the store (reader chosen by `STATE_BUCKET`) and writes `value.html` alongside `index.html` (cross-linked), soft-failing independently so a team-value hiccup never blocks the accuracy dashboard deploy.
```

Replace with:

```markdown
`cmd/projection-site.go`'s `renderValueSite` reads the store (reader chosen by `STATE_BUCKET`) and writes `value.html` alongside `index.html` (cross-linked) — both published under `DASHBOARD_BUCKET`'s `report/` prefix and surfaced as the dashboard SPA's "Value" tab — soft-failing independently so a team-value hiccup never blocks the accuracy dashboard deploy.
```

- [ ] **Step 5: Commit**

```bash
git add README.md docs/aws-deployment.md CLAUDE.md
git commit -m "docs: reflect projection/value dashboard consolidation into the private dashboard"
```

---

### Task 5: Push the branch and open a PR (does NOT merge)

**Files:** none (git/GitHub operations only).

- [ ] **Step 1: Confirm all commits are on a feature branch, not `main`**

```bash
git branch --show-current
```

Expected: something like `projection-dashboard-consolidation`, NOT `main`. If this prints `main`, STOP — do not push; create a branch from the current state first (`git checkout -b projection-dashboard-consolidation`) before continuing.

- [ ] **Step 2: Push the branch**

```bash
git push -u origin projection-dashboard-consolidation
```

- [ ] **Step 3: Open a PR with an explicit warning about the destructive deploy**

```bash
gh pr create --title "Consolidate projection/value dashboards into the private dashboard" --body "$(cat <<'EOF'
## Summary
- Projection-accuracy dashboard and team-value tracker now render inside the private dashboard SPA as "Projections"/"Value" tabs (iframe onto `/report/index.html` / `/report/value.html`), instead of a separate public CloudFront site.
- `internal/report`/`internal/valuereport` rendering logic is unchanged; only the publish target moved (dashboard bucket's `report/` prefix instead of a standalone `ReportBucket`).
- Recap site is untouched.

## ⚠️ Merging this destroys AWS resources
This PR removes `ReportBucket` and `ReportCdn` from `infra/infra.go`. Both have
`RemovalPolicy_DESTROY` + `AutoDeleteObjects`, so **merging to `main` triggers
CodeBuild's automatic `cdk deploy -c enableBuild=true`, which will delete the
bucket's contents and the CloudFront distribution** — the old
`https://d3lfzksum77fj7.cloudfront.net` URL stops working immediately.
Do not merge until you're ready for that.

## Test plan
- [ ] `go build ./...` / `go vet ./...` clean
- [ ] `cd infra && cdk synth` succeeds
- [ ] Locally verified `#projections`/`#value` tabs render via `go run . serve` (see Task 3)
- [ ] Reviewed doc updates in README.md / docs/aws-deployment.md / CLAUDE.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Report the PR URL to the user and stop**

Do not merge the PR. Merging is the action that deletes `ReportBucket`/`ReportCdn` on the next CodeBuild run — that decision belongs to the user, at a time of their choosing.

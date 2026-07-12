# CodeBuild → Pushover notifications

**Bead:** rosterbot-00j
**Date:** 2026-07-12
**Status:** approved

## Problem

Pushes to `main` (direct or merged PR) trigger the AWS CodeBuild project (`infra.go`,
gated by `-c enableBuild=true`) that builds+pushes the image, re-renders the projection
dashboard, and runs `cdk deploy`. There is no notification when that build succeeds or
fails, so a red build (e.g. a broken image or a failed `cdk deploy`) goes unnoticed until
something downstream breaks.

## Goal

Send a Pushover notification for every terminal CodeBuild outcome — `SUCCEEDED`, `FAILED`,
`STOPPED` — for the `Build` project.

## Approach

**EventBridge rule on `CodeBuild Build State Change` → a small Go Lambda → `notify.SendPushover`.**

Chosen over the two alternatives:

- **buildspec inline curl** — rejected: `post_build` never runs if `install`/`pre_build`
  fail, so it is blind to exactly the failures most worth alerting on.
- **EventBridge → API Destination (no Lambda)** — rejected: Pushover creds would have to
  move from SSM Parameter Store to Secrets Manager, and input-transformer message templating
  is weaker than formatting in Go.

The Lambda approach catches every failure phase and reuses the existing `internal/notify`
and SSM-read patterns.

## Components

### `buildnotify/` — new Go module (sibling to `lambda/`)

Mirrors `lambda/`'s module pattern: its own `go.mod` with `module
github.com/nixon-commits/rosterbot/buildnotify`, `go 1.26.1`, `replace
github.com/nixon-commits/rosterbot => ../` (so it may import `internal/notify`), and the same
`replace` for `go-fantrax` that the parent uses (pulled in transitively).

- `main.go`
  - `func handler(ctx, ev events.CodeBuildEvent) error`
  - Cold-start (in `main`, before `lambda.Start`) reads `/rosterbot/PUSHOVER_USER_KEY` and
    `/rosterbot/PUSHOVER_API_TOKEN` via `ssm.GetParameter{WithDecryption: true}` — same call
    shape as `lambda/main.go:92`. Fetched once and closed over by the handler.
  - Handler builds `(title, body)` from `formatMessage(ev)` and calls
    `notify.SendPushover(userKey, apiToken, title, body)`. All notifications priority 0
    (the `SendPushover` default), personal ops channel (`PUSHOVER_USER_KEY`).
  - `formatMessage(ev events.CodeBuildEvent) (title, body string)` — pure, no I/O:
    - status → emoji: `SUCCEEDED`→✅, `FAILED`→❌, `STOPPED`→⏹️, other→ℹ️
    - title: `Rosterbot build <STATUS>`
    - body: `<emoji> <sourceVersion short SHA (≤12 chars)> · <console deep-link>`
    - console link:
      `https://<region>.console.aws.amazon.com/codesuite/codebuild/<account>/projects/<project>/build/<url-encoded buildId>`
      built from the event's region / account / project-name / build-id fields (exact
      `events.CodeBuildEvent` field paths — e.g. `AWSRegion`, `AccountID`,
      `Detail.ProjectName`, `Detail.BuildID`, `Detail.AdditionalInformation.SourceVersion` —
      confirmed against the `aws-lambda-go` version pinned in `buildnotify/go.mod` during
      implementation). If a field is empty, degrade gracefully (omit the link rather than
      emit a broken URL).

### CDK wiring — `infra/infra.go`, inside the existing `enableBuild` block

After `project` is created (so we can reference `project.ProjectName()`):

- `buildNotifyFn := awscdklambdagoalpha.NewGoFunction(stack, "BuildNotify", …)`
  - `Entry: ../buildnotify`, `Runtime: PROVIDED_AL2023`, `Architecture: ARM_64` (matches the
    existing API GoFunction).
  - Grant `ssm:GetParameter` on the two Pushover params:
    `arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_USER_KEY` and
    `.../PUSHOVER_API_TOKEN` (KMS decrypt for a SecureString comes via the account default key;
    the existing API lambda reads `ROSTERBOT_API_TOKEN` the same way).
- `awsevents.NewRule(stack, "BuildNotifyRule", …)` with an `EventPattern`:
  - `Source: ["aws.codebuild"]`
  - `DetailType: ["CodeBuild Build State Change"]`
  - `Detail: { "project-name": [project.ProjectName()], "build-status": ["SUCCEEDED","FAILED","STOPPED"] }`
  - `Targets: [ awseventstargets.NewLambdaFunction(buildNotifyFn) ]`

No `awscdk.NewCfnOutput` required.

## Data flow

```
push to main → CodeBuild "Build" runs
  → (terminal) CodeBuild emits "CodeBuild Build State Change" to default event bus
    → BuildNotifyRule matches (project + status)
      → BuildNotify lambda
        → reads SSM PUSHOVER_USER_KEY / PUSHOVER_API_TOKEN (cold start)
        → formatMessage(ev)
        → notify.SendPushover(...)  → phone
```

## Error handling

- SSM read failure at cold start → `log.Fatal` (Lambda init error, visible in CloudWatch;
  the alert path being down is itself worth a hard failure so it surfaces).
- `SendPushover` returns an error → handler logs and returns the error (EventBridge will
  retry per its default async-invoke policy); a persistently failing Pushover is a
  swallowed-alert risk, so surfacing it via retry/log is preferred over silent success.
- Empty/malformed event fields → `formatMessage` degrades (omits link), never panics.

## Testing

- `buildnotify/main_test.go` — table test for `formatMessage`:
  - each status → correct emoji + title
  - long `SourceVersion` truncated to ≤12 chars
  - all link fields present → well-formed console URL contains project + build id
  - missing region/account → body still returned, link omitted
- SSM + HTTP stay outside the tested unit (handler wiring is thin).
- `cd buildnotify && go vet ./...` and `go mod tidy` before deploy.

## Deployment / bootstrap

- The new Lambda + rule are created by the `cdk deploy` that already runs in `buildspec.yml`
  `post_build`. **No buildspec change.**
- The first build that introduces this change won't notify itself (the rule doesn't exist
  until that build's own deploy step completes). Every subsequent build notifies. Acceptable.
- One-time before first deploy: `cd buildnotify && go mod tidy` (network) to populate `go.sum`.

## Out of scope (YAGNI)

- Per-status priority / quiet successes (chosen: all priority 0).
- Notifying the group/broadcast channel (chosen: personal ops `PUSHOVER_USER_KEY`).
- Notifying on `IN_PROGRESS` / phase-change events.
- Retro-notifying the introducing build.

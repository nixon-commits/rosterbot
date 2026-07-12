# CodeBuild → Pushover Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Send a Pushover notification for every terminal CodeBuild outcome (SUCCEEDED / FAILED / STOPPED) of the AWS `Build` project.

**Architecture:** An EventBridge rule matches `CodeBuild Build State Change` events for the project and targets a small Go Lambda (`buildnotify/`), which reads the Pushover creds from SSM at cold start and calls `internal/notify.SendPushover`. Wired in CDK inside the existing `enableBuild` block; created by the `cdk deploy` that already runs in `buildspec.yml`.

**Tech Stack:** Go (aws-lambda-go, aws-sdk-go-v2/ssm), AWS CDK v2 (Go — `awscdklambdagoalpha`, `awsevents`, `awseventstargets`), Pushover.

## Global Constraints

- Module path must be under `github.com/nixon-commits/rosterbot/` so it may import `internal/`.
- `buildnotify/` is a **separate Go module** (own `go.mod`), like `lambda/` — the root `go build ./...` / `go vet ./...` do not descend into it.
- Go version floor: `go 1.26.1` (match `lambda/go.mod`).
- Lambda runtime `PROVIDED_AL2023`, architecture `ARM_64` (match the existing `LineupApi` GoFunction).
- Account/region: `476646938644` / `us-west-1`. SSM param ARNs: `arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_USER_KEY` and `.../PUSHOVER_API_TOKEN`.
- All notifications priority 0 (the `SendPushover` default), personal ops channel (`PUSHOVER_USER_KEY`).
- The CDK additions go **inside** the existing `if v, ok := ...TryGetContext("enableBuild")...; v == "true"` block in `infra/infra.go` (so they reference `project`).
- `notify.SendPushover(userKey, apiToken, title, message string) error` is the existing helper — do not reimplement the HTTP call.

---

### Task 1: `buildnotify/` Go module (message formatter + Lambda handler)

**Files:**
- Create: `buildnotify/message.go` (pure `formatMessage` + `shortSHA`)
- Create: `buildnotify/message_test.go`
- Create: `buildnotify/main.go` (handler + SSM cold-start read)
- Create: `buildnotify/go.mod`
- Create: `buildnotify/.gitignore` (ignore the built binary, mirroring `lambda/.gitignore`)

**Interfaces:**
- Consumes: `github.com/aws/aws-lambda-go/events` (`CodeBuildEvent`, `CodeBuildPhaseStatusSucceeded/Failed/Stopped`); `internal/notify.SendPushover`.
- Produces: `formatMessage(ev events.CodeBuildEvent) (title, body string)` — pure, used by the handler and the test.

Confirmed `events.CodeBuildEvent` field paths (aws-lambda-go v1.49.0):
`ev.Detail.BuildStatus` (`CodeBuildPhaseStatus`, a string), `ev.Detail.ProjectName`,
`ev.Detail.AdditionalInformation.SourceVersion` (commit SHA for a GitHub source),
`ev.Detail.AdditionalInformation.Logs.DeepLink` (ready CloudWatch Logs link).

- [ ] **Step 1: Write the failing test** — `buildnotify/message_test.go`

```go
package main

import (
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func evt(status events.CodeBuildPhaseStatus, sha, link string) events.CodeBuildEvent {
	var ev events.CodeBuildEvent
	ev.Detail.BuildStatus = status
	ev.Detail.ProjectName = "Build"
	ev.Detail.AdditionalInformation.SourceVersion = sha
	ev.Detail.AdditionalInformation.Logs.DeepLink = link
	return ev
}

func TestFormatMessage(t *testing.T) {
	tests := []struct {
		name        string
		ev          events.CodeBuildEvent
		wantTitle   string
		wantEmoji   string
		wantInBody  []string // substrings that must be present
		wantNotBody []string // substrings that must be absent
	}{
		{
			name:       "succeeded",
			ev:         evt(events.CodeBuildPhaseStatusSucceeded, "abc1234def5678", "https://logs.example/deep"),
			wantTitle:  "Rosterbot build SUCCEEDED",
			wantEmoji:  "✅",
			wantInBody: []string{"abc1234", "https://logs.example/deep"},
			// long SHA truncated to 7 chars — the 8th char must not appear glued on
			wantNotBody: []string{"abc1234d"},
		},
		{
			name:       "failed",
			ev:         evt(events.CodeBuildPhaseStatusFailed, "deadbeef", "https://logs.example/x"),
			wantTitle:  "Rosterbot build FAILED",
			wantEmoji:  "❌",
			wantInBody: []string{"deadbee", "https://logs.example/x"},
		},
		{
			name:       "stopped",
			ev:         evt(events.CodeBuildPhaseStatusStopped, "cafef00d", ""),
			wantTitle:  "Rosterbot build STOPPED",
			wantEmoji:  "⏹️",
			wantInBody:  []string{"cafef00"},
			wantNotBody: []string{" · "}, // no link separator when DeepLink empty
		},
		{
			name:        "no sha, no link",
			ev:          evt(events.CodeBuildPhaseStatusSucceeded, "", ""),
			wantTitle:   "Rosterbot build SUCCEEDED",
			wantEmoji:   "✅",
			wantNotBody: []string{" · "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, body := formatMessage(tt.ev)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if !strings.HasPrefix(body, tt.wantEmoji) {
				t.Errorf("body %q does not start with emoji %q", body, tt.wantEmoji)
			}
			for _, s := range tt.wantInBody {
				if !strings.Contains(body, s) {
					t.Errorf("body %q missing %q", body, s)
				}
			}
			for _, s := range tt.wantNotBody {
				if strings.Contains(body, s) {
					t.Errorf("body %q should not contain %q", body, s)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Create `buildnotify/go.mod`**

```
module github.com/nixon-commits/rosterbot/buildnotify

go 1.26.1

require (
	github.com/aws/aws-lambda-go v1.49.0
	github.com/aws/aws-sdk-go-v2/config v1.32.28
	github.com/aws/aws-sdk-go-v2/service/ssm v1.62.0
	github.com/nixon-commits/rosterbot v0.0.0
)

replace github.com/nixon-commits/rosterbot => ../

replace github.com/pmurley/go-fantrax => github.com/nixon-commits/go-fantrax v0.1.14-0.20260707023508-e5d491da74a1
```

Then populate `go.sum` and indirects:

Run: `cd buildnotify && go mod tidy`
Expected: succeeds, writes `buildnotify/go.sum`. (If `go mod tidy` reports the `go-fantrax` replace as unused, that is harmless — `internal/notify` is stdlib-only so `go-fantrax` may not enter this module's graph; leave the replace in for parity with `lambda/go.mod`.)

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd buildnotify && go test ./...`
Expected: FAIL — `undefined: formatMessage`.

- [ ] **Step 4: Write `buildnotify/message.go`**

```go
package main

import (
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// formatMessage renders a Pushover (title, body) from a CodeBuild state-change
// event. Pure — no I/O — so it is unit-tested directly.
func formatMessage(ev events.CodeBuildEvent) (title, body string) {
	status := string(ev.Detail.BuildStatus)
	var emoji string
	switch ev.Detail.BuildStatus {
	case events.CodeBuildPhaseStatusSucceeded:
		emoji = "✅"
	case events.CodeBuildPhaseStatusFailed:
		emoji = "❌"
	case events.CodeBuildPhaseStatusStopped:
		emoji = "⏹️"
	default:
		emoji = "ℹ️"
	}

	title = "Rosterbot build " + status

	parts := []string{emoji}
	if sha := shortSHA(ev.Detail.AdditionalInformation.SourceVersion); sha != "" {
		parts = append(parts, sha)
	}
	body = strings.Join(parts, " ")
	if link := ev.Detail.AdditionalInformation.Logs.DeepLink; link != "" {
		body += " · " + link
	}
	return title, body
}

// shortSHA trims a git commit SHA to its 7-char prefix; short/empty values pass
// through unchanged.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd buildnotify && go test ./...`
Expected: PASS (`ok  github.com/nixon-commits/rosterbot/buildnotify`).

- [ ] **Step 6: Write `buildnotify/main.go` (handler + SSM cold-start read)**

```go
// Command buildnotify is the AWS Lambda that turns a CodeBuild "Build State
// Change" event into a Pushover notification. It is wired by the CDK GoFunction
// in infra/ (Entry: ../buildnotify) and invoked by the BuildNotifyRule
// EventBridge rule. Separate module so aws-lambda-go stays out of the main
// rosterbot binary's dependency graph (mirrors lambda/).
package main

import (
	"context"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/nixon-commits/rosterbot/internal/notify"
)

func main() {
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load aws config: %v", err)
	}
	ssmc := ssm.NewFromConfig(cfg)
	// Read once at cold start; the alert path being down is itself worth a hard
	// init failure so it surfaces in CloudWatch.
	userKey := mustParam(ctx, ssmc, "/rosterbot/PUSHOVER_USER_KEY")
	apiToken := mustParam(ctx, ssmc, "/rosterbot/PUSHOVER_API_TOKEN")

	lambda.Start(func(_ context.Context, ev events.CodeBuildEvent) error {
		title, body := formatMessage(ev)
		if err := notify.SendPushover(userKey, apiToken, title, body); err != nil {
			// Return the error so EventBridge async-invoke retries rather than
			// silently swallowing a missed alert.
			log.Printf("send pushover: %v", err)
			return err
		}
		return nil
	})
}

func mustParam(ctx context.Context, c *ssm.Client, name string) string {
	withDecryption := true
	out, err := c.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &name,
		WithDecryption: &withDecryption,
	})
	if err != nil {
		log.Fatalf("read %s: %v", name, err)
	}
	return *out.Parameter.Value
}
```

- [ ] **Step 7: Create `buildnotify/.gitignore`**

```
/buildnotify
```

(Ignores the compiled binary if built locally; mirrors `lambda/.gitignore`, which ignores `/lambda`.)

- [ ] **Step 8: Vet + build the module**

Run: `cd buildnotify && go vet ./... && go build ./...`
Expected: no output, exit 0. (The `go build` produces a `buildnotify` binary that `.gitignore` excludes; delete it if created: `rm -f buildnotify/buildnotify`.)

- [ ] **Step 9: Commit**

```bash
git add buildnotify/
git commit -m "feat(buildnotify): CodeBuild-event -> Pushover Lambda (rosterbot-00j)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FwEf55Sn5Xc2xRHqAxkvc3"
```

---

### Task 2: CDK wiring — GoFunction + EventBridge rule

**Files:**
- Modify: `infra/infra.go` — inside the `enableBuild` block (after `project` and its grants, before `awscdk.NewCfnOutput(... "BuildProject" ...)` at ~line 297).
- Modify: `docs/aws-deployment.md` — document the build-notification path.
- Modify: `CLAUDE.md` — one line in the AWS/GHA-superseded area noting build-outcome Pushover alerts.

**Interfaces:**
- Consumes: `project` (`awscodebuild.Project`) → `project.ProjectName()`; already-imported `awscdklambdagoalpha`, `awsevents`, `awseventstargets`, `awslambda`, `awsiam`, `awscdk`, `jsii`.
- Produces: no exported symbols (infra side effects only).

- [ ] **Step 1: Add the GoFunction + rule inside the `enableBuild` block**

Insert immediately before the `awscdk.NewCfnOutput(stack, jsii.String("BuildProject"), ...)` line (~297), still inside the `if ... enableBuild ... == "true" {` block:

```go
		// Pushover on every terminal build outcome (rosterbot-00j). An EventBridge
		// rule on the project's "CodeBuild Build State Change" events targets a
		// small Go lambda that reads the Pushover creds from SSM and posts. This
		// catches every failure phase (install/pre_build/build/deploy) + success —
		// unlike a buildspec curl, which never runs if install/pre_build fail.
		buildNotifyFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("BuildNotify"), &awscdklambdagoalpha.GoFunctionProps{
			Entry:        jsii.String("../buildnotify"),
			Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
			Architecture: awslambda.Architecture_ARM_64(),
			Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		})
		buildNotifyFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
			Actions: jsii.Strings("ssm:GetParameter"),
			Resources: jsii.Strings(
				"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_USER_KEY",
				"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_API_TOKEN",
			),
		}))
		awsevents.NewRule(stack, jsii.String("BuildNotifyRule"), &awsevents.RuleProps{
			EventPattern: &awsevents.EventPattern{
				Source:     jsii.Strings("aws.codebuild"),
				DetailType: jsii.Strings("CodeBuild Build State Change"),
				Detail: &map[string]interface{}{
					"project-name": []interface{}{project.ProjectName()},
					"build-status": []interface{}{"SUCCEEDED", "FAILED", "STOPPED"},
				},
			},
			Targets: &[]awsevents.IRuleTarget{
				awseventstargets.NewLambdaFunction(buildNotifyFn, &awseventstargets.LambdaFunctionProps{}),
			},
		})
```

- [ ] **Step 2: Compile-check the infra module**

Run: `cd infra && go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Structural check via `cdk synth` (requires cdk CLI + network for GoFunction bundling)**

Run: `cd infra && cdk synth -c enableBuild=true 2>/dev/null | grep -E "BuildNotify|AWS::Events::Rule|aws.codebuild" | head`
Expected: output includes the `BuildNotify` Lambda logical id, an `AWS::Events::Rule`, and `aws.codebuild` in the event pattern.
(If `cdk` is unavailable locally, this is exercised by the in-build `cdk deploy` on merge; the `go build`/`go vet` gate in Step 2 is the required check.)

- [ ] **Step 4: Document the notification path**

In `docs/aws-deployment.md`, add a short subsection near the CodeBuild/image-build docs:

```markdown
### Build notifications

A push to `main` runs the `Build` CodeBuild project. An EventBridge rule
(`BuildNotifyRule`) matches its `CodeBuild Build State Change` events for
`SUCCEEDED` / `FAILED` / `STOPPED` and invokes the `BuildNotify` Lambda
(`buildnotify/`), which reads `PUSHOVER_USER_KEY` / `PUSHOVER_API_TOKEN` from SSM
and posts a Pushover alert. All outcomes notify at priority 0 on the personal ops
channel. The rule and Lambda are created only under `-c enableBuild=true`, and the
first build that introduces them will not notify itself (the rule does not exist
until that build's own `cdk deploy` completes).
```

In `CLAUDE.md`, in the AWS deployment area (the superseded-GHA section that describes EventBridge schedules), add one line:

```markdown
> A push to `main` also triggers a Pushover alert on build success/failure via an
> EventBridge rule on CodeBuild state-change events → the `buildnotify/` Lambda.
```

- [ ] **Step 5: Commit**

```bash
git add infra/infra.go docs/aws-deployment.md CLAUDE.md
git commit -m "feat(infra): EventBridge rule + Lambda for CodeBuild Pushover alerts (rosterbot-00j)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01FwEf55Sn5Xc2xRHqAxkvc3"
```

---

## Verification (whole feature)

- [ ] `cd buildnotify && go test ./... && go vet ./...` — green.
- [ ] `cd infra && go build ./... && go vet ./...` — green.
- [ ] (If cdk available) `cd infra && cdk synth -c enableBuild=true` includes `BuildNotify` + an `AWS::Events::Rule` with `source: aws.codebuild`.
- [ ] `git status` clean on branch `feat/codebuild-pushover-notify`.
- [ ] `bd close rosterbot-00j` after merge/deploy verification.

## Post-merge (real-world verification)

The rule/lambda ship via the in-build `cdk deploy`. After the **next** push to `main`
following this one (the introducing build won't self-notify), confirm a Pushover
alert arrives titled `Rosterbot build SUCCEEDED`. To force a failure test, push a
commit that breaks the image build and confirm a `Rosterbot build FAILED` alert with
a CloudWatch deep-link.

## Notes / gotchas

- Root `go build ./...` / `go vet ./...` **do not** descend into `buildnotify/` (nested module) — always `cd buildnotify` for its checks, same as `lambda/`.
- `make run-all` is unaffected (no new CLI command; nothing to append).
- EventBridge `Detail` map values must be **arrays** (`[]interface{}{...}`) — an EventBridge content filter matches when the field value is in the array.

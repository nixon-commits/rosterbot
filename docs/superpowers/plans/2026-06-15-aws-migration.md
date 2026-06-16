# AWS Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move rosterbot's 8 scheduled jobs and its storage off GitHub Actions onto AWS (ECS Fargate + EventBridge + S3 + CodeBuild), defined in AWS CDK (Go), for ~$3–5/month.

**Architecture:** One container image (Go binary + chromium) in ECR, one Fargate task definition, 8 EventBridge schedule rules each launching the task with a different `command` override (a 1:1 port of the 8 workflows). A shell entrypoint syncs state to/from S3 around each run. CodeBuild builds and pushes the image. Recap site goes to S3 + CloudFront. All infra is one CDK-Go app under `infra/`.

**Tech Stack:** Go 1.26, Docker, AWS CDK v2 (Go), ECS Fargate, EventBridge (`events.Rule` + `EcsTask` target), ECR, S3, CloudFront, CodeBuild + CodeConnections, SSM Parameter Store, CloudWatch Logs.

**Spec:** `docs/superpowers/specs/2026-06-15-aws-migration-design.md`
**AWS account:** `476646938644` (do NOT hardcode in committed files; pass via env/CDK context).

**Phasing:** Six phases, each independently verifiable. Phase 0 is local (no AWS). Phases 1–5 each end in a working `cdk deploy` + verification. Phase 6 is cutover. Keep GHA workflows running until Phase 6.

---

## Phase 0 — Local prep (no AWS)

### Task 0.1: Make the claims cursor path env-overridable

**Why:** Default cursor is `.cache/last-claims.json` — inside the shared, every-job-writes `cache/` S3 prefix. On AWS that's a clobber race. We relocate it to `.waivers/` (single-writer prefix) via a `CLAIMS_CURSOR_PATH` env var, so no AWS-specific code lands in the binary.

**Files:**
- Modify: `cmd/claims.go`
- Create: `cmd/claims_cursor.go`
- Test: `cmd/claims_cursor_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/claims_cursor_test.go`:

```go
package cmd

import "testing"

func TestResolveCursorPath(t *testing.T) {
	if got := resolveCursorPath(""); got != "" {
		t.Fatalf("empty env should yield empty (run.go applies default), got %q", got)
	}
	if got := resolveCursorPath(".waivers/last-claims.json"); got != ".waivers/last-claims.json" {
		t.Fatalf("env override not honored, got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run TestResolveCursorPath -v`
Expected: FAIL — `undefined: resolveCursorPath`

- [ ] **Step 3: Write minimal implementation**

Create `cmd/claims_cursor.go`:

```go
package cmd

// resolveCursorPath returns the cursor path from the env override, or "" to let
// claims.Run apply its default (.cache/last-claims.json). On AWS the container
// sets CLAIMS_CURSOR_PATH=.waivers/last-claims.json so the cursor rides the
// single-writer claims/ S3 prefix instead of the shared cache/ prefix.
func resolveCursorPath(env string) string {
	return env
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/ -run TestResolveCursorPath -v`
Expected: PASS

- [ ] **Step 5: Wire it into runClaims**

In `cmd/claims.go`, add `"os"` to imports, and set `CursorPath` in the `claims.Options` literal:

```go
	opts := claims.Options{
		CacheDir:         ".cache",
		CursorPath:       resolveCursorPath(os.Getenv("CLAIMS_CURSOR_PATH")),
		DryRun:           cfg.DryRun,
		NoSignals:        claimsNoSignals,
		Since:            since,
		DropsMin:         claimsDropsMin,
		PushoverUserKey:  cfg.PushoverUserKey,
		PushoverAPIToken: cfg.PushoverAPIToken,
	}
```

- [ ] **Step 6: Verify build + full claims tests + vet**

Run: `go build ./... && go test ./cmd/ ./internal/claims/ && go vet ./...`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/claims.go cmd/claims_cursor.go cmd/claims_cursor_test.go
git commit -m "feat(claims): allow cursor path override via CLAIMS_CURSOR_PATH"
```

---

### Task 0.2: Container image (Go binary + chromium + AWS CLI)

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`

- [ ] **Step 1: Write `.dockerignore`**

```
.cache
.waivers
.fantrax-cache
.backtest
dist
rosterbot
infra/cdk.out
.git
```

- [ ] **Step 2: Write the `Dockerfile`**

```dockerfile
# --- build stage ---
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/rosterbot .

# --- runtime stage ---
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      chromium ca-certificates awscli tini \
 && rm -rf /var/lib/apt/lists/*
ENV CHROME_BIN=/usr/bin/chromium
WORKDIR /app
COPY --from=build /out/rosterbot /app/rosterbot
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
```

- [ ] **Step 3: Build locally to verify the image compiles**

Run: `docker build -t rosterbot:local .`
Expected: build succeeds; final line `naming to docker.io/library/rosterbot:local`.

- [ ] **Step 4: Verify the binary runs in the image**

Run: `docker run --rm --entrypoint /app/rosterbot rosterbot:local --help`
Expected: cobra help text listing subcommands (optimize, waivers, claims, …).

- [ ] **Step 5: Commit**

```bash
git add Dockerfile .dockerignore
git commit -m "build: container image with chromium + aws cli for fargate"
```

---

### Task 0.3: Entrypoint state-sync wrapper

**Why:** Fargate disk is ephemeral. Sync state from S3 before the run and back after. Failures are non-fatal (matches the existing "all cache I/O errors are non-fatal" philosophy).

**Files:**
- Create: `entrypoint.sh`

- [ ] **Step 1: Write `entrypoint.sh`**

```sh
#!/bin/sh
# Entrypoint for Fargate runs: warm state from S3, run the bot, save state back.
# STATE_BUCKET is injected by the task definition. The command (e.g. "optimize
# --matchup") is passed as container args by the EventBridge target override.
set -u

sync_down() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync "s3://$STATE_BUCKET/cache/"   ./.cache/         --quiet || true
  aws s3 sync "s3://$STATE_BUCKET/session/" ./.fantrax-cache/ --quiet || true
  aws s3 sync "s3://$STATE_BUCKET/claims/"  ./.waivers/       --quiet || true
}

sync_up() {
  [ -n "${STATE_BUCKET:-}" ] || return 0
  aws s3 sync ./.cache/         "s3://$STATE_BUCKET/cache/"   --quiet || true
  aws s3 sync ./.fantrax-cache/ "s3://$STATE_BUCKET/session/" --quiet || true
  aws s3 sync ./.waivers/       "s3://$STATE_BUCKET/claims/"  --quiet || true
}

sync_down
./rosterbot "$@"
rc=$?
sync_up
exit $rc
```

- [ ] **Step 2: Lint the script**

Run: `sh -n entrypoint.sh`
Expected: no output (syntax OK).

- [ ] **Step 3: Rebuild image to bake in the entrypoint**

Run: `docker build -t rosterbot:local .`
Expected: build succeeds.

- [ ] **Step 4: Verify entrypoint no-ops cleanly with no STATE_BUCKET**

Run: `docker run --rm rosterbot:local --help`
Expected: cobra help text (sync steps skip because `STATE_BUCKET` is unset, binary still runs).

- [ ] **Step 5: Commit**

```bash
git add entrypoint.sh
git commit -m "build: s3 state-sync entrypoint wrapper"
```

---

## Phase 1 — CDK foundation: ECR + S3 + SSM + IAM

> CDK lives in `infra/` as its **own Go module** (separate `go.mod`) so the repo's main module isn't polluted with CDK deps. From here on, run CDK commands from `infra/`.

### Task 1.1: Scaffold the CDK app

**Files:**
- Create: `infra/` (via `cdk init`)

- [ ] **Step 1: Init the CDK Go app**

```bash
mkdir -p infra && cd infra && cdk init app --language go
```
Expected: generates `infra.go`, `infra_test.go`, `go.mod`, `cdk.json`.

- [ ] **Step 2: Verify it synthesizes**

Run (in `infra/`): `cdk synth`
Expected: prints a CloudFormation template, no errors.

- [ ] **Step 3: Commit**

```bash
git add infra
git commit -m "infra: scaffold CDK Go app"
```

### Task 1.2: Define ECR repo, S3 buckets, SSM params, log group

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Replace the stack body**

In `infra/infra.go`, inside the stack constructor, add (imports: `awsecr`, `awss3`, `awsssm`, `awslogs`, `awscdk` `RemovalPolicy`):

```go
// ECR repo for the image, keep only the last 10 images.
repo := awsecr.NewRepository(stack, jsii.String("Repo"), &awsecr.RepositoryProps{
	RepositoryName: jsii.String("rosterbot"),
	LifecycleRules: &[]*awsecr.LifecycleRule{{MaxImageCount: jsii.Number(10)}},
})

// Durable state bucket (cache/, session/, claims/ prefixes).
stateBucket := awss3.NewBucket(stack, jsii.String("StateBucket"), &awss3.BucketProps{
	Versioned:     jsii.Bool(true),
	RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
})

// Static recap site bucket (private; served via CloudFront in Phase 5).
siteBucket := awss3.NewBucket(stack, jsii.String("SiteBucket"), &awss3.BucketProps{
	RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	AutoDeleteObjects: jsii.Bool(true),
})

// Shared log group for all task runs.
logGroup := awslogs.NewLogGroup(stack, jsii.String("Logs"), &awslogs.LogGroupProps{
	Retention:     awslogs.RetentionDays_ONE_MONTH,
	RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
})

awscdk.NewCfnOutput(stack, jsii.String("RepoUri"), &awscdk.CfnOutputProps{Value: repo.RepositoryUri()})
awscdk.NewCfnOutput(stack, jsii.String("StateBucketName"), &awscdk.CfnOutputProps{Value: stateBucket.BucketName()})
awscdk.NewCfnOutput(stack, jsii.String("SiteBucketName"), &awscdk.CfnOutputProps{Value: siteBucket.BucketName()})

// Expose for later phases by returning from a helper or storing on a struct;
// for a single-file stack, keep these vars in scope for Tasks 1.3 / 3.x / 5.x.
_ = logGroup
```

> Note: as later tasks add the task definition and schedules in the same stack function, keep `repo`, `stateBucket`, `siteBucket`, `logGroup` in scope (do not split files yet).

- [ ] **Step 2: Synth**

Run (in `infra/`): `cdk synth`
Expected: template includes `AWS::ECR::Repository` and two `AWS::S3::Bucket` resources.

- [ ] **Step 3: Commit**

```bash
git add infra/infra.go
git commit -m "infra: ECR repo, state + site S3 buckets, log group"
```

### Task 1.3: Create SSM SecureString params (manual, one-time)

**Why:** Secrets must exist in SSM before the task definition references them. CDK should not contain secret values, so create them with the CLI once.

- [ ] **Step 1: Put each secret (run once, locally, authed to account 476646938644)**

```bash
for kv in \
  FANTRAX_USERNAME FANTRAX_PASSWORD FANTRAX_LEAGUE_ID FANTRAX_TEAM_ID \
  FANTRAX_IL_SLOTS FANTRAX_MINORS_SLOTS GS_MAX GS_MIN \
  PUSHOVER_USER_KEY PUSHOVER_GROUP_KEY PUSHOVER_API_TOKEN ; do
  read -r -p "$kv = " val
  aws ssm put-parameter --name "/rosterbot/$kv" --type SecureString --value "$val" --overwrite
done
```
Expected: each call prints a `Version` number.

- [ ] **Step 2: Verify**

Run: `aws ssm get-parameters-by-path --path /rosterbot --query 'Parameters[].Name'`
Expected: lists all parameter names (values omitted).

- [ ] **Step 3: Deploy foundation**

Run (in `infra/`): `cdk deploy`
Expected: `CREATE_COMPLETE`; outputs print `RepoUri`, `StateBucketName`, `SiteBucketName`. Record `RepoUri` for Phase 2.

---

## Phase 2 — Build pipeline (CodeBuild → ECR)

### Task 2.1: buildspec

**Files:**
- Create: `buildspec.yml`

- [ ] **Step 1: Write `buildspec.yml`**

```yaml
version: 0.2
env:
  variables:
    AWS_DEFAULT_REGION: us-east-1
phases:
  pre_build:
    commands:
      - aws ecr get-login-password | docker login --username AWS --password-stdin "$ECR_URI"
      - REPO="${ECR_URI%/*}"
      - TAG="$(echo "$CODEBUILD_RESOLVED_SOURCE_VERSION" | cut -c1-12)"
  build:
    commands:
      - docker build -t "$ECR_URI:latest" -t "$ECR_URI:$TAG" .
  post_build:
    commands:
      - docker push "$ECR_URI:latest"
      - docker push "$ECR_URI:$TAG"
```

- [ ] **Step 2: Commit**

```bash
git add buildspec.yml
git commit -m "build: codebuild buildspec to push image to ECR"
```

### Task 2.2: CodeBuild project + GitHub source in CDK

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Create a CodeConnections (GitHub) connection (one-time, console)**

In AWS console → Developer Tools → Settings → Connections → Create connection → GitHub → authorize. Copy the connection ARN.

- [ ] **Step 2: Add the CodeBuild project to the stack**

In `infra/infra.go` (imports `awscodebuild`), pass the connection ARN via CDK context (`cdk deploy -c githubConn=<arn> -c githubOwnerRepo=nixon-commits/rosterbot`):

```go
conn := stack.Node().TryGetContext(jsii.String("githubConn")).(string)
ownerRepo := stack.Node().TryGetContext(jsii.String("githubOwnerRepo")).(string)

project := awscodebuild.NewProject(stack, jsii.String("Build"), &awscodebuild.ProjectProps{
	Source: awscodebuild.Source_GitHub(&awscodebuild.GitHubSourceProps{
		Owner: jsii.String(strings.SplitN(ownerRepo, "/", 2)[0]),
		Repo:  jsii.String(strings.SplitN(ownerRepo, "/", 2)[1]),
		Webhook: jsii.Bool(true),
		WebhookFilters: &[]awscodebuild.FilterGroup{
			awscodebuild.FilterGroup_InEventOf(awscodebuild.EventAction_PUSH).
				AndBranchIs(jsii.String("main")),
		},
	}),
	Environment: &awscodebuild.BuildEnvironment{
		BuildImage: awscodebuild.LinuxBuildImage_STANDARD_7_0(),
		Privileged: jsii.Bool(true), // needed for docker build
	},
	EnvironmentVariables: &map[string]*awscodebuild.BuildEnvironmentVariable{
		"ECR_URI": {Value: repo.RepositoryUri()},
	},
})
repo.GrantPullPush(project)
_ = conn // used if you switch to CodeStarConnectionsSourceCredentials
```

> Note: GitHub webhook source in CodeBuild needs a one-time `aws codebuild import-source-credentials` (token) OR a CodeConnections source. If the webhook approach errors on first deploy, fall back to `Source_GitHub` with a personal access token imported via `import-source-credentials`. Resolve the exact auth path here at execution time — both are documented and either works.

- [ ] **Step 3: Deploy**

Run (in `infra/`): `cdk deploy -c githubConn=<arn> -c githubOwnerRepo=nixon-commits/rosterbot`
Expected: `UPDATE_COMPLETE`; CodeBuild project `rosterbot-Build` exists.

- [ ] **Step 4: Trigger a build and verify the image lands in ECR**

```bash
git commit --allow-empty -m "ci: trigger first codebuild" && git push
aws ecr list-images --repository-name rosterbot
```
Expected: `imageTags` includes `latest` and a 12-char sha tag.

---

## Phase 3 — Compute: cluster, task definition, first schedule

### Task 3.1: VPC, cluster, Fargate task definition

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Add VPC lookup + cluster + task def**

Imports: `awsec2`, `awsecs`, `awsiam`. Add after the buckets:

```go
vpc := awsec2.Vpc_FromLookup(stack, jsii.String("DefaultVpc"), &awsec2.VpcLookupOptions{IsDefault: jsii.Bool(true)})
cluster := awsecs.NewCluster(stack, jsii.String("Cluster"), &awsecs.ClusterProps{Vpc: vpc})

taskDef := awsecs.NewFargateTaskDefinition(stack, jsii.String("Task"), &awsecs.FargateTaskDefinitionProps{
	Cpu:            jsii.Number(1024), // 1 vCPU
	MemoryLimitMiB: jsii.Number(2048), // 2 GB
})

// Task role can read/write its S3 prefixes and read SSM secrets.
stateBucket.GrantReadWrite(taskDef.TaskRole(), nil)
siteBucket.GrantReadWrite(taskDef.TaskRole(), nil)
taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
	Actions:   jsii.Strings("ssm:GetParameters", "ssm:GetParameter"),
	Resources: jsii.Strings("arn:aws:ssm:*:*:parameter/rosterbot/*"),
}))

secret := func(name string) awsecs.Secret {
	p := awsssm.StringParameter_FromSecureStringParameterAttributes(stack, jsii.String("P"+name),
		&awsssm.SecureStringParameterAttributes{ParameterName: jsii.String("/rosterbot/" + name)})
	return awsecs.Secret_FromSsmParameter(p)
}

container := taskDef.AddContainer(jsii.String("bot"), &awsecs.ContainerDefinitionOptions{
	Image: awsecs.ContainerImage_FromEcrRepository(repo, jsii.String("latest")),
	Logging: awsecs.LogDriver_AwsLogs(&awsecs.AwsLogDriverProps{
		LogGroup: logGroup, StreamPrefix: jsii.String("run"),
	}),
	Environment: &map[string]*string{
		"STATE_BUCKET":        stateBucket.BucketName(),
		"CLAIMS_CURSOR_PATH":  jsii.String(".waivers/last-claims.json"),
	},
	Secrets: &map[string]awsecs.Secret{
		"FANTRAX_USERNAME":     secret("FANTRAX_USERNAME"),
		"FANTRAX_PASSWORD":     secret("FANTRAX_PASSWORD"),
		"FANTRAX_LEAGUE_ID":    secret("FANTRAX_LEAGUE_ID"),
		"FANTRAX_TEAM_ID":      secret("FANTRAX_TEAM_ID"),
		"FANTRAX_IL_SLOTS":     secret("FANTRAX_IL_SLOTS"),
		"FANTRAX_MINORS_SLOTS": secret("FANTRAX_MINORS_SLOTS"),
		"GS_MAX":               secret("GS_MAX"),
		"GS_MIN":               secret("GS_MIN"),
		"PUSHOVER_USER_KEY":    secret("PUSHOVER_USER_KEY"),
		"PUSHOVER_GROUP_KEY":   secret("PUSHOVER_GROUP_KEY"),
		"PUSHOVER_API_TOKEN":   secret("PUSHOVER_API_TOKEN"),
	},
})
_ = container
```

> `Vpc_FromLookup` requires the account/region be set on the stack env (not env-agnostic). Set `Env` in the `App`/stack props to account `476646938644`, region `us-east-1`.

- [ ] **Step 2: Set stack env**

In the stack props (where the stack is instantiated in `infra.go`'s `main`), set:

```go
Env: &awscdk.Environment{Account: jsii.String("476646938644"), Region: jsii.String("us-east-1")},
```

- [ ] **Step 3: Synth + deploy**

Run (in `infra/`): `cdk deploy -c githubConn=<arn> -c githubOwnerRepo=nixon-commits/rosterbot`
Expected: `UPDATE_COMPLETE`; an `AWS::ECS::TaskDefinition` with one container.

### Task 3.2: First schedule (lineup) + manual smoke run

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Add an events.Rule + EcsTask target for `lineup`**

Imports `awsevents`, `awseventstargets`:

```go
lineupRule := awsevents.NewRule(stack, jsii.String("LineupRule"), &awsevents.RuleProps{
	Schedule: awsevents.Schedule_Expression(jsii.String("cron(0 14-23,0-3 * * ? *)")),
})
lineupRule.AddTarget(awseventstargets.NewEcsTask(&awseventstargets.EcsTaskProps{
	Cluster:        cluster,
	TaskDefinition: taskDef,
	AssignPublicIp: jsii.Bool(true),
	SubnetSelection: &awsec2.SubnetSelection{SubnetType: awsec2.SubnetType_PUBLIC},
	ContainerOverrides: &[]*awseventstargets.ContainerOverride{{
		ContainerName: jsii.String("bot"),
		Command:       jsii.Strings("optimize", "--matchup", "--archive-projections"),
	}},
}))
```

- [ ] **Step 2: Deploy**

Run (in `infra/`): `cdk deploy -c githubConn=<arn> -c githubOwnerRepo=nixon-commits/rosterbot`
Expected: `UPDATE_COMPLETE`.

- [ ] **Step 3: Smoke-run the task by hand and watch logs**

```bash
aws ecs run-task --cluster <ClusterName> --task-definition <TaskDefArn> \
  --launch-type FARGATE --network-configuration \
  'awsvpcConfiguration={subnets=[<publicSubnet>],assignPublicIp=ENABLED}' \
  --overrides '{"containerOverrides":[{"name":"bot","command":["optimize","--matchup","--dry-run"]}]}'
```
Then tail logs: `aws logs tail /aws/.../Logs --follow`
Expected: optimizer dry-run output in CloudWatch; task exits `0`.

- [ ] **Step 4: Verify state round-tripped to S3**

Run: `aws s3 ls s3://<StateBucketName>/cache/`
Expected: cache objects present (the run synced them up).

---

## Phase 4 — Remaining 7 schedules (DRY, table-driven)

### Task 4.1: Add the other schedules from a table

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Replace the single lineup rule with a table-driven loop**

```go
type job struct {
	id, cron string
	cmd      []string
}
jobs := []job{
	{"Lineup", "cron(0 14-23,0-3 * * ? *)", []string{"optimize", "--matchup", "--archive-projections"}},
	{"Prospects", "cron(0 11 * * ? *)", []string{"prospects"}},
	{"GsCheck", "cron(0 12 * * ? *)", []string{"gs-check"}},
	{"Waivers", "cron(0 13 * * ? *)", []string{"waivers"}},
	{"Transactions", "cron(0 14 * * ? *)", []string{"transactions"}},
	{"Claims", "cron(20 14 * * ? *)", []string{"claims"}}, // +20m vs transactions
	{"Recap", "cron(0 11 ? * MON *)", []string{"recap-site", "--out", "dist"}},
	{"Backtest", "cron(0 12 ? * MON *)", []string{"backtest"}},
}
for _, j := range jobs {
	r := awsevents.NewRule(stack, jsii.String(j.id+"Rule"), &awsevents.RuleProps{
		Schedule: awsevents.Schedule_Expression(jsii.String(j.cron)),
	})
	r.AddTarget(awseventstargets.NewEcsTask(&awseventstargets.EcsTaskProps{
		Cluster: cluster, TaskDefinition: taskDef, AssignPublicIp: jsii.Bool(true),
		SubnetSelection:    &awsec2.SubnetSelection{SubnetType: awsec2.SubnetType_PUBLIC},
		ContainerOverrides: &[]*awseventstargets.ContainerOverride{{
			ContainerName: jsii.String("bot"),
			Command:       jsii.Strings(j.cmd...),
		}},
	}))
}
```

> `backtest --recency-experiment` is read-only and optional; run it as a second manual/dispatch task rather than a schedule, or add a `BacktestExp` row. Decide at execution time.

- [ ] **Step 2: Deploy + verify all 8 rules**

Run (in `infra/`): `cdk deploy -c githubConn=<arn> -c githubOwnerRepo=nixon-commits/rosterbot`
Then: `aws events list-rules --query 'Rules[].Name'`
Expected: 8 rules (Lineup, Prospects, GsCheck, Waivers, Transactions, Claims, Recap, Backtest).

- [ ] **Step 3: Smoke-run claims and confirm cursor lands in claims/ prefix**

```bash
aws ecs run-task ... --overrides '{"containerOverrides":[{"name":"bot","command":["claims","--dry-run"]}]}'
aws s3 ls s3://<StateBucketName>/claims/
```
Expected: `last-claims.json` appears under `claims/` (NOT under `cache/`), proving the `CLAIMS_CURSOR_PATH` relocation works.

- [ ] **Step 4: Commit infra**

```bash
git add infra buildspec.yml
git commit -m "infra: ECS task def, secrets, 8 EventBridge schedules"
```

---

## Phase 5 — Recap site (S3 + CloudFront)

### Task 5.1: CloudFront distribution over the site bucket

**Files:**
- Modify: `infra/infra.go`

- [ ] **Step 1: Add CloudFront with an S3 origin (OAC)**

Imports `awscloudfront`, `awscloudfrontorigins`:

```go
dist := awscloudfront.NewDistribution(stack, jsii.String("SiteCdn"), &awscloudfront.DistributionProps{
	DefaultRootObject: jsii.String("index.html"),
	DefaultBehavior: &awscloudfront.BehaviorOptions{
		Origin: awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(siteBucket, nil),
		ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
	},
})
awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
	Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dist.DistributionDomainName()}),
})
```

- [ ] **Step 2: Make the recap entrypoint publish dist/ to the site bucket**

The recap command writes `dist/` locally. Extend `entrypoint.sh` `sync_up` to also push the site when present:

```sh
  [ -d ./dist ] && [ -n "${SITE_BUCKET:-}" ] && aws s3 sync ./dist/ "s3://$SITE_BUCKET/" --delete --quiet || true
```

And add `"SITE_BUCKET": siteBucket.BucketName()` to the container `Environment` map in Task 3.1.

- [ ] **Step 3: Deploy + run recap-site by hand**

Run (in `infra/`): `cdk deploy ...`; then `aws ecs run-task ... command=["recap-site","--out","dist"]`.
Then open the `SiteUrl` output in a browser.
Expected: the recap site renders over HTTPS via CloudFront.

- [ ] **Step 4: Commit**

```bash
git add infra/infra.go entrypoint.sh
git commit -m "infra: cloudfront-fronted recap site on s3"
```

---

## Phase 6 — Cutover & decommission GHA

### Task 6.1: Parallel-run, verify, then retire GHA

- [ ] **Step 1: Let AWS and GHA run in parallel for 2–3 days**

Confirm each AWS job produces the same Pushover notifications / outputs as its GHA twin. Watch CloudWatch logs for errors (especially the chromedp login on the first daily run).

- [ ] **Step 2: Disable the GHA workflows**

```bash
git rm .github/workflows/lineup.yml .github/workflows/prospects.yml \
       .github/workflows/gs-check.yml .github/workflows/transactions.yml \
       .github/workflows/waivers.yml .github/workflows/claims.yml \
       .github/workflows/recap.yml .github/workflows/backtest.yml
```

- [ ] **Step 3: Update docs**

Update `CLAUDE.md` (the GHA section) and `README.md` to describe the AWS deployment instead of the workflows. Note the new `make`/deploy flow (`cdk deploy`) and that secrets live in SSM.

- [ ] **Step 4: Commit + push**

```bash
git add -A
git commit -m "chore: retire GitHub Actions workflows, migrate to AWS"
git push
```

- [ ] **Step 5: Turn off GitHub Pages**

In repo Settings → Pages, set Source to None (recap now served by CloudFront).

---

## Notes for the executor

- **CDK Go API drift:** construct prop names occasionally shift between CDK versions. If a prop errors at `cdk synth`, check the installed `aws-cdk-lib` Go docs for the exact field — the shapes above match CDK v2 as of 2026-06 but verify against `cdk synth` output, which is the source of truth.
- **`Vpc_FromLookup` needs real creds + a concrete account/region** on the stack env (already set in Task 3.1 Step 2). It writes to `cdk.context.json` — commit that file.
- **First daily run does the 15–20s chromedp browser login**; subsequent same-day runs restore the cookie from `s3://.../session/`. Confirm the session prefix populates after run #1.
- **Cost guard while learning:** `cdk destroy` tears the whole stack down (state bucket is RETAIN, so data survives). Spin down between experiments to avoid even the few-dollar charge.
```

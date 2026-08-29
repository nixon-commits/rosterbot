package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsathena"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscertificatemanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfrontorigins"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscodebuild"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsec2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecr"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsevents"
	"github.com/aws/aws-cdk-go/awscdk/v2/awseventstargets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsglue"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awskms"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53targets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsssm"
	"github.com/aws/aws-cdk-go/awscdklambdagoalpha/v2"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

type InfraStackProps struct {
	awscdk.StackProps

	// Certificate is the us-east-1 ACM cert covering every rosterbot.dev name
	// this stack serves (see infra/domain.go). It is REQUIRED, not optional:
	// treating a missing cert as "skip the alias domains" would let the custom
	// hostnames silently stop being served while the deploy stayed green — the
	// stack would come up healthy on its cloudfront.net names and nothing would
	// say the domain had dropped off. Failing loudly at synth is the only
	// version of this that can be noticed.
	Certificate awscertificatemanager.ICertificate
}

func NewInfraStack(scope constructs.Construct, id string, props *InfraStackProps) awscdk.Stack {
	var sprops awscdk.StackProps
	if props != nil {
		sprops = props.StackProps
	}
	if props == nil || props.Certificate == nil {
		panic("InfraStackProps.Certificate is required: the alias domains on both " +
			"distributions cannot be attached without it (see infra/domain.go)")
	}
	stack := awscdk.NewStack(scope, &id, &sprops)

	// The zone that answers for the alias records below. Imported, never
	// created — see importZone.
	zone := importZone(stack, "Zone")

	// --- Phase 1: foundation (ECR, S3 state + site buckets, log group) ---

	// ECR repo for the container image; keep only the last 10 images.
	repo := awsecr.NewRepository(stack, jsii.String("Repo"), &awsecr.RepositoryProps{
		RepositoryName: jsii.String("rosterbot"),
		LifecycleRules: &[]*awsecr.LifecycleRule{{MaxImageCount: jsii.Number(10)}},
	})

	// Durable state bucket (cache/, session/, claims/ prefixes synced by the entrypoint).
	stateBucket := awss3.NewBucket(stack, jsii.String("StateBucket"), &awss3.BucketProps{
		Versioned:     jsii.Bool(true),
		RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
		LifecycleRules: &[]*awss3.LifecycleRule{{
			// cache/ is TTL-driven and safe to trim: every overwrite of a
			// FileCache key (the S3 Store adapter, see cache-store-seam) left
			// the prior version live forever with versioning on but no
			// expiration rule, so storage cost grew unbounded relative to
			// actual live cache size. analysis/, backtest/, runs/ (and
			// runledger/), lineup/, session/, and claims/ are intentionally
			// durable/bounded (append-only archives or small ledgers) and
			// stay untouched — only cache/ gets this rule.
			Id:                          jsii.String("ExpireNoncurrentCacheVersions"),
			Prefix:                      jsii.String("cache/"),
			NoncurrentVersionExpiration: awscdk.Duration_Days(jsii.Number(14)),
		}, {
			// opsalert/ holds one small object per alert the notifier has
			// already sent, purely so a repeat delivery of the same event
			// stays quiet (rosterbot-chs). One key per stopped task means it
			// grows with the run rate — ~26/day — and a marker is worthless
			// once no further delivery of its event can arrive. 30 days is far
			// past EventBridge's retry horizon, so expiring here can never
			// resurrect an old alert.
			//
			// The heartbeat's markers (rosterbot-ys8) share the prefix and are
			// one per *job*, rewritten in place, so they are unaffected by the
			// count — but they are also refreshed on every alert, and an
			// expiry during an outage longer than 30 days costs exactly one
			// repeat push.
			Id:         jsii.String("ExpireOpsAlertMarkers"),
			Prefix:     jsii.String("opsalert/"),
			Expiration: awscdk.Duration_Days(jsii.Number(30)),
		}, {
			// Bucket-wide noncurrent-version expiry. The cache/ rule above was
			// written on the reasoning that "analysis/, backtest/, runs/,
			// lineup/, session/ and claims/ are intentionally durable/bounded
			// (append-only archives or small ledgers) and stay untouched" —
			// correct about their LIVE objects, and wrong about their versions,
			// because nothing on the write path checked whether the bytes had
			// changed. entrypoint.sh runs sync_up after every task and
			// statesync.Up re-PUT every file unconditionally, so append-only
			// trees were rewritten ~29x/day.
			//
			// Measured 2026-08-18, before the statesync fix: this bucket held
			// 563 GB across 701,781 objects against ~975 MB of live data, of
			// which archive/ alone was 486,877 versions / 635 GB against 679
			// live objects. archive/ is not even named in the list above — it
			// postdates that comment, which is precisely how it escaped.
			//
			// Deliberately unscoped by prefix. The cache/ rule enumerated who
			// needed trimming, and the artifact that went on to cost 635 GB was
			// the one added afterwards; a rule that has to be extended for each
			// new prefix fails silently in exactly that way, and the default it
			// fails to is unbounded growth. 30 days rather than cache/'s 14
			// because versioning's remaining job here is recovery from a bad
			// overwrite of a NoBackfill artifact (the Team Value Store, see
			// docs/adr/0002) — a month is a realistic window to notice one.
			// Noncurrent versions only: live objects are never touched, so this
			// cannot delete an artifact that is merely old.
			Id:                          jsii.String("ExpireNoncurrentVersions"),
			NoncurrentVersionExpiration: awscdk.Duration_Days(jsii.Number(30)),
		}},
	})

	// Static recap site bucket (private; served via CloudFront in Phase 5).
	siteBucket := awss3.NewBucket(stack, jsii.String("SiteBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
	})

	// Access logs for the recap CDN — the recap site is public and unauthenticated,
	// so these logs are the only signal that anyone reads it (rosterbot-b41). They
	// answer when a page was fetched, from what IP, and which week page it was.
	//
	// ObjectOwnership must be OBJECT_WRITER: CloudFront's standard logging writes
	// via an ACL, which buckets have refused by default since S3 disabled ACLs in
	// 2023, so the CDK default (BUCKET_OWNER_ENFORCED) silently yields no logs.
	//
	// 90-day expiry rather than keep-forever: this is a ~12-person league's reading
	// habits, the questions asked of it are all recent-window, and an unbounded log
	// prefix is the same slow storage leak the cache/ rule above exists to stop.
	recapLogBucket := awss3.NewBucket(stack, jsii.String("RecapLogBucket"), &awss3.BucketProps{
		ObjectOwnership:   awss3.ObjectOwnership_OBJECT_WRITER,
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
		LifecycleRules: &[]*awss3.LifecycleRule{{
			Id:         jsii.String("ExpireRecapAccessLogs"),
			Expiration: awscdk.Duration_Days(jsii.Number(90)),
		}},
	})

	// Dashboard bucket (static web UI; private, served via its own CDN below).
	dashboardBucket := awss3.NewBucket(stack, jsii.String("DashboardBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
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
	awscdk.NewCfnOutput(stack, jsii.String("RecapLogBucketName"), &awscdk.CfnOutputProps{Value: recapLogBucket.BucketName()})
	awscdk.NewCfnOutput(stack, jsii.String("DashboardBucketName"), &awscdk.CfnOutputProps{Value: dashboardBucket.BucketName()})

	// --- Phase 3: compute (VPC, cluster, ARM64 Fargate task definition) ---

	vpc := awsec2.Vpc_FromLookup(stack, jsii.String("DefaultVpc"), &awsec2.VpcLookupOptions{
		IsDefault: jsii.Bool(true),
	})
	cluster := awsecs.NewCluster(stack, jsii.String("Cluster"), &awsecs.ClusterProps{Vpc: vpc})

	// --- Multi-tenant identity (rosterbot-crq.6) ---
	//
	// The one store in this tree that is small, hot, mutable and contended:
	// users, their passkey credentials, their Fantrax connection, and the
	// single-use enrollment tokens. ADR-0001 rules S3 for key->blob and asks
	// that a database not be re-proposed "without a new access pattern that
	// needs queries". This is that access pattern's opposite twin, and the
	// reason is not queries: what S3 cannot give is an atomic compare-and-set
	// on a mutable record and a uniqueness constraint on create, both of which
	// this data needs on every write. rosterbot-crq.2 is the bug that proves
	// it — a concurrent login and registration silently losing a passkey.
	//
	// Single table, PK/SK strings; the key layout lives in the design doc.
	identityTable := awsdynamodb.NewTableV2(stack, jsii.String("IdentityTable"), &awsdynamodb.TablePropsV2{
		PartitionKey: &awsdynamodb.Attribute{Name: jsii.String("pk"), Type: awsdynamodb.AttributeType_STRING},
		SortKey:      &awsdynamodb.Attribute{Name: jsii.String("sk"), Type: awsdynamodb.AttributeType_STRING},
		Billing:      awsdynamodb.Billing_OnDemand(nil),
		// ENROLL# items carry an absolute expiry and DynamoDB reaps them, so an
		// unredeemed invite cannot linger as a live credential.
		TimeToLiveAttribute: jsii.String("expires_at"),
		// The only store here whose contents cannot be regenerated from an
		// upstream. Losing the cache costs a refetch; losing this locks every
		// user out of the dashboard and orphans their Fantrax connection.
		PointInTimeRecoverySpecification: &awsdynamodb.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: jsii.Bool(true),
		},
		RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
	})

	// The CMK that wraps each tenant's Fantrax credentials. Its purpose is the
	// ASYMMETRIC grant made further down (where apiFn is created), not the
	// encryption itself — DynamoDB is already encrypted at rest, so a key both
	// roles could use would add close to nothing.
	fantraxCredKey := awskms.NewKey(stack, jsii.String("FantraxCredKey"), &awskms.KeyProps{
		Description:       jsii.String("rosterbot: envelope key for per-tenant Fantrax credentials"),
		EnableKeyRotation: jsii.Bool(true),
		RemovalPolicy:     awscdk.RemovalPolicy_RETAIN,
	})

	taskDef := awsecs.NewFargateTaskDefinition(stack, jsii.String("Task"), &awsecs.FargateTaskDefinitionProps{
		Cpu:            jsii.Number(1024), // 1 vCPU
		MemoryLimitMiB: jsii.Number(2048), // 2 GB
		RuntimePlatform: &awsecs.RuntimePlatform{
			CpuArchitecture:       awsecs.CpuArchitecture_ARM64(), // Graviton; matches local arm64 build, ~20% cheaper
			OperatingSystemFamily: awsecs.OperatingSystemFamily_LINUX(),
		},
	})

	// --- CloudFront in front of the recap + report buckets (HTTPS + CDN) ---
	// Created before the container so their distribution IDs can be injected as
	// env vars; entrypoint.sh invalidates them after each S3 sync so a freshly
	// rendered page is served immediately instead of after the ~24h cache TTL.
	dist := awscloudfront.NewDistribution(stack, jsii.String("SiteCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		// Adding an alias does NOT retire the default cloudfront.net name —
		// CloudFront keeps serving both — so every published or bookmarked
		// recap URL keeps working through this change and after it.
		DomainNames: jsii.Strings(recapHost),
		Certificate: props.Certificate,
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(siteBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
		// Readership signal for the recap site. Cookies are deliberately excluded:
		// the site sets none, and logging them would only widen what these files
		// hold. Only SiteCdn is logged — DashboardCdn is passkey-gated and out of
		// scope for rosterbot-b41.
		EnableLogging:      jsii.Bool(true),
		LogBucket:          recapLogBucket,
		LogFilePrefix:      jsii.String("recap/"),
		LogIncludesCookies: jsii.Bool(false),
	})
	cfArn := func(d awscloudfront.Distribution) *string {
		return awscdk.Fn_Join(jsii.String(""), &[]*string{
			jsii.String("arn:aws:cloudfront::"), stack.Account(), jsii.String(":distribution/"), d.DistributionId(),
		})
	}

	// Task role: read/write its S3 prefixes, read the rosterbot SSM secrets, and
	// invalidate the two CloudFront distributions after publishing a site.
	stateBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	siteBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	// Read-only, and scoped to recap/: projection-site reads these logs to build
	// views.json, and CloudFront is the only writer. Nothing in the bot should be
	// able to alter or delete the readership record it reports on.
	recapLogBucket.GrantRead(taskDef.TaskRole(), jsii.String("recap/*"))
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("ssm:GetParameters", "ssm:GetParameter"),
		Resources: jsii.Strings("arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/*"),
	}))
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("cloudfront:CreateInvalidation"),
		Resources: &[]*string{cfArn(dist)},
	}))

	secret := func(name string) awsecs.Secret {
		p := awsssm.StringParameter_FromSecureStringParameterAttributes(stack, jsii.String("P"+name),
			&awsssm.SecureStringParameterAttributes{ParameterName: jsii.String("/rosterbot/" + name)})
		return awsecs.Secret_FromSsmParameter(p)
	}

	botContainer := taskDef.AddContainer(jsii.String("bot"), &awsecs.ContainerDefinitionOptions{
		Image: awsecs.ContainerImage_FromEcrRepository(repo, jsii.String("latest")),
		Logging: awsecs.LogDriver_AwsLogs(&awsecs.AwsLogDriverProps{
			LogGroup:     logGroup,
			StreamPrefix: jsii.String("run"),
		}),
		Environment: &map[string]*string{
			"STATE_BUCKET":        stateBucket.BucketName(),
			"SITE_BUCKET":         siteBucket.BucketName(),
			"RECAP_LOG_BUCKET":    recapLogBucket.BucketName(),
			"DASHBOARD_BUCKET":    dashboardBucket.BucketName(),
			"SITE_CF_DIST_ID":     dist.DistributionId(),
			"CLAIMS_CURSOR_PATH":  jsii.String(".waivers/last-claims.json"),
			"GS_TRACKING_ENABLED": jsii.String("true"),
			// Named rather than retyped in each module, so a rename cannot
			// leave one composition root pointing at a table that is gone.
			"IDENTITY_TABLE":   identityTable.TableName(),
			"FANTRAX_CRED_KEY": fantraxCredKey.KeyArn(),
			// AN ENVIRONMENT VARIABLE, NOT A SECRET, AND THAT IS LOAD-BEARING
			// FOR THE FAN-OUT (rosterbot-crq.13).
			//
			// It was a Secrets entry, carrying a comment asserting that
			// containerOverrides "takes precedence over this default". AWS
			// documents containerOverrides.environment as overriding the task
			// definition's `environment`; it says nothing about overriding a
			// `secrets` entry of the same name, and the two are resolved by
			// different machinery — the agent injects secrets from SSM at
			// container start. So the assertion was untested, and its failure
			// mode is the worst one available here: every fanned-out task would
			// silently keep the operator's id, write to the operator's
			// PerTenant session/ prefix, and act as them, with every job
			// reporting success (cmd/sync_tenant_test.go names exactly this).
			//
			// Designed away rather than tested. As `environment` the override
			// is documented behaviour, and the value was never a secret: the
			// same reasoning is already written at the apiFn.AddEnvironment
			// call below, which passes this identical parameter as a plain
			// environment variable — the tenant id is an opaque handle that
			// already appears in every per-tenant S3 key.
			//
			// Still on the task definition rather than per-schedule, so every
			// launch path inherits the operator default: the EventBridge
			// schedules, the jobs the API launches, and the projection-site
			// render CodeBuild fires after a deploy.
			"ROSTERBOT_USER_ID": awsssm.StringParameter_ValueForStringParameter(
				stack, jsii.String("/rosterbot/OPERATOR_USER_ID"), nil),
			// The operator's id under its OWN name, so a fanned-out task can
			// tell whether it is the operator's run after the dispatcher
			// overrides ROSTERBOT_USER_ID. projection-site's views report
			// (recap readership — deployment-wide data, not per-tenant) gates
			// on this to publish only into the operator's partition.
			"OPERATOR_USER_ID": awsssm.StringParameter_ValueForStringParameter(
				stack, jsii.String("/rosterbot/OPERATOR_USER_ID"), nil),
			// The Apple developer team the APNs provider token signs as (`iss`).
			// Not a secret and stable, so it lives here rather than in Secrets —
			// a name must appear in exactly one of the two maps, and cdk synth
			// does not catch the mistake.
			"APNS_TEAM_ID": jsii.String("8KBU54NP6U"),
			// Cutover window: fantasy events go to BOTH Pushover and APNs.
			// Delete this entry (and redeploy) to complete the migration.
			// Deliberately NOT keyed off PUSHOVER_USER_KEY, which the operator
			// channel reads permanently — see the spec's Cutover section.
			"PUSHOVER_FANTASY_DUAL_SEND": jsii.String("1"),
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
			// The tenant this task runs for (rosterbot-crq.11/.13). Every
			// per-tenant store composes user=<this>/ into its prefix; unset, they
			// fall back to the original un-segmented layout, which is why the
			// cutover is exactly this parameter existing.
			//
			// On the task definition rather than per-schedule so that EVERY
			// launch path inherits it — the EventBridge schedules, the jobs the
			// API launches, and the projection-site render CodeBuild fires after
			// a deploy. A per-schedule override would have left those last two
			// writing to the legacy prefix while the schedules wrote to the new
			// one, splitting the same artifact across two locations.
			//
			// SLEEPER_LEAGUE_ID is the one hard requirement football-values/
			// football-trades have (loadFootballConfig errors without it,
			// unlike DYNASTY_FORMAT and FOOTBALL_PUSHOVER_*, which fall back
			// safely in code) — without this the FootballValues/FootballTrades
			// EventBridge schedules fail every run with "missing required env
			// var: SLEEPER_LEAGUE_ID" once deployed.
			"SLEEPER_LEAGUE_ID": secret("SLEEPER_LEAGUE_ID"),
			// APNs provider-token credentials (the .p8 body and its Key ID).
			// MERGE-ORDER LOAD-BEARING: an ECS Secret naming an SSM parameter
			// that does not exist fails EVERY task launch at provisioning
			// (ResourceInitializationError), taking down all scheduled jobs.
			// Both parameters must exist in us-west-1 before this deploys —
			// they are created by hand from the Apple developer portal key,
			// which is served for download exactly once.
			"APNS_AUTH_KEY": secret("APNS_AUTH_KEY"),
			"APNS_KEY_ID":   secret("APNS_KEY_ID"),
		},
	})

	awscdk.NewCfnOutput(stack, jsii.String("ClusterName"), &awscdk.CfnOutputProps{Value: cluster.ClusterName()})
	awscdk.NewCfnOutput(stack, jsii.String("TaskDefArn"), &awscdk.CfnOutputProps{Value: taskDef.TaskDefinitionArn()})

	// --- Phase 5: CloudFront URLs (distributions are created above, before the
	// container, so their IDs can be injected as env vars for cache invalidation) ---
	awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
		Value: jsii.String("https://" + recapHost),
	})
	awscdk.NewCfnOutput(stack, jsii.String("SiteCdnDefaultUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dist.DistributionDomainName()}),
	})

	// A *and* AAAA. CloudFront serves dual-stack, so an A-only record leaves
	// IPv6-only clients resolving nothing — a failure invisible from any
	// dual-stack machine, which is every machine we would notice it from.
	recapTarget := awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(dist))
	awsroute53.NewARecord(stack, jsii.String("RecapAliasA"), &awsroute53.ARecordProps{
		Zone: zone, RecordName: jsii.String(recapHost), Target: recapTarget,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("RecapAliasAAAA"), &awsroute53.AaaaRecordProps{
		Zone: zone, RecordName: jsii.String(recapHost), Target: recapTarget,
	})

	// --- Lineup + control API: Go Lambda behind a Function URL ---
	// Serves GET /v1/lineup/today from the precomputed JSON the hourly optimize
	// run publishes (lineup/ prefix), GET /v1/runs from the run ledger
	// (runledger/ prefix written by entrypoint.sh) plus captured output blobs
	// (runs/<id>/output.json), and POST /v1/jobs/{name} which launches the
	// existing Fargate task. No Chrome/Fantrax on the request path.
	//
	// A dedicated egress-only SG for tasks the API launches (RunTask requires a
	// concrete SG; tasks only need outbound to pull the image + hit upstreams).
	taskSg := awsec2.NewSecurityGroup(stack, jsii.String("TaskSg"), &awsec2.SecurityGroupProps{
		Vpc:              vpc,
		AllowAllOutbound: jsii.Bool(true),
		Description:      jsii.String("rosterbot tasks launched by the API"),
	})
	publicSubnets := vpc.SelectSubnets(&awsec2.SubnetSelection{SubnetType: awsec2.SubnetType_PUBLIC})

	// Every GoFunction below bundles with -buildvcs=false, and it is a cost
	// control rather than a build preference. `go build` defaults to
	// -buildvcs=auto, which stamps the current git commit into the binary — so
	// every push produced a different binary for all three Lambdas even when
	// their own source had not changed, a different CDK asset hash, and
	// therefore a real CloudFormation update on every single build. Measured
	// 2026-08-17: the build for 36a865d changed exactly one file
	// (web/dashboard/settings.js, a static dashboard asset that cannot affect a
	// Go binary) and still replaced LineupApi, Dispatch and OpsNotify.
	//
	// The cost was ~94s of `cdk deploy` per build — 47% of a 201s build — on the
	// ~64% of builds that touch no Lambda input at all, plus three needless
	// Lambda code replacements per push and ~3 new objects/build in the CDK
	// assets bucket (561 objects / 4.1 GB when this was written).
	//
	// Fixing the HASH rather than gating the deploy on changed paths is
	// deliberate. lambda/go.mod carries `replace ../`, so a change to
	// internal/lineupapi genuinely changes the API Lambda while touching neither
	// infra/ nor lambda/; a path allowlist cannot see through a module replace
	// and would ship stale Lambda code with nothing reporting it. With stable
	// hashes cdk deploy gates itself on the actual bytes and reports "no
	// changes" — the decision moves to the one place that can make it correctly.
	//
	// -trimpath is REQUIRED here, and -buildvcs=false alone was not enough.
	// Shipping only the latter was measured as a failure: git diff between the
	// two deploys touched zero Go source (infra/, docs/, web/dashboard only) and
	// CloudFormation still replaced all three Lambdas. Go embeds the absolute
	// build directory in the binary unless -trimpath is passed, and CodeBuild's
	// source directory carries a fresh random component per build --
	// /codebuild/output/src160962602 then /codebuild/output/src2576922972 on two
	// consecutive builds -- so the asset hash moved on every push no matter what
	// the source did.
	//
	// The original argument for omitting it (that it strips absolute paths from
	// panic traces, and OpsNotify is the failure-alerting path) was close to
	// backwards. The absolute prefix in production is precisely that ephemeral
	// /codebuild/output/srcNNNNNNNNN/src, which is different on every build and
	// identifies nothing; -trimpath replaces it with a module-relative path, so
	// a trace reads github.com/nixon-commits/rosterbot/internal/... and is more
	// useful, not less.
	//
	// Verify any change to this by synthesizing from TWO DIFFERENT DIRECTORIES
	// and comparing asset hashes -- NOT from two commits in one directory, which
	// is what let the incomplete version look verified: it held the build path
	// constant and so could not see the variable that actually dominates in CI.
	deterministicGoBundling := &awscdklambdagoalpha.BundlingOptions{
		GoBuildFlags: jsii.Strings("-buildvcs=false", "-trimpath"),
	}

	apiFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("LineupApi"), &awscdklambdagoalpha.GoFunctionProps{
		Entry:    jsii.String("../lambda"),
		Bundling: deterministicGoBundling,
		// Pin to provided.al2023: provided.al2 (the GoFunction default) loses
		// support 2026-07-31. The Go binary is statically linked, so the AL
		// version under it is immaterial — this is a base-OS swap only.
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		Environment: &map[string]*string{
			"STATE_BUCKET":         stateBucket.BucketName(),
			"API_TOKEN_PARAM":      jsii.String("/rosterbot/ROSTERBOT_API_TOKEN"),
			"SESSION_SECRET_PARAM": jsii.String("/rosterbot/DASHBOARD_SESSION_SECRET"),
			"CLUSTER":              cluster.ClusterArn(),
			"TASK_DEF":             taskDef.TaskDefinitionArn(),
			"SUBNETS":              awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds),
			"SECURITY_GROUPS":      taskSg.SecurityGroupId(),
			"CONTAINER_NAME":       jsii.String("bot"),
		},
	})
	// Least privilege: read lineup/ + the run ledger/output objects + the one
	// token param. runledger/ is the ledger (rosterbot-432); runs/ is still
	// read for per-run captured output blobs (runs/<id>/output.json).
	stateBucket.GrantRead(apiFn, jsii.String("lineup/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runledger/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runs/*"))
	stateBucket.GrantRead(apiFn, jsii.String("notifications/*"))
	stateBucket.GrantRead(apiFn, jsii.String("trades/*"))
	stateBucket.GrantRead(apiFn, jsii.String("tradevalues/*"))
	stateBucket.GrantRead(apiFn, jsii.String("pool/*"))
	// reports/ holds the three private dashboard reports (model, gap, views).
	// They are in THIS bucket rather than the dashboard bucket's report/ prefix
	// precisely because no CloudFront distribution fronts the state bucket:
	// serving them through this Lambda's passkey-gated /v1/reports/{name} is the
	// only path to them (rosterbot-crq.3). value.json and football.json stay on
	// the public prefix — league-wide standings, not one manager's performance.
	stateBucket.GrantRead(apiFn, jsii.String("reports/*"))
	// webauthn/ holds the single Identity record and is read-modify-written
	// on every registration and login (sign-counter update).
	stateBucket.GrantReadWrite(apiFn, jsii.String("webauthn/*"))

	// --- The split that makes credential custody survivable (decision 6) ---
	//
	// Both principals read and write the identity table. Only ONE of them can
	// decrypt a Fantrax password:
	//
	//	apiFn         kms:Encrypt   — it accepts the credential from the browser
	//	taskDef role  kms:Decrypt   — it is the only thing that ever uses one
	//
	// This asymmetry is the whole security argument for storing passwords at
	// all. apiFn sits behind a Function URL with AuthType: NONE — the one
	// component in this stack reachable from the open internet. If it could
	// decrypt, every authentication bug in internal/lineupapi would also be a
	// third-party-password disclosure bug, and a dashboard compromise would
	// cost twelve people their Fantrax accounts. With the split, a full
	// compromise of the public surface yields ciphertext and no key.
	//
	// The cost is a real design constraint, stated here so it is not quietly
	// "fixed" later: the API can never verify a credential synchronously.
	// Verification happens in the connect ECS task, which is why that flow is
	// asynchronous and polls for progress (rosterbot-crq.12). Granting apiFn
	// kms:Decrypt to make connect feel snappier would trade the property away.
	identityTable.GrantReadWriteData(apiFn)
	identityTable.GrantReadWriteData(taskDef.TaskRole())
	fantraxCredKey.GrantDecrypt(taskDef.TaskRole())

	// The connect task needs BOTH halves of the key, which the encrypt/decrypt
	// split above did not anticipate. It DECRYPTS the credential pair the API
	// sealed, drives the login, and then must ENCRYPT the captured FX_RM
	// session cookie back into the record (cmd/connect.go's sealer.Seal).
	//
	// With decrypt only, connect ran the entire flow successfully — login,
	// MyTeamIDs ownership proof, ClaimTeam — and then died on the seal with
	//
	//   AccessDeniedException: ... not authorized to perform: kms:Encrypt
	//
	// i.e. at the last write, after the expensive and irreversible parts had
	// already happened, leaving the team claimed and the connection stuck at
	// pending. Measured in production 2026-08-14 (rosterbot-crq.18).
	//
	// Written by hand rather than fantraxCredKey.GrantEncrypt for the same
	// reason as the Lambda's statement below: that helper also grants
	// kms:ReEncrypt*. This role can already decrypt, so ReEncrypt* would give
	// it nothing it lacks — but keeping the narrow form leaves ONE rule for
	// this key ("nobody gets ReEncrypt*") instead of an exception that has to
	// be re-justified every time someone reads it.
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("kms:Encrypt"),
		Resources: &[]*string{fantraxCredKey.KeyArn()},
	}))

	// NOT fantraxCredKey.GrantEncrypt(apiFn). That helper grants kms:Encrypt,
	// kms:GenerateDataKey* AND kms:ReEncrypt* — and ReEncrypt* covers
	// ReEncryptFrom, which is a decryption primitive wearing a disguise: a
	// principal holding it can re-encrypt our ciphertext under a key THEY
	// control (their own key policy supplies the matching ReEncryptTo) and
	// then decrypt it there at leisure. KMS never returns plaintext during the
	// call, so it reads as harmless; the end state is indistinguishable from
	// having granted kms:Decrypt. That is precisely the property this whole
	// block exists to deny the internet-facing Lambda, so the grant is written
	// out by hand instead.
	//
	// kms:Encrypt alone is sufficient: KMS encrypts up to 4 KB directly and a
	// Fantrax credential pair is far under that, so there is no data key to
	// generate and no reason for GenerateDataKey* either.
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("kms:Encrypt"),
		Resources: &[]*string{fantraxCredKey.KeyArn()},
	}))

	// The API reads the same per-tenant prefixes the tasks write, so it must
	// compose the SAME tenant segment. Giving the tasks a tenant and not the
	// reader would split every per-tenant artifact in two: jobs writing to
	// user=<uid>/ while the dashboard reads the un-segmented prefix and shows
	// nothing, with every job still reporting success.
	// A PLAIN String parameter, not a SecureString. CloudFormation refuses
	// ssm-secure dynamic references in Lambda environment variables
	// ("SSM Secure reference is not supported in:
	// [AWS::Lambda::Function/Properties/Environment/Variables/...]") because it
	// resolves them at deploy time rather than handing them to a runtime role
	// the way ECS secrets work. cdk synth does not catch this — CDK emits the
	// reference and only warns, so the rejection comes from CFN at deploy.
	//
	// The value is not a secret in any case: the tenant id is an opaque handle
	// that already appears in every per-tenant S3 key.
	apiFn.AddEnvironment(jsii.String("ROSTERBOT_USER_ID"),
		awsssm.StringParameter_ValueForStringParameter(stack, jsii.String("/rosterbot/OPERATOR_USER_ID"), nil), nil)
	apiFn.AddEnvironment(jsii.String("IDENTITY_TABLE"), identityTable.TableName(), nil)
	apiFn.AddEnvironment(jsii.String("FANTRAX_CRED_KEY"), fantraxCredKey.KeyArn(), nil)
	// GET /v1/infra reports the health of every prefix in the state bucket, so
	// it needs to LIST the whole bucket — but only list. It reports object
	// counts, sizes, last-modified times and dt= partition names, all of which
	// come from the listing itself; it never reads an object body. Granting
	// s3:ListBucket alone (rather than widening GrantRead to "*") keeps the
	// Lambda unable to read the contents of prefixes it has no business in,
	// notably session/ (the Fantrax auth cookie) and claims/.
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("s3:ListBucket"),
		Resources: jsii.Strings(*stateBucket.BucketArn()),
	}))
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: jsii.Strings("ssm:GetParameter"),
		Resources: jsii.Strings(
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/ROSTERBOT_API_TOKEN",
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/DASHBOARD_SESSION_SECRET",
		),
	}))
	// Launch the existing task definition on demand (POST /v1/jobs/{name}).
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("ecs:RunTask"),
		Resources: jsii.Strings(*taskDef.TaskDefinitionArn()),
	}))
	// RunTask passes the task's roles to ECS — the API role must be allowed to.
	passRoles := []*string{taskDef.TaskRole().RoleArn()}
	if taskDef.ExecutionRole() != nil {
		passRoles = append(passRoles, taskDef.ExecutionRole().RoleArn())
	}
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("iam:PassRole"),
		Resources: &passRoles,
	}))
	// AuthType NONE: the function enforces the Bearer token itself (IAM signing
	// is impractical for a thin iOS client).
	apiURL := apiFn.AddFunctionUrl(&awslambda.FunctionUrlOptions{
		AuthType: awslambda.FunctionUrlAuthType_NONE,
	})
	awscdk.NewCfnOutput(stack, jsii.String("LineupApiUrl"), &awscdk.CfnOutputProps{Value: apiURL.Url()})

	// --- Dashboard: static UI + the same Lambda API, one distribution ---
	// "/v1/*" proxies straight to the Function URL so the browser sees a single
	// same-origin app — no CORS handling needed anywhere. CachePolicy is
	// disabled and OriginRequestPolicy forwards everything (including the
	// Authorization header) except the Host header — the Function URL origin
	// doesn't need CloudFront's own Host and forwarding it risks the origin
	// rejecting the request.

	// dashboardHosts are the names this distribution answers to under its own
	// identity. ONE slice, because it feeds two places that must not disagree:
	// the distribution's DomainNames (the aliases CloudFront serves) and the
	// viewer-request function's allowlist (the aliases it does NOT redirect
	// away). TestDashboardRedirect_AllowlistMatchesTheDistributionAliases pins
	// them together against the synthesized template rather than against this
	// variable, so a future edit that reaches past it still fails.
	dashboardHosts := []string{dashHost, apexHost}

	canonicalJS := make([]string, len(dashboardHosts))
	for i, h := range dashboardHosts {
		canonicalJS[i] = "'" + h + "'"
	}

	// Two jobs, one viewer-request function — and the ORDER of them is the
	// contract. The host redirect runs first, so an /invite link opened on a
	// deprecated hostname is redirected with its path and token intact and the
	// apex's own pass rewrites it; rewriting first would serve index.html from
	// the very hostname we are retiring.
	//
	// (1) DEPRECATE THE DEFAULT *.cloudfront.net NAME. It was the dashboard's
	// original URL and, until rosterbot-jloe.4, the WebAuthn RP ID. It is now
	// neither, and it had become a trap rather than a fallback: the SPA renders
	// perfectly there while every ceremony is refused for RP mismatch, because
	// DASHBOARD_RP_ORIGIN below lists only the apex and dash. A login page that
	// looks healthy and fails at the biometric prompt is worse than one that is
	// gone. CloudFront does not let a distribution disown its default name, so
	// a redirect is the only spelling of "retired" available.
	//
	// AN ALLOWLIST, NOT A MATCH ON THE OLD NAME — and that is forced, not
	// stylistic. This function is an INPUT to dashboardDist, so reading
	// dashboardDist.DistributionDomainName() here would close a
	// Function -> Distribution -> Function cycle, the same one documented at
	// the DASHBOARD_CF_DIST_ID and RP_ID/RP_ORIGIN blocks below. The allowlist
	// needs no reference at all. It is also inert to a distribution
	// REPLACEMENT, which mints a new cloudfront.net name that an exact-match
	// denylist would silently stop catching — the reasoning that keeps the RP
	// ID a literal, one level down.
	//
	// 301 WITH A BOUNDED cache-control. The status says "this hostname is not
	// coming back", which is true and is what makes link-rewriting clients do
	// the right thing; the max-age keeps a browser from pinning it forever, so
	// a mistake here expires in an hour instead of needing every operator to
	// clear their cache. The two are not in tension — one is the semantics of
	// the move, the other the revalidation policy for it.
	//
	// (2) SERVE THE SPA AT /invite (universal-link fallback). /invite is the
	// path the iOS app claims in its AASA applinks block, so it has to render
	// the SPA for anyone who taps the link WITHOUT the app installed. It does
	// not by default: the origin is S3 behind OAC, which does no directory
	// index for subpaths, so /invite is a 403.
	//
	// A viewer-request function rather than the obvious ErrorResponses
	// 403/404 -> /index.html. CloudFront custom error responses apply to the
	// WHOLE DISTRIBUTION, not per behavior, so that version would also rewrite
	// the API's genuine 401/403 on /v1/* into index.html with status 200 — and
	// the iOS client reads those 401s to drive its sign-in gate, so it would
	// sign users out into a page that looks fine. A function association is
	// per behavior, so attaching this to DefaultBehavior alone leaves /v1/*
	// semantics untouched. That scoping now carries the redirect too, and
	// deliberately: a 301 on /v1/* would break every POST that followed it
	// (method and body are not preserved), and leaving the API reachable on
	// the old name keeps the bearer-token break-glass working if the apex
	// alias or certificate ever fails — see the DashboardCdnDefaultUrl output.
	// The rewrite is also exact rather than a catch-all: a genuine 404 stays
	// a 404.
	//
	// The construct id is still "InviteRewrite" although the function now does
	// two things. Renaming it would change the logical id, replacing the
	// function and rewriting dashboardDist's association for no behavioural
	// gain — deploy churn on the slowest resource in the stack, to fix a name.
	dashboardViewerRequest := awscloudfront.NewFunction(stack, jsii.String("InviteRewrite"), &awscloudfront.FunctionProps{
		Runtime: awscloudfront.FunctionRuntime_JS_2_0(),
		Comment: jsii.String("redirect deprecated hostnames to the apex; serve the SPA at /invite"),
		Code: awscloudfront.FunctionCode_FromInline(jsii.String(`function handler(event) {
    var request = event.request;
    var canonical = [` + strings.Join(canonicalJS, ", ") + `];
    var host = (request.headers.host ? request.headers.host.value.toLowerCase() : '').split(':')[0];

    if (canonical.indexOf(host) === -1) {
        // The reconstruction concatenates raw values rather than running them
        // through encodeURIComponent, and that is deliberate: CloudFront hands
        // querystring values to a function STILL PERCENT-ENCODED, so encoding
        // again would double-encode every %XX in the redirect target.
        //
        // AWS does not state the encoding outright, but its own documented
        // normalize-query-string sample performs this exact concatenation and
        // assigns the result back to request.querystring. Were values decoded,
        // that published sample would corrupt any encoded query on every
        // distribution using it. The Host header is documented as raw for the
        // same reason (functions-event-structure.html).
        //
        // So a value carrying an encoded '&' or '#' round-trips intact. If a
        // future reader is tempted to add encodeURIComponent here, the test
        // that pins this is TestDashboardFunction_BehavesCorrectly's
        // encoded-value case in domain_test.go.
        var pairs = [];
        for (var key in request.querystring) {
            var q = request.querystring[key];
            if (q.multiValue) {
                for (var i = 0; i < q.multiValue.length; i++) {
                    pairs.push(key + '=' + q.multiValue[i].value);
                }
            } else if (q.value === '') {
                pairs.push(key);
            } else {
                pairs.push(key + '=' + q.value);
            }
        }
        var query = pairs.length ? '?' + pairs.join('&') : '';
        return {
            statusCode: 301,
            statusDescription: 'Moved Permanently',
            headers: {
                'location': { value: 'https://` + apexHost + `' + request.uri + query },
                'cache-control': { value: 'max-age=3600' }
            }
        };
    }

    if (request.uri === '/invite' || request.uri === '/invite/') {
        request.uri = '/index.html';
    }
    return request;
}`)),
	})

	dashboardDist := awscloudfront.NewDistribution(stack, jsii.String("DashboardCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		// The SPA and the /v1 API answer on these hostnames, and the SPA answers
		// on NO others: the viewer-request function above 301s anything else —
		// in practice the default cloudfront.net name — to the apex.
		//
		// Both names, because the apex is the WebAuthn RP ID from
		// rosterbot-jloe.6 and iOS native ceremonies report their origin as
		// https://<rpId> — so the apex has to be a real served origin, not
		// just a DNS name that redirects. dash. stays because bookmarks name
		// it; it is no longer needed for credentials, since a passkey scoped
		// to the apex RP ID is usable from any subdomain of it.
		//
		// This list and the function's allowlist are the same slice for a
		// reason: a name served here but absent there would 301 away from
		// itself, and the apex missing from there would 301 to itself forever.
		//
		// The certificate must cover BOTH or this deploy fails with
		// InvalidViewerCertificate. It does not by default: SiteCert names
		// dash as its subject and recaps as a SAN, so infra/domain.go adds
		// the apex as a second SAN. That is a REPLACEMENT of the certificate
		// (SubjectAlternativeNames is an update-requires-replacement property
		// on AWS::CertificateManager::Certificate), which is why this deploy
		// is slower and more failure-prone than its diff suggests.
		DomainNames: jsii.Strings(dashboardHosts...),
		Certificate: props.Certificate,
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(dashboardBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
			FunctionAssociations: &[]*awscloudfront.FunctionAssociation{{
				Function:  dashboardViewerRequest,
				EventType: awscloudfront.FunctionEventType_VIEWER_REQUEST,
			}},
		},
		AdditionalBehaviors: &map[string]*awscloudfront.BehaviorOptions{
			"/v1/*": {
				Origin:               awscloudfrontorigins.NewFunctionUrlOrigin(apiURL, nil),
				ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
				AllowedMethods:       awscloudfront.AllowedMethods_ALLOW_ALL(),
				CachePolicy:          awscloudfront.CachePolicy_CACHING_DISABLED(),
				OriginRequestPolicy:  awscloudfront.OriginRequestPolicy_ALL_VIEWER_EXCEPT_HOST_HEADER(),
			},
		},
	})

	// The bot task (projection-site) publishes report/value.json +
	// report/football.json under DashboardBucket's "report/" prefix, so it needs
	// write access here too (previously it only wrote to its own now-removed
	// ReportBucket), plus permission to invalidate DashboardCdn after publishing.
	//
	// Only those two, deliberately. model.json, gap.json and views.json used to
	// ride along here and were therefore world-readable through the default
	// behavior below; they now go to StateBucket's reports/ prefix and are read
	// back through /v1/reports/{name} (rosterbot-crq.3). Nothing published under
	// this prefix should be anything a league rival could not already see.
	dashboardBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("cloudfront:CreateInvalidation"),
		Resources: &[]*string{cfArn(dashboardDist)},
	}))
	// A direct AddEnvironment(DASHBOARD_CF_DIST_ID, dashboardDist.DistributionId())
	// here is the same circular dependency the RP_ID/RP_ORIGIN comment below
	// documents: Task -> DashboardCdn (this GetAtt) while DashboardCdn ->
	// LineupApiFunctionUrl -> apiFn -> Task (apiFn's TASK_DEF env var) closes the
	// loop — confirmed live: CloudFormation's changeset creation rejected the
	// stack with "Circular dependency between resources" (cdk synth alone does
	// not catch this). An ECS Secret backed by an imported SSM parameter doesn't
	// help either: cdk deploy needs to read the parameter's type at synth time,
	// which fails for a parameter this very deploy is about to create. Mirror
	// the RP_ID/RP_ORIGIN fix instead: publish the value into SSM and hand the
	// container only the parameter *name* (a plain string, zero CDK reference)
	// — the bot resolves the actual value via a runtime ssm:GetParameter call
	// (cmd/sync.go's dashboardCFDistID), the same pattern lambda/main.go uses
	// for RP_ID_PARAM/RP_ORIGIN_PARAM.
	awsssm.NewStringParameter(stack, jsii.String("DashboardCfDistIdParam"), &awsssm.StringParameterProps{
		ParameterName: jsii.String("/rosterbot/DASHBOARD_CF_DIST_ID"),
		StringValue:   dashboardDist.DistributionId(),
	})
	botContainer.AddEnvironment(jsii.String("DASHBOARD_CF_DIST_ID_PARAM"), jsii.String("/rosterbot/DASHBOARD_CF_DIST_ID"))

	awscdk.NewCfnOutput(stack, jsii.String("DashboardUrl"), &awscdk.CfnOutputProps{
		Value: jsii.String("https://" + dashHost),
	})
	// DEPRECATED AS A DASHBOARD URL, and still emitted on purpose.
	//
	// It is no longer where anyone signs in: the SPA 301s from here to the
	// apex, and it stopped being the WebAuthn RP ID at rosterbot-jloe.4. What
	// survives is /v1/*, which carries no function association and so still
	// answers on this name. That is the break-glass the props comment at the
	// top of this file worries about — if the apex alias or the us-east-1
	// certificate ever fails, this is the origin the bearer-token recovery
	// curl in docs/aws-deployment.md can still reach. Dropping the output
	// would satisfy the epic's "no in-repo references" tidily and throw that
	// away; the name is not a secret, and an escape hatch nobody can find is
	// not an escape hatch.
	awscdk.NewCfnOutput(stack, jsii.String("DashboardCdnDefaultUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dashboardDist.DistributionDomainName()}),
	})

	dashTarget := awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(dashboardDist))
	awsroute53.NewARecord(stack, jsii.String("DashAliasA"), &awsroute53.ARecordProps{
		Zone: zone, RecordName: jsii.String(dashHost), Target: dashTarget,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("DashAliasAAAA"), &awsroute53.AaaaRecordProps{
		Zone: zone, RecordName: jsii.String(dashHost), Target: dashTarget,
	})
	// The apex, pointed at the same distribution. Route 53 alias records are
	// what make this possible at all — a CNAME is illegal at a zone apex, and
	// CloudFront publishes no stable address to put in an A record.
	awsroute53.NewARecord(stack, jsii.String("ApexAliasA"), &awsroute53.ARecordProps{
		Zone: zone, RecordName: jsii.String(apexHost), Target: dashTarget,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("ApexAliasAAAA"), &awsroute53.AaaaRecordProps{
		Zone: zone, RecordName: jsii.String(apexHost), Target: dashTarget,
	})

	// Mail for the apex, so the privacy contact published at
	// rosterbot.dev/privacy.html actually receives deletion requests. Web
	// records above, mail records here — same zone, unrelated failure modes.
	addMailRecords(stack, zone)
	awscdk.NewCfnOutput(stack, jsii.String("DashboardCdnId"), &awscdk.CfnOutputProps{Value: dashboardDist.DistributionId()})

	// The Lambda's WebAuthn RP config needs the dashboard's own origin, which
	// only exists once this distribution is created. Deferring apiFn's env-var
	// wiring to after dashboardDist's construction (as a prior version of this
	// code did) does NOT break the circular dependency — it only defers when
	// the Go code runs, not the CloudFormation resource graph: an env var set
	// to dashboardDist.DistributionDomainName() is still a Fn::GetAtt on the
	// Distribution, and the Distribution's own origin config references
	// apiFn's Function URL (Distribution -> FunctionUrl -> apiFn), so a direct
	// GetAtt the other way (apiFn -> Distribution) is a genuine cycle that CDK
	// synth rejects at deploy time. Instead, publish the domain into SSM
	// (mirroring API_TOKEN_PARAM/SESSION_SECRET_PARAM) and hand apiFn only the
	// literal parameter *names* — a plain string, not a resource reference —
	// so apiFn has zero CloudFormation dependency on dashboardDist. The Lambda
	// fetches the actual values from SSM at cold start (see lambda/main.go).
	//
	// THE CUTOVER (rosterbot-jloe.4). These two values used to be a GetAtt on
	// dashboardDist.DistributionDomainName() — the cloudfront.net name — and
	// slices 2/3 deliberately left them that way so the hostname migration
	// shipped with zero passkey impact. This is the one-way door: every
	// passkey registered against the old RP ID stops validating the instant
	// this deploys. There is no dual-RP-ID mode. Recovery is the break-glass
	// primitive proven end-to-end on 2026-08-18 (bearer token -> POST
	// /v1/tenants/{id}/recovery -> redeem on the NEW origin) — see
	// docs/user-registration.md and TestRpParams_NameTheAliasHostAfterCutover
	// below, which now asserts the opposite of what it asserted before this
	// commit. That is deliberate, not drift: the two literal strings ARE the
	// cutover.
	//
	// A LITERAL STRING, not dashboardDist.DistributionDomainName(), for a
	// second reason beyond "that's the whole point": a GetAtt here would make
	// the RP ID track the distribution's domain automatically, so a future
	// distribution REPLACEMENT (new logical id, new cloudfront.net name) would
	// silently rewrite the RP ID again and invalidate every passkey a second
	// time with no code change and no one deciding it. A literal is inert to
	// that — it changes only when someone edits this line.
	//
	// Existing warm Lambda execution environments read these values ONCE, in
	// main() (lambda/main.go), so a bare `cdk deploy` does not itself force a
	// recycle: the SSM value changes but nothing about apiFn's own
	// CloudFormation properties does, so CloudFormation has no reason to
	// replace the function. Force one explicitly after this deploys:
	//   aws lambda update-function-configuration --region us-west-1 \
	//     --function-name <LineupApi> --description "rp-cutover-$(date -u +%Y%m%dT%H%M%SZ)"
	// A configuration update is enough to start fresh execution environments
	// without a code change.
	// THE SECOND CUTOVER (rosterbot-jloe.6). jloe.4 moved the RP ID from the
	// cloudfront.net name to dash.; this moves it to the apex, which is the
	// permanent, subdomain-portable identity the iOS app associates its
	// webcredentials with. Same one-way door, same recovery primitive, and
	// deliberately taken now: only the operator's passkey exists, so the cost
	// is one re-enrollment by one person.
	//
	// RP ID is the APEX while the origin list contains BOTH. Those are
	// different things and the asymmetry is the point. RP ID is the identity a
	// credential is bound to, and a credential scoped to rosterbot.dev is
	// usable from any subdomain of it — that is what makes the apex the
	// portable choice. Origin is the concrete URL a ceremony was performed
	// from, and there are genuinely two: the browser reports
	// https://dash.rosterbot.dev, while an iOS native ceremony has no browser
	// origin at all and reports https://<rpId>. Listing only one would refuse
	// every ceremony from the other surface.
	awsssm.NewStringParameter(stack, jsii.String("RpIdParam"), &awsssm.StringParameterProps{
		ParameterName: jsii.String("/rosterbot/DASHBOARD_RP_ID"),
		StringValue:   jsii.String(apexHost),
	})
	// Comma-separated rather than a second parameter: one value to keep in
	// sync, one IAM resource, one cold-start read, and the list is read as a
	// unit by the one caller that needs it. lambda/main.go splits it.
	awsssm.NewStringParameter(stack, jsii.String("RpOriginParam"), &awsssm.StringParameterProps{
		ParameterName: jsii.String("/rosterbot/DASHBOARD_RP_ORIGIN"),
		StringValue:   jsii.String("https://" + apexHost + ",https://" + dashHost),
	})
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: jsii.Strings("ssm:GetParameter"),
		Resources: jsii.Strings(
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/DASHBOARD_RP_ID",
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/DASHBOARD_RP_ORIGIN",
		),
	}))
	apiFn.AddEnvironment(jsii.String("RP_ID_PARAM"), jsii.String("/rosterbot/DASHBOARD_RP_ID"), nil)
	apiFn.AddEnvironment(jsii.String("RP_ORIGIN_PARAM"), jsii.String("/rosterbot/DASHBOARD_RP_ORIGIN"), nil)

	// --- Ops notification: Pushover for CodeBuild + ECS task outcomes ---
	// Created UNCONDITIONALLY, unlike the CodeBuild rule below. Job-failure
	// alerting must survive a stack deployed without `-c enableBuild=true`,
	// and the previous BuildNotify function lived entirely inside that gate.
	// Timeout is 60s, well above the sibling Lambdas' 10s: before it can decide
	// whether to alert, the handler reads up to ledgerWindow (200) run-ledger
	// objects sequentially (opsnotify/ledger.go), and the live ledger already
	// holds enough entries that every invocation hits the full 200-object walk,
	// not just a worst case. A timeout here is a *silently missed* alert, not
	// a slow one — handleTask reads the ledger before sending Pushover, so
	// EventBridge's retry just re-runs the same slow path. Lambda bills on
	// duration actually used, so a generous ceiling costs nothing.
	opsNotifyFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("OpsNotify"), &awscdklambdagoalpha.GoFunctionProps{
		Entry:        jsii.String("../opsnotify"),
		Bundling:     deterministicGoBundling,
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(60)),
		Environment: &map[string]*string{
			"STATE_BUCKET": stateBucket.BucketName(),
		},
	})
	opsNotifyFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: jsii.Strings("ssm:GetParameter"),
		Resources: jsii.Strings(
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_USER_KEY",
			"arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/PUSHOVER_API_TOKEN",
		),
	}))
	// Read-only on the run ledger: the notifier derives failure streaks from it
	// and writes nothing. Mirrors the API Lambda's own runledger/ grant.
	stateBucket.GrantRead(opsNotifyFn, jsii.String("runledger/*"))
	// Read-write on its own marker prefix, and only that one. This is the sole
	// place the notifier writes anything; scoping it here keeps the "a notifier
	// cannot mutate the bot's state" property intact while letting it remember
	// which alerts it has already sent (rosterbot-chs, rosterbot-ys8).
	stateBucket.GrantReadWrite(opsNotifyFn, jsii.String("opsalert/*"))

	// Every scheduled job failure (rosterbot-naz). The pattern deliberately
	// stops at "a task in our cluster reached STOPPED" — whether that stop was
	// a failure is decided in Go (opsnotify/task.go), where "exit code absent
	// OR non-zero" is table-testable instead of an event-pattern puzzle over an
	// array of objects. Cost is ~700 invocations/month, inside the free tier.
	awsevents.NewRule(stack, jsii.String("TaskFailRule"), &awsevents.RuleProps{
		EventPattern: &awsevents.EventPattern{
			Source:     jsii.Strings("aws.ecs"),
			DetailType: jsii.Strings("ECS Task State Change"),
			Detail: &map[string]interface{}{
				"clusterArn": []interface{}{cluster.ClusterArn()},
				"lastStatus": []interface{}{"STOPPED"},
			},
		},
		Targets: &[]awsevents.IRuleTarget{
			awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{}),
		},
	})

	// --- CIS CloudTrail alarms (replaces the disabled Security Hub) ---
	//
	// See cloudtrail.go for why these fourteen controls are built directly
	// rather than through Security Hub. addCISTrailAlarms creates every alarm
	// and returns only the names of the ones that should page, so the
	// deploy-driven majority record a breach in CloudWatch without waking
	// anyone.
	if pagingAlarms := addCISTrailAlarms(stack); len(pagingAlarms) > 0 {
		names := make([]interface{}, 0, len(pagingAlarms))
		for _, n := range pagingAlarms {
			names = append(names, n)
		}
		// Filtering by alarm NAME in the event pattern, rather than delivering
		// all fourteen and discarding in Go, is what keeps a `cdk deploy` from
		// costing a Lambda invocation per tripped alarm. The names come from
		// cisAlarm.alarmName so the rule and the alarms cannot drift apart.
		awsevents.NewRule(stack, jsii.String("CisAlarmRule"), &awsevents.RuleProps{
			EventPattern: &awsevents.EventPattern{
				Source:     jsii.Strings("aws.cloudwatch"),
				DetailType: jsii.Strings("CloudWatch Alarm State Change"),
				Detail: &map[string]interface{}{
					"alarmName": names,
					// ALARM only. Forwarding OK transitions would pair every
					// alert with a recovery notice for something that has no
					// recovery: these fire on an event that already happened,
					// so "back to normal" reports nothing.
					"state": map[string]interface{}{
						"value": []interface{}{"ALARM"},
					},
				},
			},
			Targets: &[]awsevents.IRuleTarget{
				awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{}),
			},
		})
	}

	// One declaration of the repository coordinates, consumed by the webhook
	// source below and by the drift check's GITHUB_REPO in Phase 4. A checker
	// that restated them could drift from the thing it is checking.
	const ghOwner, ghRepo = "nixon-commits", "rosterbot"
	// buildProject escapes the gate so Phase 4 can wire the scheduled drift
	// check beside the heartbeat, where it belongs: both are scheduled
	// assertions, not reactions. Nil when the stack is deployed without
	// `-c enableBuild=true`, and Phase 4 skips the check accordingly.
	var buildProject awscodebuild.IProject

	// --- Phase 2: CodeBuild (build + push image to ECR on push to main) ---
	// Gated: only instantiated with `-c enableBuild=true`, because the GitHub
	// webhook source requires a one-time source credential (GitHub OAuth/PAT) to
	// exist in the account first. Until then the stack deploys without it.
	if v, ok := stack.Node().TryGetContext(jsii.String("enableBuild")).(string); ok && v == "true" {
		// S3 cache for the build's Go caches (GOCACHE/GOMODCACHE — the paths
		// live in buildspec.yml's cache: block, pinned to these by
		// TestBuildspec_GoCacheDirsArePinnedAndCached). Without it, `cdk synth`
		// recompiles the aws-cdk-go bindings plus both bundled lambda modules
		// from scratch on every push — measured 130s of build 290's 380s total
		// (2026-08-29), the single largest segment of the build. Local caching
		// is not a substitute here: it is best-effort on on-demand hosts, which
		// is exactly the "works on recycled hosts, cold on fresh ones" variance
		// the build already shows.
		//
		// 30-day expiry, not keep-forever: a build cache is regenerable by
		// definition, and an unexpiring prefix is the same slow leak the state
		// bucket's lifecycle rules exist to stop (563 GB vs ~1 GB live,
		// measured 2026-08-18). CodeBuild rewrites the cache object on every
		// build, so expiry can only ever collect a cache no build has touched
		// in a month — which is a cache worth losing.
		buildCache := awss3.NewBucket(stack, jsii.String("BuildCache"), &awss3.BucketProps{
			RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
			AutoDeleteObjects: jsii.Bool(true),
			LifecycleRules: &[]*awss3.LifecycleRule{{
				Id:         jsii.String("ExpireBuildCache"),
				Expiration: awscdk.Duration_Days(jsii.Number(30)),
			}},
		})
		project := awscodebuild.NewProject(stack, jsii.String("Build"), &awscodebuild.ProjectProps{
			Source: awscodebuild.Source_GitHub(&awscodebuild.GitHubSourceProps{
				Owner:   jsii.String(ghOwner),
				Repo:    jsii.String(ghRepo),
				Webhook: jsii.Bool(true),
				// Shallow: nothing in the build reads git history. TAG comes
				// from CODEBUILD_RESOLVED_SOURCE_VERSION, the Go builds run
				// -buildvcs=false, and the buildspec invokes no git command —
				// while an unset depth clones the full history on every push.
				CloneDepth: jsii.Number(1),
				WebhookFilters: &[]awscodebuild.FilterGroup{
					awscodebuild.FilterGroup_InEventOf(awscodebuild.EventAction_PUSH).
						AndBranchIs(jsii.String("main")),
				},
			}),
			Cache: awscodebuild.Cache_Bucket(buildCache, &awscodebuild.BucketCacheOptions{}),
			Environment: &awscodebuild.BuildEnvironment{
				// ARM build host so the image matches the Graviton task definition.
				//
				// AL2023, not the older amazonlinux2-aarch64 image: that one carried a
				// fixed Node 18 / Go 1.20 with no `runtime-versions` selection, so the
				// aws-cdk CLI warned "Node 18 is end-of-life" on every install phase
				// (rosterbot-2no). The AL2023 image exposes nodejs 18/20/22/24 and
				// golang 1.20-1.26, so buildspec.yml pins the ones this build uses
				// rather than inheriting a default that shifts on each EOL.
				BuildImage: awscodebuild.LinuxArmBuildImage_AMAZON_LINUX_2023_STANDARD_3_0(),
				// SMALL, not LinuxArmBuildImage's default of LARGE — which was
				// inherited, never chosen. us-west-1 on-demand: ARM g1.large is
				// $0.0175/min against g1.small at $0.00425/min, 4.1x. Measured on a
				// representative build (2026-08-17), only `docker build` (46s of
				// 201s) is CPU-bound: cdk deploy, both docker pushes and the Node
				// runtime install are network-bound and do not move with vCPU count.
				// So the build stretches but the bill does not follow it — even at 6
				// billed minutes instead of 4 this is ~2.7x cheaper.
				//
				// The constraint to watch is 4 GB of RAM against 16, and the thing
				// most likely to notice is the headless chromium smoke test in the
				// Dockerfile. If that starts failing here while still passing
				// locally on Apple Silicon, this line is the first suspect.
				ComputeType: awscodebuild.ComputeType_SMALL,
				Privileged:  jsii.Bool(true), // docker build
			},
			EnvironmentVariables: &map[string]*awscodebuild.BuildEnvironmentVariable{
				"ECR_URI": {Value: repo.RepositoryUri()},
				// Launch coordinates for the post-build projection-site render so a
				// push to main re-renders the dashboard immediately instead of
				// waiting for the daily ProjectionSite schedule. Reuses the same
				// egress-only SG + public subnets the API uses to launch tasks.
				"CLUSTER":         {Value: cluster.ClusterArn()},
				"TASK_DEF":        {Value: taskDef.TaskDefinitionArn()},
				"SUBNETS":         {Value: awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds)},
				"SECURITY_GROUPS": {Value: taskSg.SecurityGroupId()},
			},
			// -1, not omitted: CloudFormation cannot clear a CodeBuild property by
			// leaving it out (rosterbot-7p1i). The limit was set to 1 and reverted
			// the same day (rosterbot-ill) by DELETING this line — and the revert
			// never took effect. Measured 2026-08-20, six days and ~68 deploys
			// later: the deployed template carried no ConcurrentBuildLimit while
			// the live project still reported 1. AWS states the rule on CfnProject
			// — "to unset or remove a project value via CFN, explicitly provide the
			// attribute" — so an omitted property is not an assertion that it is
			// unset, it is silence, and CodeBuild keeps whatever it already had.
			// -1 is the API's remove-the-limit sentinel (verified live: the field
			// reads back absent), and the CFN resource schema puts no minimum on
			// it, so the value survives validation on the way through.
			//
			// The limit is wrong for this project because it THROTTLES the excess
			// build rather than queueing it: "new builds are throttled and are not
			// run." A push landing during an in-flight build never builds at all —
			// main sits deployed-behind with no failed build, no Pushover and
			// nothing red anywhere. queuedTimeoutInMinutes does not rescue it;
			// that governs compute-capacity queuing, not the concurrency
			// rejection, which happens BEFORE a build record exists. So anything
			// watching build outcomes structurally cannot see this — there is no
			// outcome. Two deploys were dropped in two days (6056658, bb1cc1e) and
			// the only evidence in the entire system was an HTTP 400 in the GitHub
			// webhook delivery log.
			//
			// A racing build fails LOUDLY and the next push retries it. A dropped
			// build is silent and permanent. Given the choice, take the noisy
			// failure — the same reasoning as every other guard in this repo.
			// BuildDriftRule below is the counterweight for the dropped case,
			// whatever its cause: it compares main's HEAD against the newest
			// successfully built commit, so a trigger that fails for a reason
			// nobody predicted still surfaces. This one failed for a reason
			// everybody thought was already fixed.
			ConcurrentBuildLimit: jsii.Number(-1),
		})
		// The other half of rosterbot-ill (set -e in the buildspec's cdk block, so a
		// failed deploy is reported at the deploy instead of blamed on the next
		// step) is kept: it makes the loud failure legible, which is the part that
		// was actually costing time.
		repo.GrantPullPush(project)
		// Let the build launch the projection-site task (ecs:RunTask + the
		// iam:PassRole on the task's execution/task roles that RunTask requires).
		taskDef.GrantRun(project)
		// Let the build publish the static dashboard: write its bucket, then
		// invalidate its distribution so the new build is served immediately.
		dashboardBucket.GrantReadWrite(project, nil)
		project.Role().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
			Actions:   jsii.Strings("cloudfront:CreateInvalidation"),
			Resources: &[]*string{cfArn(dashboardDist)},
		}))
		// Let a push-to-main build run `cdk deploy` (buildspec post_build) so
		// infra changes ship on merge, not just the image. cdk v2 performs all
		// CloudFormation/IAM work through the bootstrap roles, so the build role
		// only needs to assume them — not broad admin. Wildcard covers the
		// deploy / file-publishing / image-publishing / lookup roles.
		project.Role().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
			Actions:   jsii.Strings("sts:AssumeRole"),
			Resources: jsii.Strings("arn:aws:iam::476646938644:role/cdk-hnb659fds-*"),
		}))
		awscdk.NewCfnOutput(stack, jsii.String("BuildProject"), &awscdk.CfnOutputProps{Value: project.ProjectName()})

		// Pushover on every terminal build outcome (rosterbot-00j). This catches
		// every failure phase (install/pre_build/build/deploy) + success — unlike
		// a buildspec curl, which never runs if install/pre_build fail. The
		// target is the shared OpsNotify function created above; only this rule
		// is gated, because it references the gated CodeBuild project.
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
				awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{}),
			},
		})

		buildProject = project
	}

	// --- Analysis Store: Glue table over analysis/grades + Athena workgroup ---
	glueDB := awsglue.NewCfnDatabase(stack, jsii.String("AnalysisDB"), &awsglue.CfnDatabaseProps{
		CatalogId:     stack.Account(),
		DatabaseInput: &awsglue.CfnDatabase_DatabaseInputProperty{Name: jsii.String("rosterbot_analysis")},
	})

	gradesLoc := awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("s3://"), stateBucket.BucketName(), jsii.String("/analysis/grades/")})
	col := func(name, typ string) interface{} {
		return &awsglue.CfnTable_ColumnProperty{Name: jsii.String(name), Type: jsii.String(typ)}
	}
	gradesTable := awsglue.NewCfnTable(stack, jsii.String("GradesTable"), &awsglue.CfnTableProps{
		CatalogId:    stack.Account(),
		DatabaseName: jsii.String("rosterbot_analysis"),
		TableInput: &awsglue.CfnTable_TableInputProperty{
			Name:          jsii.String("grades"),
			TableType:     jsii.String("EXTERNAL_TABLE"),
			PartitionKeys: &[]interface{}{col("user", "string"), col("dt", "string"), col("system", "string")},
			Parameters: &map[string]*string{
				"classification":              jsii.String("json"),
				"projection.enabled":          jsii.String("true"),
				"projection.dt.type":          jsii.String("date"),
				"projection.dt.format":        jsii.String("yyyy-MM-dd"),
				"projection.dt.range":         jsii.String("2026-01-01,NOW"),
				"projection.dt.interval":      jsii.String("1"),
				"projection.dt.interval.unit": jsii.String("DAYS"),
				// system is an enum projection over the captured projection
				// systems. Legacy objects without a system= path segment predate
				// this partition and are not visible to Athena (the report reads
				// them via the store readers, which attribute them to depthcharts-ros).
				"projection.system.type":   jsii.String("enum"),
				"projection.system.values": jsii.String("steamer-ros,depthcharts-ros,thebatx-ros,atc-ros"),
				// user is INJECTED rather than enum: tenant ids are opaque 64-byte
				// handles that cannot be enumerated in a template, and an enum
				// would need a stack deploy on every signup. Injected means every
				// query must name the tenant in its WHERE clause, which is a
				// constraint worth having — it makes an accidental cross-tenant
				// scan impossible to write by omission.
				"projection.user.type":      jsii.String("injected"),
				"storage.location.template": awscdk.Fn_Join(jsii.String(""), &[]*string{gradesLoc, jsii.String("user=${user}/dt=${dt}/system=${system}/")}),
			},
			StorageDescriptor: &awsglue.CfnTable_StorageDescriptorProperty{
				Location:     gradesLoc,
				InputFormat:  jsii.String("org.apache.hadoop.mapred.TextInputFormat"),
				OutputFormat: jsii.String("org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"),
				SerdeInfo: &awsglue.CfnTable_SerdeInfoProperty{
					SerializationLibrary: jsii.String("org.openx.data.jsonserde.JsonSerDe"),
				},
				Columns: &[]interface{}{
					col("player_id", "string"), col("name", "string"), col("mlb_team", "string"),
					col("projected", "double"), col("actual", "double"), col("diff", "double"),
					col("bucket", "string"), col("is_pitcher", "boolean"), col("source", "string"),
				},
			},
		},
	})
	gradesTable.AddDependency(glueDB)

	// Recap site readership. CloudFront standard logs are gzipped TSV with two
	// leading comment lines (#Version, #Fields), written FLAT as
	// recap/<dist-id>.YYYY-MM-DD-HH.<hash>.gz — no Hive dt= layout, so unlike
	// grades this table takes no partition projection and simply scans the
	// prefix. At a ~12-person league's request volume that is a rounding error,
	// and partitioning a flat key layout would mean rewriting the objects.
	//
	// Only the first 14 fields of CloudFront's ~33 are declared. LazySimpleSerDe
	// ignores trailing fields, and 14 reaches everything asked for — timestamp,
	// client IP, which page, plus the edge location that serves as free coarse
	// geo. Extending later means appending columns in CloudFront's documented
	// order; the positions above must not be reordered.
	recapLogsLoc := awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("s3://"), recapLogBucket.BucketName(), jsii.String("/recap/")})
	recapLogsTable := awsglue.NewCfnTable(stack, jsii.String("RecapAccessLogsTable"), &awsglue.CfnTableProps{
		CatalogId:    stack.Account(),
		DatabaseName: jsii.String("rosterbot_analysis"),
		TableInput: &awsglue.CfnTable_TableInputProperty{
			Name:      jsii.String("recap_access_logs"),
			TableType: jsii.String("EXTERNAL_TABLE"),
			Parameters: &map[string]*string{
				"classification": jsii.String("csv"),
				// The #Version and #Fields comment lines are data rows to a TSV
				// reader; without this every query returns two junk rows.
				"skip.header.line.count": jsii.String("2"),
			},
			StorageDescriptor: &awsglue.CfnTable_StorageDescriptorProperty{
				Location:     recapLogsLoc,
				InputFormat:  jsii.String("org.apache.hadoop.mapred.TextInputFormat"),
				OutputFormat: jsii.String("org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"),
				SerdeInfo: &awsglue.CfnTable_SerdeInfoProperty{
					SerializationLibrary: jsii.String("org.apache.hadoop.hive.serde2.lazy.LazySimpleSerDe"),
					Parameters:           &map[string]*string{"field.delim": jsii.String("\t")},
				},
				// Named for querying rather than mirroring CloudFront's own field
				// names: `date` and `time` are reserved words in Athena, and the
				// TSV mapping is positional anyway.
				Columns: &[]interface{}{
					col("log_date", "string"), col("log_time", "string"), col("edge_location", "string"),
					col("sc_bytes", "bigint"), col("client_ip", "string"), col("method", "string"),
					col("host", "string"), col("uri_stem", "string"), col("status", "int"),
					col("referer", "string"), col("user_agent", "string"), col("uri_query", "string"),
					col("cookie", "string"), col("edge_result_type", "string"),
				},
			},
		},
	})
	recapLogsTable.AddDependency(glueDB)

	awsathena.NewCfnWorkGroup(stack, jsii.String("AnalysisWG"), &awsathena.CfnWorkGroupProps{
		Name: jsii.String("rosterbot"),
		WorkGroupConfiguration: &awsathena.CfnWorkGroup_WorkGroupConfigurationProperty{
			ResultConfiguration: &awsathena.CfnWorkGroup_ResultConfigurationProperty{
				OutputLocation: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("s3://"), stateBucket.BucketName(), jsii.String("/athena-results/")}),
			},
		},
	})

	// --- Phase 4: schedules (1:1 port of the 8 GHA workflows) ---
	// All crons are UTC (EventBridge rules are UTC-only). claims is offset +20m
	// from transactions so their shared cache/ write-back doesn't race.
	//
	// Post-cutover (2026-06-16) the schedules are the live driver, so they are
	// ENABLED by default — a routine `cdk deploy` keeps them running. Pass
	// `-c schedulesEnabled=false` as an explicit kill switch to pause all jobs.
	schedulesEnabled := true
	if v, ok := stack.Node().TryGetContext(jsii.String("schedulesEnabled")).(string); ok && v == "false" {
		schedulesEnabled = false
	}
	// maxGap is how long a job may go without launching before the heartbeat
	// check calls it dead (rosterbot-ys8). It lives here, beside the cron it
	// describes, because the two only mean anything together — a tolerance kept
	// in the notifier would drift from the schedule it claims to describe, and
	// would do so silently, surfacing only as an alert that never fires. The
	// notifier receives this table through JOB_SCHEDULES below.
	//
	// Each value is the job's longest legitimate quiet period plus slack, not
	// its nominal period: Lineup runs hourly but only 14:00-03:00 UTC, so its
	// real worst case is the 11h overnight gap, and a "1h" tolerance would page
	// every single morning.
	const (
		hourlyGap    = 13 * time.Hour     // 11h overnight window + 2h slack
		sixHourlyGap = 8 * time.Hour      // 6h nominal + 2h slack; runs all day, no Lineup-style window
		dailyGap     = 26 * time.Hour     // 24h + 2h slack
		weeklyGap    = 8 * 24 * time.Hour // 7d + 1d slack
	)
	type job struct {
		id, cron string
		cmd      *[]*string
		maxGap   time.Duration
	}
	jobs := []job{
		{"Lineup", "cron(0 14-23,0-3 * * ? *)", jsii.Strings("optimize", "--matchup", "--archive-projections"), hourlyGap},
		// Asserts the pinned Fantrax API version still passes Fantrax's gate.
		// 10:30 UTC sits ahead of the first daily job (Prospects, 11:00) and well
		// ahead of the hourly Lineup window (14:00-03:00), so a dead pin is known
		// before the day's cascade rather than after it. Exits non-zero on
		// STALE_CLIENT, so the alert rides the ordinary task-failure path.
		{"VersionCheck", "cron(30 10 * * ? *)", jsii.Strings("version-check"), dailyGap},
		{"Prospects", "cron(0 11 * * ? *)", jsii.Strings("prospects"), dailyGap},
		{"GsCheck", "cron(0 12 * * ? *)", jsii.Strings("gs-check"), dailyGap},
		{"Waivers", "cron(0 13 * * ? *)", jsii.Strings("waivers"), dailyGap},
		{"Transactions", "cron(0 14 * * ? *)", jsii.Strings("transactions"), dailyGap},
		{"Claims", "cron(20 14 * * ? *)", jsii.Strings("claims"), dailyGap},
		{"Recap", "cron(0 11 ? * MON *)", jsii.Strings("recap-site", "--out", "dist"), weeklyGap},
		{"Backtest", "cron(0 12 ? * MON *)", jsii.Strings("backtest"), weeklyGap},
		{"Grade", "cron(30 13 * * ? *)", jsii.Strings("grade"), dailyGap},
		// SPLIT IN TWO (rosterbot-crq.14 follow-up). projection-site produces
		// two unrelated kinds of artifact: the three PRIVATE per-tenant reports
		// (model, gap, views) and the league-wide PUBLIC value.json /
		// football.json. It was excluded from perTenantJobs because of the
		// second half, which meant the FIRST half only ever ran as the operator
		// — layout.Reports is PerTenant and resolved per caller, so every other
		// tenant read a permanent 404 on their own model, gap and views.
		//
		// The league-wide half stays a singleton: N tenants publishing
		// value.json would be N tasks racing on one key, and publishSite
		// mirrors that directory with --delete.
		{"ProjectionSite", "cron(0 15 * * ? *)", jsii.Strings("projection-site", "--out", "report", "--scope", "league"), dailyGap},
		// The per-tenant half, five minutes later so it reads the same day's
		// grades without contending with the league render.
		{"ProjectionReports", "cron(5 15 * * ? *)", jsii.Strings("projection-site", "--scope", "tenant"), dailyGap},
		// daily capture of ephemeral upstream data (HKB, projections, Savant, prospects) after upstreams' once-daily refresh
		{"Archive", "cron(15 14 * * ? *)", jsii.Strings("archive"), dailyGap},
		// Appends today's per-team aggregate HKB dynasty value to the Team Value
		// Store. Runs 14:30 UTC (after Archive's HKB refresh, before ProjectionSite
		// at 15:00) so value.json renders today's point same-day. Accumulates
		// forward — HKB has no history — so one write per day is the whole record.
		{"TeamValues", "cron(30 14 * * ? *)", jsii.Strings("team-values"), dailyGap},
		// Appends today's per-player dynasty value rows (players + reconstructed
		// picks) to the Dynasty Value Store. Runs 14:45 UTC -- after Archive
		// (14:15) and TeamValues (14:30), before ProjectionSite (15:00) reads it,
		// the same staging as TeamValues -- football's Sleeper/StatsGuy analog.
		{"FootballValues", "cron(45 14 * * ? *)", jsii.Strings("football-values"), dailyGap},
		// Polls Sleeper league transactions for newly completed trades and
		// pushes a graded Pushover alert. Every 6 hours (offset :45, off the
		// top of the hour the daily jobs crowd) -- trades don't happen on a
		// predictable schedule, so this is a poll, not a once-daily capture
		// like TeamValues/FootballValues. Idempotent via a per-transaction_id
		// dedup marker, so overlapping polls never double-alert.
		{"FootballTrades", "cron(45 */6 * * ? *)", jsii.Strings("football-trades"), sixHourlyGap},
		// Shadow captures every projection system's lineup projection for the
		// model-comparison report. It runs in the MORNING UTC window, and that
		// is a correctness requirement, not a preference.
		//
		// It used to run at 23:40 UTC. On 2026-08-19 FanGraphs put
		// /api/projections behind a Cloudflare interactive challenge that holds
		// from roughly 17:00 to 03:00 UTC and clears outside it, so 23:40 sat
		// squarely inside the blocked window. Shadow is the ONLY fetcher of the
		// atc-ros and thebatx-ros projections, so it kept capturing whatever
		// the stale fallback held: measured 2026-08-21, both had last refreshed
		// on 2026-08-18, and three nights of "model comparison" had silently
		// graded three systems against one system's three-day-old numbers.
		//
		// Two constraints bound the new slot, and 11:30 UTC satisfies both.
		// The snapshot's generated_at must fall on the same ET calendar day as
		// the date it projects or backtest's sameETDate guard excludes it — ET
		// rolls at 04:00 UTC, so any morning-UTC time works and any late-night
		// one does not. And it must precede the next day's Grade (13:30 UTC),
		// which scores the per-system snapshots.
		//
		// The cost is real and worth stating: the capture moves from ~19:40 ET,
		// after first pitch, to ~07:30 ET, before it. That is a better forecast
		// — it is what the systems would actually have told you in time to act
		// — but it is NOT the same measurement, so the shadow series has a
		// discontinuity at this change and pre/post windows should not be
		// pooled.
		{"Shadow", "cron(30 11 * * ? *)", jsii.Strings("shadow"), dailyGap},
	}
	// --- Per-tenant fan-out (rosterbot-crq.13) ---
	//
	// perTenantJobs names the schedules that run once PER TENANT. Everything
	// absent from this set stays a league-wide singleton targeting ECS
	// directly, and that split is what keeps the expensive rate-limited
	// upstreams (FanGraphs, Savant, HKB, MLB) at one call per day rather than
	// one per tenant per day — layout.Cache is deliberately NOT PerTenant for
	// the same reason.
	//
	//	Lineup     applies to the tenant's own team; writes Lineup, Backtest,
	//	           Trades and TradeOffers, all PerTenant.
	//	Grade      writes Analysis and LineupGaps, both PerTenant.
	//	Backtest   reads the tenant's own PerTenant Backtest snapshots.
	//	Shadow     writes per-system snapshots under PerTenant Backtest.
	//	Prospects  the non-obvious one — cmd/prospects.go has no TeamID in it,
	//	           but internal/prospects/run.go calls ft.GetMinorsRoster(),
	//	           which is team-scoped.
	//
	// ProjectionSite is deliberately ABSENT despite writing PerTenant Reports:
	// it also publishes the league-wide PUBLIC report/value.json and
	// report/football.json with --delete, so N concurrent tasks would each
	// delete the others' output. It has to be split into a per-tenant half and
	// a singleton half before it can join this set.
	//
	// Waivers, Transactions and GsCheck are league-wide as written — no team
	// scoping at all — so they are singletons, not per-tenant jobs.
	perTenantJobs := map[string]bool{
		"Lineup": true, "Grade": true, "Backtest": true, "Shadow": true, "Prospects": true,
		// The private-reports half of projection-site. Its league-wide sibling
		// (ProjectionSite) is deliberately absent — see the schedule table.
		"ProjectionReports": true,
	}
	// A typo'd key would not fail — it would silently mean "not per-tenant",
	// leaving that job running once for the operator forever while looking
	// wired. Same reason check-pins is a hard error rather than a skip.
	{
		known := map[string]bool{}
		for _, j := range jobs {
			known[j.id] = true
		}
		for id := range perTenantJobs {
			if !known[id] {
				panic(fmt.Sprintf("perTenantJobs names %q, which is not a job in the schedule table", id))
			}
		}
	}

	// The dispatcher exists because ONE RULE CAN ONLY EVER PRODUCE ONE
	// RunTask: awseventstargets.TaskEnvironmentVariable.Value is a required
	// static string, so "one task per row in a table" cannot be expressed in a
	// rule at all. The rule hands this function the job's argv; it enumerates
	// active tenants and launches one task each.
	dispatchFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("Dispatch"), &awscdklambdagoalpha.GoFunctionProps{
		Entry:        jsii.String("../lambda/dispatch"),
		Bundling:     deterministicGoBundling,
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Architecture: awslambda.Architecture_ARM_64(),
		// Generous next to the API's 10s: this makes one DynamoDB read plus one
		// RunTask per tenant, and a timeout here silently drops whichever
		// tenants had not been reached yet.
		Timeout: awscdk.Duration_Seconds(jsii.Number(60)),
		Environment: &map[string]*string{
			"IDENTITY_TABLE":  identityTable.TableName(),
			"CLUSTER":         cluster.ClusterArn(),
			"TASK_DEF":        taskDef.TaskDefinitionArn(),
			"SUBNETS":         awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds),
			"SECURITY_GROUPS": taskSg.SecurityGroupId(),
			"CONTAINER_NAME":  jsii.String("bot"),
		},
	})
	// Read-only on the directory: the dispatcher enumerates tenants and must
	// never be able to alter one. It also gets no KMS grant of either kind — it
	// never touches a credential, only an id.
	identityTable.GrantReadData(dispatchFn)
	// RunTask scoped to the same ARN handed to it as TASK_DEF above, so the
	// permission and the argument come from one CDK token and cannot drift to
	// different revisions. Mirrors what apiFn already does for POST /v1/jobs.
	dispatchFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("ecs:RunTask"),
		Resources: jsii.Strings(*taskDef.TaskDefinitionArn()),
	}))
	dispatchPassRoles := []*string{taskDef.TaskRole().RoleArn()}
	if taskDef.ExecutionRole() != nil {
		dispatchPassRoles = append(dispatchPassRoles, taskDef.ExecutionRole().RoleArn())
	}
	dispatchFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("iam:PassRole"),
		Resources: &dispatchPassRoles,
	}))
	// The active tenant roster (rosterbot-crq.4): the dispatcher publishes the
	// list it has just read in order to do its work, and the notifier reads it
	// so opsalert.Overdue can assert on a tenant that has NEVER run. Without it
	// Overdue derives tenants from the ledger and can only surface one that ran
	// and then stopped. Write for one, read for the other — neither needs the
	// opposite.
	//
	// The prefix is a LITERAL here while both Go sides derive it from
	// layout.TenantRoster, and that asymmetry is deliberate rather than an
	// oversight. infra/ has no dependency on the application module at all;
	// adding one for a single constant would pull the root's whole module graph
	// into infra/go.sum and put a fourth file in dependabot's grouped bumps.
	//
	// It is acceptable HERE and was not acceptable in cmd/sync.go — which
	// restated "session/" and blinded ops for three days — because the two
	// failures differ in kind. There, a literal decided WHERE DATA WAS WRITTEN,
	// so drift silently split an artifact across two locations with every job
	// reporting success. Here it decides only an IAM scope: if it drifts, the
	// dispatcher logs "could not publish tenant roster", the heartbeat falls
	// back to its ledger-derived tenants, and nothing is lost or misplaced.
	// Loud and degrading, not silent and wrong.
	const tenantRosterPrefix = "tenants/" // must match layout.TenantRoster.S3Prefix
	stateBucket.GrantWrite(dispatchFn, jsii.String(tenantRosterPrefix+"*"), nil)
	stateBucket.GrantRead(opsNotifyFn, jsii.String(tenantRosterPrefix+"*"))
	dispatchFn.AddEnvironment(jsii.String("STATE_BUCKET"), stateBucket.BucketName(), nil)

	for _, j := range jobs {
		r := awsevents.NewRule(stack, jsii.String(j.id+"Rule"), &awsevents.RuleProps{
			Schedule: awsevents.Schedule_Expression(jsii.String(j.cron)),
			Enabled:  jsii.Bool(schedulesEnabled),
		})
		if perTenantJobs[j.id] {
			// The command is passed as DATA, unchanged. It must stay
			// byte-identical to the JOB_SCHEDULES entry built below, because
			// entrypoint.sh records the run as CMD="$*" and internal/opsalert
			// looks the schedule up by that exact string.
			r.AddTarget(awseventstargets.NewLambdaFunction(dispatchFn, &awseventstargets.LambdaFunctionProps{
				Event: awsevents.RuleTargetInput_FromObject(&map[string]interface{}{
					"command": *j.cmd,
				}),
			}))
			continue
		}
		r.AddTarget(awseventstargets.NewEcsTask(&awseventstargets.EcsTaskProps{
			Cluster:         cluster,
			TaskDefinition:  taskDef,
			AssignPublicIp:  jsii.Bool(true),
			SubnetSelection: &awsec2.SubnetSelection{SubnetType: awsec2.SubnetType_PUBLIC},
			ContainerOverrides: &[]*awseventstargets.ContainerOverride{{
				ContainerName: jsii.String("bot"),
				Command:       j.cmd,
			}},
		}))
	}

	// --- Heartbeat: alert on a job that never launched (rosterbot-ys8) ---
	//
	// TaskFailRule above is reactive — a task has to stop for it to hear
	// anything — so a disabled rule, a broken cron or an unreachable cluster
	// emits no event, writes no ledger record, and produces perfect silence,
	// which is indistinguishable from perfect health. This is the scheduled
	// counterweight: it asserts on a timer that every job in the table above has
	// a run recent enough to be alive.
	//
	// The command string is what joins the three sides: EventBridge passes j.cmd
	// as the container override, entrypoint.sh joins argv with spaces into
	// `run-ledger --command`, and opsalert matches on that exact string. One
	// table, three consumers, no restatement.
	type jobSchedule struct {
		Command       string `json:"command"`
		MaxGapSeconds int    `json:"max_gap_seconds"`
		// PerTenant comes from the SAME perTenantJobs set that decides which
		// rules target the dispatcher, so the notifier cannot disagree with the
		// wiring about which jobs fan out. It matters because the active tenant
		// roster applies only to per-tenant jobs: applying it to a league-wide
		// one would assert that job for every tenant, and since no tenant runs
		// it under their own id, report a healthy job as permanently dark.
		PerTenant bool `json:"per_tenant,omitempty"`
	}
	scheds := make([]jobSchedule, 0, len(jobs))
	for _, j := range jobs {
		parts := make([]string, 0, len(*j.cmd))
		for _, s := range *j.cmd {
			parts = append(parts, *s)
		}
		scheds = append(scheds, jobSchedule{
			Command:       strings.Join(parts, " "),
			MaxGapSeconds: int(j.maxGap.Seconds()),
			PerTenant:     perTenantJobs[j.id],
		})
	}
	schedJSON, err := json.Marshal(scheds)
	if err != nil {
		panic(err) // a literal table cannot fail to marshal; reaching here is a code bug
	}
	opsNotifyFn.AddEnvironment(jsii.String("JOB_SCHEDULES"), jsii.String(string(schedJSON)), nil)

	// Every 6 hours at :15, off the top of the hour the jobs themselves crowd.
	// This cadence bounds detection *latency* only — the per-job tolerances
	// decide what counts as overdue and the notifier alerts once per outage — so
	// a tighter schedule would buy a few hours at the cost of re-reading the
	// ledger, and a looser one risks a whole day passing unnoticed.
	//
	// Gated with the other schedules: `-c schedulesEnabled=false` is an explicit
	// operator pause, and a heartbeat that pages about jobs someone deliberately
	// stopped is exactly the alert that teaches people to ignore the channel.
	awsevents.NewRule(stack, jsii.String("HeartbeatRule"), &awsevents.RuleProps{
		Schedule: awsevents.Schedule_Expression(jsii.String("cron(15 */6 * * ? *)")),
		Enabled:  jsii.Bool(schedulesEnabled),
		Targets: &[]awsevents.IRuleTarget{
			awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{
				// A scheduled rule carries no detail-type of its own, so the
				// dispatcher's envelope switch needs one supplied. The
				// "Rosterbot" prefix marks it as ours beside the two real AWS
				// sources; it must match heartbeatDetailType in opsnotify/.
				Event: awsevents.RuleTargetInput_FromObject(&map[string]interface{}{
					"detail-type": "Rosterbot Heartbeat",
					"source":      "rosterbot.ops",
					"detail":      map[string]interface{}{},
				}),
			}),
		},
	})

	// --- Build drift: alert on a merge that never reached production ---
	//
	// BuildNotifyRule is reactive, and so is TaskFailRule: something has to STOP
	// for either to fire. A merge whose build is rejected at the webhook never
	// creates a build record, so it produces no state change, no event and no
	// outcome — there is nothing to be quiet or loud about. That dropped two
	// deploys in two days (rosterbot-7p1i), and the only evidence anywhere in
	// the system was an HTTP 400 in a GitHub webhook delivery log nobody reads.
	//
	// The heartbeat above cannot cover it either, despite being the scheduled
	// counterweight: it asks whether each job LAUNCHED, and a job launching on a
	// stale image answers yes. The run ledger agrees, and opsalert reports the
	// deployment healthy. So this asks the outcome question directly — is main's
	// HEAD the commit that is actually deployed? — which catches a dropped build
	// whatever its cause, including causes nobody predicted. The builds it was
	// written for were dropped by a cause everybody believed was already fixed.
	if buildProject != nil {
		opsNotifyFn.AddEnvironment(jsii.String("GITHUB_REPO"), jsii.String(ghOwner+"/"+ghRepo), nil)
		opsNotifyFn.AddEnvironment(jsii.String("BUILD_PROJECT"), buildProject.ProjectName(), nil)
		// Read build metadata for this project only. The check never starts a
		// build, and never needs to: it compares what already happened.
		opsNotifyFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
			Actions:   jsii.Strings("codebuild:ListBuildsForProject", "codebuild:BatchGetBuilds"),
			Resources: &[]*string{buildProject.ProjectArn()},
		}))
		// Every 2 hours at :35 — clear of :15 where the heartbeat sits and of the
		// top of the hour the jobs themselves crowd. The cadence bounds only how
		// long a dropped deploy can run stale before it is reported; the alert
		// deduplicates on the last successfully built commit, so a tighter
		// schedule would cost invocations, not noise.
		//
		// Gated with the other schedules for the same reason they are: a drift
		// alert about a deployment someone deliberately paused is exactly the
		// alert that teaches an operator to ignore the channel.
		awsevents.NewRule(stack, jsii.String("BuildDriftRule"), &awsevents.RuleProps{
			Schedule: awsevents.Schedule_Expression(jsii.String("cron(35 */2 * * ? *)")),
			Enabled:  jsii.Bool(schedulesEnabled),
			Targets: &[]awsevents.IRuleTarget{
				awseventstargets.NewLambdaFunction(opsNotifyFn, &awseventstargets.LambdaFunctionProps{
					// Must match driftDetailType in opsnotify/.
					Event: awsevents.RuleTargetInput_FromObject(&map[string]interface{}{
						"detail-type": "Rosterbot Build Drift",
						"source":      "rosterbot.ops",
						"detail":      map[string]interface{}{},
					}),
				}),
			},
		})
	}

	return stack
}

func main() {
	defer jsii.Close()

	app := awscdk.NewApp(nil)

	// The rosterbot.dev certificate, in us-east-1 because CloudFront reads
	// viewer certs from nowhere else (see infra/domain.go). Both return values
	// are dropped on purpose: this slice creates the certificate and stops
	// there, so InfraStack still holds no reference to it and its diff stays
	// empty. The capture — and CrossRegionReferences on InfraStack, which is
	// only required once a reference actually crosses — arrives with the first
	// alias domain.
	_, siteCert := NewCertStack(app, "InfraCertStack", &CertStackProps{
		awscdk.StackProps{
			Env:                   certEnv(),
			CrossRegionReferences: jsii.Bool(true),
		},
	})

	// CrossRegionReferences is required on BOTH stacks now that the cert ARN
	// actually crosses: CDK carries it through generated SSM parameters plus a
	// reader custom resource. Note the one-way cost — CloudFormation refuses to
	// delete an export still in use, so unwinding this reference later is a
	// staged weakening across several deploys, not a plain revert.
	NewInfraStack(app, "InfraStack", &InfraStackProps{
		StackProps: awscdk.StackProps{
			Env:                   env(),
			CrossRegionReferences: jsii.Bool(true),
		},
		Certificate: siteCert,
	})

	app.Synth(nil)
}

// env determines the AWS environment (account+region) in which our stack is to
// be deployed. For more information see: https://docs.aws.amazon.com/cdk/latest/guide/environments.html
func env() *awscdk.Environment {
	// Pinned to the rosterbot account/region. Concrete env is required so that
	// Vpc_FromLookup (Phase 3) can resolve the default VPC's subnets.
	return &awscdk.Environment{
		Account: jsii.String("476646938644"),
		Region:  jsii.String("us-west-1"),
	}
}

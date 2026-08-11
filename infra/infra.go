package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsathena"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfrontorigins"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscodebuild"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsec2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecr"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsevents"
	"github.com/aws/aws-cdk-go/awscdk/v2/awseventstargets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsglue"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsssm"
	"github.com/aws/aws-cdk-go/awscdklambdagoalpha/v2"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

type InfraStackProps struct {
	awscdk.StackProps
}

func NewInfraStack(scope constructs.Construct, id string, props *InfraStackProps) awscdk.Stack {
	var sprops awscdk.StackProps
	if props != nil {
		sprops = props.StackProps
	}
	stack := awscdk.NewStack(scope, &id, &sprops)

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

	awscdk.NewCfnOutput(stack, jsii.String("ClusterName"), &awscdk.CfnOutputProps{Value: cluster.ClusterName()})
	awscdk.NewCfnOutput(stack, jsii.String("TaskDefArn"), &awscdk.CfnOutputProps{Value: taskDef.TaskDefinitionArn()})

	// --- Phase 5: CloudFront URLs (distributions are created above, before the
	// container, so their IDs can be injected as env vars for cache invalidation) ---
	awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dist.DistributionDomainName()}),
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

	apiFn := awscdklambdagoalpha.NewGoFunction(stack, jsii.String("LineupApi"), &awscdklambdagoalpha.GoFunctionProps{
		Entry: jsii.String("../lambda"),
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
	// webauthn/ holds the single Identity record and is read-modify-written
	// on every registration and login (sign-counter update).
	stateBucket.GrantReadWrite(apiFn, jsii.String("webauthn/*"))
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
	dashboardDist := awscloudfront.NewDistribution(stack, jsii.String("DashboardCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(dashboardBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
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

	// The bot task (projection-site) publishes report/model.json + report/value.json
	// under DashboardBucket's "report/" prefix, so it needs write access here too
	// (previously it only wrote to its own now-removed ReportBucket), plus
	// permission to invalidate DashboardCdn after publishing.
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
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dashboardDist.DistributionDomainName()}),
	})
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
	awsssm.NewStringParameter(stack, jsii.String("RpIdParam"), &awsssm.StringParameterProps{
		ParameterName: jsii.String("/rosterbot/DASHBOARD_RP_ID"),
		StringValue:   dashboardDist.DistributionDomainName(),
	})
	awsssm.NewStringParameter(stack, jsii.String("RpOriginParam"), &awsssm.StringParameterProps{
		ParameterName: jsii.String("/rosterbot/DASHBOARD_RP_ORIGIN"),
		StringValue:   awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dashboardDist.DistributionDomainName()}),
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

	// --- Phase 2: CodeBuild (build + push image to ECR on push to main) ---
	// Gated: only instantiated with `-c enableBuild=true`, because the GitHub
	// webhook source requires a one-time source credential (GitHub OAuth/PAT) to
	// exist in the account first. Until then the stack deploys without it.
	if v, ok := stack.Node().TryGetContext(jsii.String("enableBuild")).(string); ok && v == "true" {
		project := awscodebuild.NewProject(stack, jsii.String("Build"), &awscodebuild.ProjectProps{
			Source: awscodebuild.Source_GitHub(&awscodebuild.GitHubSourceProps{
				Owner:   jsii.String("nixon-commits"),
				Repo:    jsii.String("rosterbot"),
				Webhook: jsii.Bool(true),
				WebhookFilters: &[]awscodebuild.FilterGroup{
					awscodebuild.FilterGroup_InEventOf(awscodebuild.EventAction_PUSH).
						AndBranchIs(jsii.String("main")),
				},
			}),
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
				Privileged: jsii.Bool(true), // docker build
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
		})
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
			PartitionKeys: &[]interface{}{col("dt", "string"), col("system", "string")},
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
				"projection.system.type":    jsii.String("enum"),
				"projection.system.values":  jsii.String("steamer-ros,depthcharts-ros,thebatx-ros,atc-ros"),
				"storage.location.template": awscdk.Fn_Join(jsii.String(""), &[]*string{gradesLoc, jsii.String("dt=${dt}/system=${system}/")}),
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
		hourlyGap = 13 * time.Hour     // 11h overnight window + 2h slack
		dailyGap  = 26 * time.Hour     // 24h + 2h slack
		weeklyGap = 8 * 24 * time.Hour // 7d + 1d slack
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
		{"ProjectionSite", "cron(0 15 * * ? *)", jsii.Strings("projection-site", "--out", "report"), dailyGap},
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
		// Shadow captures every projection system's lineup projection for the
		// model-comparison report. It runs at 23:40 UTC (~late ET evening, same
		// UTC/ET calendar day so the snapshot's generated_at passes the backtest
		// stale-guard) after the 23:00 Lineup run, so the next day's Grade
		// (13:30 UTC) finds and scores its per-system snapshots.
		{"Shadow", "cron(40 23 * * ? *)", jsii.Strings("shadow"), dailyGap},
	}
	for _, j := range jobs {
		r := awsevents.NewRule(stack, jsii.String(j.id+"Rule"), &awsevents.RuleProps{
			Schedule: awsevents.Schedule_Expression(jsii.String(j.cron)),
			Enabled:  jsii.Bool(schedulesEnabled),
		})
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

	return stack
}

func main() {
	defer jsii.Close()

	app := awscdk.NewApp(nil)

	NewInfraStack(app, "InfraStack", &InfraStackProps{
		awscdk.StackProps{
			Env: env(),
		},
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

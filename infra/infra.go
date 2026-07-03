package main

import (
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
	})

	// Static recap site bucket (private; served via CloudFront in Phase 5).
	siteBucket := awss3.NewBucket(stack, jsii.String("SiteBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
	})

	// Projection-accuracy dashboard bucket (private; served via its own CDN).
	reportBucket := awss3.NewBucket(stack, jsii.String("ReportBucket"), &awss3.BucketProps{
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
	awscdk.NewCfnOutput(stack, jsii.String("ReportBucketName"), &awscdk.CfnOutputProps{Value: reportBucket.BucketName()})

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
	})
	reportDist := awscloudfront.NewDistribution(stack, jsii.String("ReportCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(reportBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
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
	reportBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("ssm:GetParameters", "ssm:GetParameter"),
		Resources: jsii.Strings("arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/*"),
	}))
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("cloudfront:CreateInvalidation"),
		Resources: &[]*string{cfArn(dist), cfArn(reportDist)},
	}))

	secret := func(name string) awsecs.Secret {
		p := awsssm.StringParameter_FromSecureStringParameterAttributes(stack, jsii.String("P"+name),
			&awsssm.SecureStringParameterAttributes{ParameterName: jsii.String("/rosterbot/" + name)})
		return awsecs.Secret_FromSsmParameter(p)
	}

	taskDef.AddContainer(jsii.String("bot"), &awsecs.ContainerDefinitionOptions{
		Image: awsecs.ContainerImage_FromEcrRepository(repo, jsii.String("latest")),
		Logging: awsecs.LogDriver_AwsLogs(&awsecs.AwsLogDriverProps{
			LogGroup:     logGroup,
			StreamPrefix: jsii.String("run"),
		}),
		Environment: &map[string]*string{
			"STATE_BUCKET":       stateBucket.BucketName(),
			"SITE_BUCKET":        siteBucket.BucketName(),
			"REPORT_BUCKET":      reportBucket.BucketName(),
			"SITE_CF_DIST_ID":    dist.DistributionId(),
			"REPORT_CF_DIST_ID":  reportDist.DistributionId(),
			"CLAIMS_CURSOR_PATH": jsii.String(".waivers/last-claims.json"),
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

	awscdk.NewCfnOutput(stack, jsii.String("ClusterName"), &awscdk.CfnOutputProps{Value: cluster.ClusterName()})
	awscdk.NewCfnOutput(stack, jsii.String("TaskDefArn"), &awscdk.CfnOutputProps{Value: taskDef.TaskDefinitionArn()})

	// --- Phase 5: CloudFront URLs (distributions are created above, before the
	// container, so their IDs can be injected as env vars for cache invalidation) ---
	awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dist.DistributionDomainName()}),
	})
	awscdk.NewCfnOutput(stack, jsii.String("ReportUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), reportDist.DistributionDomainName()}),
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
			"STATE_BUCKET":    stateBucket.BucketName(),
			"API_TOKEN_PARAM": jsii.String("/rosterbot/ROSTERBOT_API_TOKEN"),
			"CLUSTER":         cluster.ClusterArn(),
			"TASK_DEF":        taskDef.TaskDefinitionArn(),
			"SUBNETS":         awscdk.Fn_Join(jsii.String(","), publicSubnets.SubnetIds),
			"SECURITY_GROUPS": taskSg.SecurityGroupId(),
			"CONTAINER_NAME":  jsii.String("bot"),
		},
	})
	// Least privilege: read lineup/ + the run ledger/output objects + the one
	// token param. runledger/ is the ledger (rosterbot-432); runs/ is still
	// read for per-run captured output blobs (runs/<id>/output.json).
	stateBucket.GrantRead(apiFn, jsii.String("lineup/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runledger/*"))
	stateBucket.GrantRead(apiFn, jsii.String("runs/*"))
	stateBucket.GrantRead(apiFn, jsii.String("notifications/*"))
	apiFn.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("ssm:GetParameter"),
		Resources: jsii.Strings("arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/ROSTERBOT_API_TOKEN"),
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
				BuildImage: awscodebuild.LinuxArmBuildImage_AMAZON_LINUX_2_STANDARD_3_0(),
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
	type job struct {
		id, cron string
		cmd      *[]*string
	}
	jobs := []job{
		{"Lineup", "cron(0 14-23,0-3 * * ? *)", jsii.Strings("optimize", "--matchup", "--archive-projections")},
		{"Prospects", "cron(0 11 * * ? *)", jsii.Strings("prospects")},
		{"GsCheck", "cron(0 12 * * ? *)", jsii.Strings("gs-check")},
		{"Waivers", "cron(0 13 * * ? *)", jsii.Strings("waivers")},
		{"Transactions", "cron(0 14 * * ? *)", jsii.Strings("transactions")},
		{"Claims", "cron(20 14 * * ? *)", jsii.Strings("claims")},
		{"Recap", "cron(0 11 ? * MON *)", jsii.Strings("recap-site", "--out", "dist")},
		{"Backtest", "cron(0 12 ? * MON *)", jsii.Strings("backtest")},
		{"Grade", "cron(30 13 * * ? *)", jsii.Strings("grade")},
		{"ProjectionSite", "cron(0 15 * * ? *)", jsii.Strings("projection-site", "--out", "report")},
		// daily capture of ephemeral upstream data (HKB, projections, Savant, prospects) after upstreams' once-daily refresh
		{"Archive", "cron(15 14 * * ? *)", jsii.Strings("archive")},
		// Shadow captures every projection system's lineup projection for the
		// model-comparison report. It runs at 23:40 UTC (~late ET evening, same
		// UTC/ET calendar day so the snapshot's generated_at passes the backtest
		// stale-guard) after the 23:00 Lineup run, so the next day's Grade
		// (13:30 UTC) finds and scores its per-system snapshots.
		{"Shadow", "cron(40 23 * * ? *)", jsii.Strings("shadow")},
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

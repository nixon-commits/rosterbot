package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfrontorigins"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscodebuild"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsec2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecr"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsevents"
	"github.com/aws/aws-cdk-go/awscdk/v2/awseventstargets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsssm"
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

	// Shared log group for all task runs.
	logGroup := awslogs.NewLogGroup(stack, jsii.String("Logs"), &awslogs.LogGroupProps{
		Retention:     awslogs.RetentionDays_ONE_MONTH,
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})

	awscdk.NewCfnOutput(stack, jsii.String("RepoUri"), &awscdk.CfnOutputProps{Value: repo.RepositoryUri()})
	awscdk.NewCfnOutput(stack, jsii.String("StateBucketName"), &awscdk.CfnOutputProps{Value: stateBucket.BucketName()})
	awscdk.NewCfnOutput(stack, jsii.String("SiteBucketName"), &awscdk.CfnOutputProps{Value: siteBucket.BucketName()})

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

	// Task role: read/write its S3 prefixes and read the rosterbot SSM secrets.
	stateBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	siteBucket.GrantReadWrite(taskDef.TaskRole(), nil)
	taskDef.TaskRole().AddToPrincipalPolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions:   jsii.Strings("ssm:GetParameters", "ssm:GetParameter"),
		Resources: jsii.Strings("arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/*"),
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

	// --- Phase 5: CloudFront in front of the recap site bucket (HTTPS + CDN) ---
	dist := awscloudfront.NewDistribution(stack, jsii.String("SiteCdn"), &awscloudfront.DistributionProps{
		DefaultRootObject: jsii.String("index.html"),
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			Origin:               awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(siteBucket, nil),
			ViewerProtocolPolicy: awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
	})
	awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
		Value: awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), dist.DistributionDomainName()}),
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
				BuildImage: awscodebuild.LinuxArmBuildImage_AMAZON_LINUX_2_STANDARD_3_0(),
				Privileged: jsii.Bool(true), // docker build
			},
			EnvironmentVariables: &map[string]*awscodebuild.BuildEnvironmentVariable{
				"ECR_URI": {Value: repo.RepositoryUri()},
			},
		})
		repo.GrantPullPush(project)
		awscdk.NewCfnOutput(stack, jsii.String("BuildProject"), &awscdk.CfnOutputProps{Value: project.ProjectName()})
	}

	// --- Phase 4: schedules (1:1 port of the 8 GHA workflows) ---
	// All crons are UTC (EventBridge rules are UTC-only). claims is offset +20m
	// from transactions so their shared cache/ write-back doesn't race.
	//
	// Rules are DISABLED by default so AWS doesn't double-fire alongside the
	// still-live GitHub Actions. At cutover (Phase 6) deploy with
	// `-c schedulesEnabled=true` the same moment the GHA workflows are retired.
	schedulesEnabled := false
	if v, ok := stack.Node().TryGetContext(jsii.String("schedulesEnabled")).(string); ok && v == "true" {
		schedulesEnabled = true
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

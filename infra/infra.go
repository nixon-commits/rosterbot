package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsec2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecr"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsecs"
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

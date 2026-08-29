package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudtrail"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatch"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

// The CIS AWS Foundations Benchmark's fourteen "log metric filter and alarm"
// controls (CloudWatch.1 .. CloudWatch.14), built directly rather than through
// Security Hub.
//
// Security Hub is deliberately disabled on this account (2026-08-29). Its
// controls are AWS Config rules, and Config bills per configuration item; a
// Fargate task in awsvpc mode allocates a per-task ENI, so the ~930 scheduled
// tasks/month would have recorded thousands of configuration items and cost
// more than the entire account's ~$7/mo run rate. These fourteen controls were
// the only findings Security Hub ever actually produced, because they read
// CloudTrail directly and need no Config recorder. So they are worth keeping,
// and they are cheap to keep: one multi-region trail (management events are
// free for the first copy), one log group, and $0.10/alarm/month.
//
// Everything here is in the stack's own region. A multi-region trail captures
// every other region's management events and delivers them to this one log
// group, so a single set of filters covers the account.

const (
	// cisMetricNamespace keeps these metrics out of any AWS/* namespace so they
	// are obviously ours and cannot collide with a service metric.
	cisMetricNamespace = "Rosterbot/CISBenchmark"

	// cisAlarmPrefix is load-bearing, not cosmetic. The EventBridge rule that
	// routes alarms to Pushover matches on explicit alarm NAMES, so the names
	// have to be stable and predictable rather than CDK-generated. It also
	// means a human reading a Pushover alert can tell instantly which control
	// fired without opening the console.
	cisAlarmPrefix = "rosterbot-cis-"
)

// cisAlarm is one CIS control: a CloudWatch Logs metric filter over the
// CloudTrail log group, plus an alarm that fires when the filter matches at
// all. Threshold is always "more than zero occurrences in five minutes" — these
// are anomaly detectors, not rate limiters.
type cisAlarm struct {
	control string // CIS/Security Hub control id, e.g. "CloudWatch.1"
	id      string // CDK construct id (stable; renaming replaces the resource)
	metric  string // CloudWatch metric name
	desc    string // what fired, in words, for the alarm description and the push
	pattern string // CloudWatch Logs filter pattern (CIS-standard text)
}

// alarmName is derived, never stored, so the name and the EventBridge pattern
// cannot drift apart.
func (a cisAlarm) alarmName() string { return cisAlarmPrefix + a.control }

// pages decides whether this control's alarm wakes the operator on Pushover, or
// merely records a breach in CloudWatch for later inspection.
//
// THE PROBLEM THIS SOLVES: nine of the fourteen controls fire on this repo's
// OWN CodeBuild deploys. Every `cdk deploy` that touches a role, a security
// group, a bucket policy or the VPC trips CloudWatch.4/8/10/11/12/13/14 (and
// this stack's first deploy trips CloudWatch.5, which watches CloudTrail
// itself). At the measured deploy rate — ~68 deploys in six days — routing all
// fourteen to Pushover would reproduce the stale-cache flood documented in
// CLAUDE.md: "30 pushes in 24 hours, of which the 30th said nothing the 1st had
// not." An alert nobody can afford to read is worth less than no alert.
//
// The alarm still EXISTS either way. A non-paging control keeps its metric
// filter and its alarm history, so the record is complete and queryable; what
// it does not do is interrupt anyone. That is the same split the repo already
// makes between an unconditional console line and a suppressed Pushover.
//
// The five below are the ones no deploy can cause. Each describes something
// that should not happen on a single-admin account whose every change arrives
// through CodeBuild, so a push here is always worth reading:
//
//   - CloudWatch.1  the root user was used at all
//   - CloudWatch.2  an API call was denied — the signal of someone probing
//   - CloudWatch.3  a console sign-in bypassed MFA
//   - CloudWatch.6  a console login failed — brute force, or a stolen password
//   - CloudWatch.7  a customer CMK was disabled or scheduled for deletion
//
// CloudWatch.7 earns its place on blast radius rather than likelihood: the one
// customer CMK on this account is the envelope key for per-tenant Fantrax
// credentials, and a scheduled deletion is both silent and on a timer, so the
// window in which it can still be cancelled is exactly the window in which
// nobody would otherwise look.
//
// Adding a deploy-driven control here is the one change that would undo this
// file's whole point; TestPagingSet_ExcludesEveryDeployDrivenControl pins it.
func (a cisAlarm) pages() bool {
	switch a.control {
	case "CloudWatch.1", "CloudWatch.2", "CloudWatch.3", "CloudWatch.6", "CloudWatch.7":
		return true
	default:
		return false
	}
}

// cisAlarmSpecs is the fourteen controls and their CIS-standard filter
// patterns. The patterns are transcribed from the benchmark rather than
// invented; they are the same text Security Hub's own CloudWatch.N controls
// check for, which is what makes an alarm built here satisfy the control.
func cisAlarmSpecs() []cisAlarm {
	return []cisAlarm{
		{"CloudWatch.1", "CisRootUsage", "RootAccountUsage",
			"root user was used",
			`{$.userIdentity.type="Root" && $.userIdentity.invokedBy NOT EXISTS && $.eventType!="AwsServiceEvent"}`},
		{"CloudWatch.2", "CisUnauthorizedApi", "UnauthorizedApiCalls",
			"unauthorized API call(s) denied",
			`{($.errorCode="*UnauthorizedOperation") || ($.errorCode="AccessDenied*")}`},
		{"CloudWatch.3", "CisConsoleNoMfa", "ConsoleSignInWithoutMfa",
			"console sign-in without MFA",
			`{$.eventName="ConsoleLogin" && $.additionalEventData.MFAUsed!="Yes"}`},
		{"CloudWatch.4", "CisIamPolicyChange", "IamPolicyChanges",
			"IAM policy changed",
			`{($.eventName=DeleteGroupPolicy)||($.eventName=DeleteRolePolicy)||($.eventName=DeleteUserPolicy)||($.eventName=PutGroupPolicy)||($.eventName=PutRolePolicy)||($.eventName=PutUserPolicy)||($.eventName=CreatePolicy)||($.eventName=DeletePolicy)||($.eventName=CreatePolicyVersion)||($.eventName=DeletePolicyVersion)||($.eventName=AttachRolePolicy)||($.eventName=DetachRolePolicy)||($.eventName=AttachUserPolicy)||($.eventName=DetachUserPolicy)||($.eventName=AttachGroupPolicy)||($.eventName=DetachGroupPolicy)}`},
		{"CloudWatch.5", "CisCloudTrailChange", "CloudTrailConfigChanges",
			"CloudTrail configuration changed",
			`{($.eventName=CreateTrail)||($.eventName=UpdateTrail)||($.eventName=DeleteTrail)||($.eventName=StartLogging)||($.eventName=StopLogging)}`},
		{"CloudWatch.6", "CisConsoleAuthFail", "ConsoleAuthFailures",
			"console authentication failure",
			`{($.eventName=ConsoleLogin) && ($.errorMessage="Failed authentication")}`},
		{"CloudWatch.7", "CisCmkDelete", "CmkDisableOrDelete",
			"customer CMK disabled or scheduled for deletion",
			`{($.eventSource=kms.amazonaws.com) && (($.eventName=DisableKey)||($.eventName=ScheduleKeyDeletion))}`},
		{"CloudWatch.8", "CisS3PolicyChange", "S3BucketPolicyChanges",
			"S3 bucket policy changed",
			`{($.eventSource=s3.amazonaws.com) && (($.eventName=PutBucketAcl)||($.eventName=PutBucketPolicy)||($.eventName=PutBucketCors)||($.eventName=PutBucketLifecycle)||($.eventName=PutBucketReplication)||($.eventName=DeleteBucketPolicy)||($.eventName=DeleteBucketCors)||($.eventName=DeleteBucketLifecycle)||($.eventName=DeleteBucketReplication))}`},
		{"CloudWatch.9", "CisConfigChange", "AwsConfigChanges",
			"AWS Config configuration changed",
			`{($.eventSource=config.amazonaws.com) && (($.eventName=StopConfigurationRecorder)||($.eventName=DeleteDeliveryChannel)||($.eventName=PutDeliveryChannel)||($.eventName=PutConfigurationRecorder))}`},
		{"CloudWatch.10", "CisSecGroupChange", "SecurityGroupChanges",
			"security group changed",
			`{($.eventName=AuthorizeSecurityGroupIngress)||($.eventName=AuthorizeSecurityGroupEgress)||($.eventName=RevokeSecurityGroupIngress)||($.eventName=RevokeSecurityGroupEgress)||($.eventName=CreateSecurityGroup)||($.eventName=DeleteSecurityGroup)}`},
		{"CloudWatch.11", "CisNaclChange", "NaclChanges",
			"network ACL changed",
			`{($.eventName=CreateNetworkAcl)||($.eventName=CreateNetworkAclEntry)||($.eventName=DeleteNetworkAcl)||($.eventName=DeleteNetworkAclEntry)||($.eventName=ReplaceNetworkAclEntry)||($.eventName=ReplaceNetworkAclAssociation)}`},
		{"CloudWatch.12", "CisGatewayChange", "NetworkGatewayChanges",
			"network gateway changed",
			`{($.eventName=CreateCustomerGateway)||($.eventName=DeleteCustomerGateway)||($.eventName=AttachInternetGateway)||($.eventName=CreateInternetGateway)||($.eventName=DeleteInternetGateway)||($.eventName=DetachInternetGateway)}`},
		{"CloudWatch.13", "CisRouteTableChange", "RouteTableChanges",
			"route table changed",
			`{($.eventName=CreateRoute)||($.eventName=CreateRouteTable)||($.eventName=ReplaceRoute)||($.eventName=ReplaceRouteTableAssociation)||($.eventName=DeleteRouteTable)||($.eventName=DeleteRoute)||($.eventName=DisassociateRouteTable)}`},
		{"CloudWatch.14", "CisVpcChange", "VpcChanges",
			"VPC changed",
			`{($.eventName=CreateVpc)||($.eventName=DeleteVpc)||($.eventName=ModifyVpcAttribute)||($.eventName=AcceptVpcPeeringConnection)||($.eventName=CreateVpcPeeringConnection)||($.eventName=DeleteVpcPeeringConnection)||($.eventName=RejectVpcPeeringConnection)||($.eventName=AttachClassicLinkVpc)||($.eventName=DetachClassicLinkVpc)||($.eventName=DisableVpcClassicLink)||($.eventName=EnableVpcClassicLink)}`},
	}
}

// addCISTrailAlarms creates the trail, its log group, and the fourteen metric
// filters and alarms. It returns the names of the alarms that should page, for
// the caller to wire into an EventBridge rule — this function creates no rule
// itself, because the notification target (the OpsNotify function) lives in
// infra.go and threading it in here would couple the two files in the wrong
// direction.
//
// Returning names rather than alarm objects is deliberate: the EventBridge
// pattern matches on the alarmName string, so names are what the caller needs,
// and handing back constructs would invite someone to add a second alarm action
// and split notification across two mechanisms.
func addCISTrailAlarms(scope constructs.Construct) []string {
	// Trail log storage. Ninety days matches the recap access logs beside it,
	// and is well past the window in which anyone investigates an alarm. The
	// trail is the only writer.
	trailBucket := awss3.NewBucket(scope, jsii.String("CloudTrailBucket"), &awss3.BucketProps{
		RemovalPolicy:     awscdk.RemovalPolicy_DESTROY,
		AutoDeleteObjects: jsii.Bool(true),
		Encryption:        awss3.BucketEncryption_S3_MANAGED,
		BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(),
		LifecycleRules: &[]*awss3.LifecycleRule{{
			Id:         jsii.String("ExpireCloudTrailLogs"),
			Expiration: awscdk.Duration_Days(jsii.Number(90)),
		}},
	})

	// The metric filters read from here, not from S3. Retention is ONE_MONTH
	// because the filters evaluate in near-real-time and nothing reads the group
	// historically — the S3 copy above is the archive, at a tenth the price.
	// CloudWatch Logs ingestion is the main running cost of this whole file, so
	// the trail below records MANAGEMENT events only; S3 data events on this
	// account run to ~2.4M requests/month and would dominate the bill.
	trailLogs := awslogs.NewLogGroup(scope, jsii.String("CloudTrailLogs"), &awslogs.LogGroupProps{
		Retention:     awslogs.RetentionDays_ONE_MONTH,
		RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
	})

	awscloudtrail.NewTrail(scope, jsii.String("CisTrail"), &awscloudtrail.TrailProps{
		Bucket:                     trailBucket,
		SendToCloudWatchLogs:       jsii.Bool(true),
		CloudWatchLogGroup:         trailLogs,
		IsMultiRegionTrail:         jsii.Bool(true),
		IncludeGlobalServiceEvents: jsii.Bool(true),
		// Management events only. ReadWriteType_ALL still means management
		// events exclusively — data events are a separate opt-in this trail
		// deliberately does not take. WRITE_ONLY would be cheaper still, but
		// CloudWatch.2 (unauthorized API calls) has to see denied reads.
		ManagementEvents: awscloudtrail.ReadWriteType_ALL,
	})

	var paging []string
	for _, spec := range cisAlarmSpecs() {
		mf := trailLogs.AddMetricFilter(jsii.String(spec.id+"Filter"), &awslogs.MetricFilterOptions{
			FilterPattern:   awslogs.FilterPattern_Literal(jsii.String(spec.pattern)),
			MetricNamespace: jsii.String(cisMetricNamespace),
			MetricName:      jsii.String(spec.metric),
			MetricValue:     jsii.String("1"),
			DefaultValue:    jsii.Number(0),
		})

		// Period is five minutes with Statistic Sum: the question is "did this
		// happen at all in the window", so any non-zero sum is a breach.
		// TreatMissingData_NOT_BREACHING is required, not cosmetic — these
		// metrics only publish when the filter matches, so a healthy account
		// emits no datapoints at all and the default (MISSING) would leave every
		// alarm stuck in INSUFFICIENT_DATA forever.
		alarm := mf.Metric(&awscloudwatch.MetricOptions{
			Statistic: jsii.String("Sum"),
			Period:    awscdk.Duration_Minutes(jsii.Number(5)),
		}).CreateAlarm(scope, jsii.String(spec.id+"Alarm"), &awscloudwatch.CreateAlarmOptions{
			AlarmName:          jsii.String(spec.alarmName()),
			AlarmDescription:   jsii.String("CIS " + spec.control + ": " + spec.desc),
			Threshold:          jsii.Number(0),
			EvaluationPeriods:  jsii.Number(1),
			ComparisonOperator: awscloudwatch.ComparisonOperator_GREATER_THAN_THRESHOLD,
			TreatMissingData:   awscloudwatch.TreatMissingData_NOT_BREACHING,
		})
		_ = alarm

		if spec.pages() {
			paging = append(paging, spec.alarmName())
		}
	}
	return paging
}

package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscertificatemanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

// The rosterbot.dev names both web surfaces are served under, and the zone that
// answers for them.
//
// The zone was created by the Route 53 registrar at registration (2026-08-17)
// with matching NS delegation, so it is IMPORTED here and never created — see
// importZone. The hostnames are constants rather than context or env lookups
// because they are part of the deployment's identity: the dashboard one is
// about to become the WebAuthn RP ID (rosterbot-jloe.4), and an RP ID that can
// vary per synth is an RP ID that can silently invalidate every passkey.
const (
	zoneName     = "rosterbot.dev"
	hostedZoneID = "Z07503721VYCRE63MNQ5V"

	dashHost  = "dash." + zoneName
	recapHost = "recaps." + zoneName

	// apexHost is the bare domain, and from rosterbot-jloe.6 onward it is the
	// WebAuthn RP ID. It is spelled as its own constant rather than reusing
	// zoneName at each call site because the two names mean different things:
	// zoneName identifies the hosted zone that answers for the domain, while
	// apexHost identifies a web surface that is served and that passkeys are
	// bound to. They are the same string today and would not have to stay that
	// way, and an RP ID is the last value in this file that should change as a
	// side effect of someone editing DNS.
	apexHost = zoneName
)

// importZone attaches the registrar-created hosted zone to a stack.
//
// FromHostedZoneAttributes, not FromLookup: an attribute import is pure
// synth-time data, so it needs no account credentials, writes nothing to
// cdk.context.json, and cannot drift between a local synth and CI. A lookup
// would cache the answer into context and make synth depend on who ran it.
//
// It emits no resource. That matters more than it looks: a stack that CREATED a
// zone for rosterbot.dev would get a second zone with its own nameservers, which
// the registrar does not delegate to — DNS for the domain would keep resolving
// through the original zone while every record this stack wrote landed in the
// ignored one. TestCertStack_ImportsTheZoneRatherThanCreatingOne pins the count
// at zero.
func importZone(scope constructs.Construct, id string) awsroute53.IHostedZone {
	return awsroute53.HostedZone_FromHostedZoneAttributes(scope, jsii.String(id), &awsroute53.HostedZoneAttributes{
		HostedZoneId: jsii.String(hostedZoneID),
		ZoneName:     jsii.String(zoneName),
	})
}

// certEnv pins the certificate stack to us-east-1 in the same account.
//
// CloudFront reads a viewer certificate from us-east-1 and nowhere else, no
// matter where the distribution's own stack lives — and InfraStack lives in
// us-west-1. That single constraint is the entire reason this is a separate
// stack rather than a few more lines in infra.go.
func certEnv() *awscdk.Environment {
	return &awscdk.Environment{
		Account: env().Account,
		Region:  jsii.String("us-east-1"),
	}
}

type CertStackProps struct {
	awscdk.StackProps
}

// NewCertStack holds the one resource CloudFront forces out of region, and
// returns it for InfraStack to attach.
//
// One certificate covers both hostnames (dash as the subject, recaps as a SAN)
// rather than one per surface: ACM renews a DNS-validated certificate
// automatically for as long as the validation records stand, so a single cert
// is one renewal path to keep healthy instead of two, and both distributions
// fail or succeed together rather than one quietly expiring.
//
// Callers must set CrossRegionReferences on BOTH this stack and the consuming
// one — CDK carries the ARN across the region boundary through generated SSM
// parameters plus a reader custom resource, and without the flag synth simply
// refuses the reference. Opting in has a cost worth knowing before the first
// deploy: once InfraStack references this cert, removing that reference is not
// a plain revert. CloudFormation refuses to delete an export still in use (the
// "deadly embrace"), so backing it out means weakening the reference across
// successive deploys before the final removal.
func NewCertStack(scope constructs.Construct, id string, props *CertStackProps) (awscdk.Stack, awscertificatemanager.ICertificate) {
	var sprops awscdk.StackProps
	if props != nil {
		sprops = props.StackProps
	}
	stack := awscdk.NewStack(scope, &id, &sprops)

	zone := importZone(stack, "Zone")

	// FromDns(zone) writes the HostedZoneId into each DomainValidationOption, so
	// ACM publishes and later cleans up its own validation CNAMEs. Passing no
	// zone still synths and deploys — and then waits on a record no one will
	// ever write, leaving a cert stuck PENDING_VALIDATION behind a green deploy.
	cert := awscertificatemanager.NewCertificate(stack, jsii.String("SiteCert"), &awscertificatemanager.CertificateProps{
		DomainName:              jsii.String(dashHost),
		SubjectAlternativeNames: jsii.Strings(recapHost, apexHost),
		Validation:              awscertificatemanager.CertificateValidation_FromDns(zone),
	})

	awscdk.NewCfnOutput(stack, jsii.String("SiteCertArn"), &awscdk.CfnOutputProps{Value: cert.CertificateArn()})

	return stack, cert
}

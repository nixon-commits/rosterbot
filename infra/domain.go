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

// Apple-issued values for the iCloud+ Custom Email Domain on rosterbot.dev.
//
// They are transcribed verbatim from the setup wizard rather than assembled
// from zoneName. dkimTarget contains the string "rosterbot.dev" and it is
// tempting to build it — but it is an identifier Apple minted on their side,
// not a name this zone controls, and a renamed domain would get a new one from
// Apple rather than a re-interpolated one from us. appleVerifyTxt is likewise
// account-specific and means nothing anywhere else.
const (
	appleVerifyTxt = "apple-domain=FVwanc4YuvGbc7U6"
	spfTxt         = "v=spf1 include:icloud.com ~all"
	dkimHost       = "sig1._domainkey"
	dkimTarget     = "sig1.dkim.rosterbot.dev.at.icloudmailadmin.com."
	dmarcHost      = "_dmarc"
	dmarcTxt       = "v=DMARC1; p=none;"
)

// addMailRecords points rosterbot.dev's mail at iCloud so privacy@rosterbot.dev
// receives it.
//
// This exists because web/dashboard/privacy.html publishes that address as the
// channel for data access and deletion requests, and until these records
// existed the zone had no MX at all — every such request bounced. A privacy
// contact that silently rejects mail is worse than none: it makes the rights
// the policy grants unexercisable while looking, from the outside, like they
// were granted.
//
// Three details here are load-bearing, and two of them fail in ways a reviewer
// would not predict from the diff:
//
//  1. THE TWO APEX TXT VALUES ARE ONE RECORD, NOT TWO. Apple lists the domain
//     verification string and the SPF policy as separate rows, and the obvious
//     transcription is two NewTxtRecord calls at the apex. That does not
//     deploy: DNS permits exactly one RRSet per (name, type), so the second
//     fails with "RRSet of type TXT with DNS name rosterbot.dev. already
//     exists" — and it fails mid-deploy, after the MX record has landed, so
//     the zone is left half-configured. One record with two values is the
//     same DNS result and the only synthesizable spelling of it.
//
//  2. THE VALUES CARRY NO QUOTES. Apple's wizard displays the SPF row already
//     quoted, and CDK quotes it again: TxtRecord runs each value through
//     JSON.stringify (aws-route53/lib/record-set.js, formatTxt), so a value
//     passed in as "v=spf1 ..." is published as ""v=spf1 ..."". That is not a
//     deploy error — it synths, deploys, and resolves green — it is simply an
//     SPF record no receiver can parse, which surfaces weeks later as mail
//     landing in spam with nothing in any log pointing here.
//
//  3. SPF ENDS IN ~all, NOT -all. A domain that sends no mail should publish
//     "v=spf1 -all" and this one did not send any until now. Connecting iCloud
//     changes that premise: the domain now has a legitimate sender, and a hard
//     fail alongside an include: is a rejection policy for anything the include
//     does not cover. Apple specifies the softfail; take theirs.
//
// DMARC is deliberately inert. p=none asserts ownership and makes tightening a
// one-word edit, but it instructs receivers to do nothing, so it is not yet the
// anti-spoofing control the record type implies. Raise it to p=quarantine and
// then p=reject once mail through this domain has been observed working and
// DKIM-signed — tightening before that turns a misconfigured DKIM key into
// silently discarded mail, and the first mail this address is likely to carry
// is somebody's deletion request.
//
// Records live in CDK rather than the Route 53 console for the reason the zone
// import already documents: a console edit is invisible to review and survives
// only until someone wonders what wrote it.
func addMailRecords(scope constructs.Construct, zone awsroute53.IHostedZone) {
	awsroute53.NewMxRecord(scope, jsii.String("MailMx"), &awsroute53.MxRecordProps{
		Zone:       zone,
		RecordName: jsii.String(apexHost),
		Values: &[]*awsroute53.MxRecordValue{
			{HostName: jsii.String("mx01.mail.icloud.com."), Priority: jsii.Number(10)},
			{HostName: jsii.String("mx02.mail.icloud.com."), Priority: jsii.Number(10)},
		},
	})

	// Both apex TXT values, one RRSet — see (1) above.
	awsroute53.NewTxtRecord(scope, jsii.String("MailTxt"), &awsroute53.TxtRecordProps{
		Zone:       zone,
		RecordName: jsii.String(apexHost),
		Values:     jsii.Strings(appleVerifyTxt, spfTxt),
	})

	awsroute53.NewCnameRecord(scope, jsii.String("MailDkim"), &awsroute53.CnameRecordProps{
		Zone:       zone,
		RecordName: jsii.String(dkimHost),
		DomainName: jsii.String(dkimTarget),
	})

	awsroute53.NewTxtRecord(scope, jsii.String("MailDmarc"), &awsroute53.TxtRecordProps{
		Zone:       zone,
		RecordName: jsii.String(dmarcHost),
		Values:     jsii.Strings(dmarcTxt),
	})
}

package main

import (
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/jsii-runtime-go"
)

// The cert must cover both hostnames AND bind its DNS validation to the real
// zone. Those are two different failures. A missing SAN fails loudly the moment
// a distribution tries to attach it. A certificate whose validation names no
// zone (or the wrong one) synths clean, deploys clean, and then sits in
// PENDING_VALIDATION forever, because nothing ever writes the CNAME it waits on
// — a green deploy that never produces a usable cert.
func TestCertStack_CoversBothHostnamesAndValidatesInTheImportedZone(t *testing.T) {
	app := awscdk.NewApp(nil)
	stack, cert := NewCertStack(app, "Cert", &CertStackProps{awscdk.StackProps{
		Env:                   certEnv(),
		CrossRegionReferences: jsii.Bool(true),
	}})
	if cert == nil {
		t.Fatal("NewCertStack returned a nil certificate; nothing downstream could attach it")
	}

	assertions.Template_FromStack(stack, nil).HasResourceProperties(
		jsii.String("AWS::CertificateManager::Certificate"),
		map[string]any{
			"DomainName":              dashHost,
			"SubjectAlternativeNames": []any{recapHost},
			"ValidationMethod":        "DNS",
			"DomainValidationOptions": []any{
				map[string]any{"DomainName": dashHost, "HostedZoneId": hostedZoneID},
				map[string]any{"DomainName": recapHost, "HostedZoneId": hostedZoneID},
			},
		},
	)
}

// CloudFront reads a viewer certificate from us-east-1 and nowhere else,
// regardless of where the rest of the stack lives. A cert stack that drifts to
// InfraStack's us-west-1 synths and deploys clean, then fails at attach time
// with an error naming the distribution rather than the certificate.
func TestCertStack_IsPinnedToUsEast1InTheSameAccount(t *testing.T) {
	if got := *certEnv().Region; got != "us-east-1" {
		t.Errorf("cert stack region = %q, want us-east-1 (CloudFront accepts viewer certs from nowhere else)", got)
	}
	if got, want := *certEnv().Account, *env().Account; got != want {
		t.Errorf("cert stack account = %q, want %q (same account as InfraStack)", got, want)
	}
}

// The cert stack must hold ONLY the certificate. An imported hosted zone is a
// synth-time construct and must not materialise as an AWS::Route53::HostedZone
// — emitting one would attempt to create a SECOND zone for rosterbot.dev, with
// its own nameservers that the registrar does not delegate to, silently
// breaking DNS for the domain.
func TestCertStack_ImportsTheZoneRatherThanCreatingOne(t *testing.T) {
	app := awscdk.NewApp(nil)
	stack, _ := NewCertStack(app, "Cert", &CertStackProps{awscdk.StackProps{
		Env:                   certEnv(),
		CrossRegionReferences: jsii.Bool(true),
	}})
	assertions.Template_FromStack(stack, nil).ResourceCountIs(jsii.String("AWS::Route53::HostedZone"), jsii.Number(0))
}

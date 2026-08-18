package main

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscertificatemanager"
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

// --- InfraStack: alias domains on the two distributions ---

// Synthesising InfraStack bundles three Go Lambdas, so it is done once and
// shared. A failure here is a hard stop for every test below, hence the panic:
// there is no partial result worth reporting.
var (
	infraOnce sync.Once
	infraTpl  assertions.Template
	infraRaw  map[string]any
)

func infraTemplate(t *testing.T) (assertions.Template, map[string]any) {
	t.Helper()
	infraOnce.Do(func() {
		app := awscdk.NewApp(nil)
		// An imported certificate is a literal ARN, so it needs no
		// cross-region machinery — the real cert arrives as a token from
		// InfraCertStack, but nothing asserted here depends on which.
		scope := awscdk.NewStack(app, jsii.String("TestCertScope"), &awscdk.StackProps{Env: certEnv()})
		cert := awscertificatemanager.Certificate_FromCertificateArn(scope, jsii.String("Cert"),
			jsii.String("arn:aws:acm:us-east-1:476646938644:certificate/00000000-0000-0000-0000-000000000000"))

		stack := NewInfraStack(app, "TestStack", &InfraStackProps{
			StackProps:  awscdk.StackProps{Env: env()},
			Certificate: cert,
		})
		infraTpl = assertions.Template_FromStack(stack, nil)
		raw, err := json.Marshal(infraTpl.ToJSON())
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw, &infraRaw); err != nil {
			panic(err)
		}
	})
	return infraTpl, infraRaw
}

// An alias domain is inert without a matching certificate: CloudFront rejects
// the distribution update outright, so these two travel together or not at all.
func TestSiteCdn_ServesTheRecapHostnameUnderTheCert(t *testing.T) {
	tpl, _ := infraTemplate(t)
	tpl.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]any{
		"DistributionConfig": assertions.Match_ObjectLike(&map[string]any{
			"Aliases": []any{recapHost},
			"ViewerCertificate": assertions.Match_ObjectLike(&map[string]any{
				"AcmCertificateArn": assertions.Match_AnyValue(),
				"SslSupportMethod":  "sni-only",
			}),
		}),
	})
}

// Both address families, deliberately. CloudFront serves dual-stack, and an
// A-only record leaves IPv6-only clients resolving nothing — a failure that is
// invisible from any dual-stack machine, which is every machine we would test
// from.
func TestRecapAlias_CoversBothAddressFamilies(t *testing.T) {
	tpl, _ := infraTemplate(t)
	for _, typ := range []string{"A", "AAAA"} {
		tpl.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
			"Name":         recapHost + ".",
			"Type":         typ,
			"HostedZoneId": hostedZoneID,
			"AliasTarget":  assertions.Match_AnyValue(),
		})
	}
}

// THE TRIPWIRE. Slice 3 attaches dash.rosterbot.dev to the dashboard
// distribution but must NOT touch the WebAuthn RP config; slice 4 is where that
// happens, deliberately and with humans standing by, because it invalidates
// every registered passkey and nothing can migrate them.
//
// The two are one line apart in infra.go and look interchangeable, so this
// asserts the RP parameters still resolve to the CloudFront domain (a GetAtt,
// i.e. a structure) rather than to the literal hostname (a plain string). If
// this test ever fails without rosterbot-jloe.4 being the reason, a passkey
// wipe is about to ship.
func TestRpParams_StillNameTheCloudFrontDomainNotTheAliasHost(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)
	seen := 0
	for logicalID, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::SSM::Parameter" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		name, _ := props["Name"].(string)
		if name != "/rosterbot/DASHBOARD_RP_ID" && name != "/rosterbot/DASHBOARD_RP_ORIGIN" {
			continue
		}
		seen++
		switch v := props["Value"].(type) {
		case string:
			t.Errorf("%s (%s) has literal Value %q — the RP is being cut over to the alias "+
				"hostname. That invalidates EVERY registered passkey. If this is "+
				"rosterbot-jloe.4, update this test deliberately; otherwise revert.",
				name, logicalID, v)
		default:
			_ = v // a GetAtt/Join on the distribution domain: correct.
		}
	}
	if seen != 2 {
		t.Fatalf("found %d of the 2 RP parameters; the tripwire is not watching what it thinks it is", seen)
	}
}

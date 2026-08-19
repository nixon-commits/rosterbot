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
			"SubjectAlternativeNames": []any{recapHost, apexHost},
			"ValidationMethod":        "DNS",
			"DomainValidationOptions": []any{
				map[string]any{"DomainName": dashHost, "HostedZoneId": hostedZoneID},
				map[string]any{"DomainName": recapHost, "HostedZoneId": hostedZoneID},
				map[string]any{"DomainName": apexHost, "HostedZoneId": hostedZoneID},
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

// THE TRIPWIRE, INVERTED (rosterbot-jloe.4).
//
// Before this commit, this test asserted the opposite: that the RP params
// resolved to a GetAtt on the CloudFront domain, NOT to the literal alias
// hostname — because slices 2/3 deliberately shipped the hostname migration
// with zero passkey impact, and the cutover was a separate, deliberate step
// with humans standing by. That step has now happened. This test flipping is
// not drift; it IS the change under review, same as the two literal strings
// in infra.go it is pinned to.
//
// It stays valuable in its new polarity for a reason distinct from the one
// above: the RP config is a literal string specifically so a future
// distribution REPLACEMENT cannot silently rewrite it (see the comment beside
// RpIdParam in infra.go). If a future edit reverts to a GetAtt — even with
// good intentions, e.g. "let's DRY this up" — this test catches it before a
// distribution replacement turns that into an unplanned second passkey wipe.
func TestRpParams_NameTheAliasHostAfterCutover(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)
	want := map[string]string{
		"/rosterbot/DASHBOARD_RP_ID":     apexHost,
		"/rosterbot/DASHBOARD_RP_ORIGIN": "https://" + apexHost + ",https://" + dashHost,
	}
	seen := map[string]bool{}
	for logicalID, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::SSM::Parameter" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		name, _ := props["Name"].(string)
		wantVal, ok := want[name]
		if !ok {
			continue
		}
		seen[name] = true
		gotVal, isStr := props["Value"].(string)
		if !isStr {
			t.Errorf("%s (%s) is not a literal string — got a structured value (GetAtt/Join?). "+
				"The RP ID must be inert to a future distribution replacement; see the comment "+
				"beside RpIdParam in infra.go.", name, logicalID)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("%s (%s) = %q, want %q", name, logicalID, gotVal, wantVal)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("did not find expected SSM parameter %s; the tripwire is not watching what it thinks it is", name)
		}
	}
}

// The dashboard's own hostname. Asserted by its alias rather than by logical
// id, which also proves the two distributions did not get each other's name —
// a swap would serve the SPA on recaps. and the recap site on dash., both with
// a valid cert, so TLS would look perfectly healthy.
func TestDashboardCdn_ServesTheDashHostnameUnderTheCert(t *testing.T) {
	tpl, _ := infraTemplate(t)
	tpl.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]any{
		"DistributionConfig": assertions.Match_ObjectLike(&map[string]any{
			"Aliases": []any{dashHost, apexHost},
			"ViewerCertificate": assertions.Match_ObjectLike(&map[string]any{
				"AcmCertificateArn": assertions.Match_AnyValue(),
				"SslSupportMethod":  "sni-only",
			}),
		}),
	})
}

func TestDashAlias_CoversBothAddressFamilies(t *testing.T) {
	tpl, _ := infraTemplate(t)
	for _, typ := range []string{"A", "AAAA"} {
		tpl.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
			"Name":         dashHost + ".",
			"Type":         typ,
			"HostedZoneId": hostedZoneID,
			"AliasTarget":  assertions.Match_AnyValue(),
		})
	}
}

// Exactly six alias records and no more: recaps, dash and the apex, each in
// both address families. Each rosterbot.dev record this stack writes lands in a
// zone the registrar really delegates to, so a stray one is live DNS for a name
// nobody meant to publish.
func TestAliasRecords_AreExactlyTheSixWeIntend(t *testing.T) {
	tpl, _ := infraTemplate(t)
	tpl.ResourceCountIs(jsii.String("AWS::Route53::RecordSet"), jsii.Number(6))
}

// The apex is a served origin, not a redirect, because an iOS native WebAuthn
// ceremony reports its origin as https://<rpId> — and from rosterbot-jloe.6 the
// RP ID is the apex. A missing record here does not degrade gracefully: the
// dashboard keeps working on dash. and only the iOS surface fails, which is the
// hardest version of this to notice.
func TestApexAlias_CoversBothAddressFamilies(t *testing.T) {
	tpl, _ := infraTemplate(t)
	for _, typ := range []string{"A", "AAAA"} {
		tpl.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
			"Name":         apexHost + ".",
			"Type":         typ,
			"HostedZoneId": hostedZoneID,
			"AliasTarget":  assertions.Match_AnyValue(),
		})
	}
}

// The /invite rewrite must be attached to the DEFAULT behavior only.
//
// This is the whole reason it is a function association rather than the more
// obvious ErrorResponses 403/404 -> /index.html: custom error responses apply
// to the entire distribution, so that version would also rewrite the API's
// genuine 401/403 on /v1/* into index.html with status 200. The iOS client
// reads those 401s to drive its sign-in gate, so it would sign users out into a
// page that renders perfectly. Asserting the association's SCOPE is what keeps
// a future "let's simplify this" from reintroducing that.
func TestInviteRewrite_IsScopedToTheDefaultBehaviorOnly(t *testing.T) {
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)

	var checked int
	for logicalID, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::CloudFront::Distribution" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		cfg, _ := props["DistributionConfig"].(map[string]any)
		aliases, _ := cfg["Aliases"].([]any)
		if len(aliases) == 0 || aliases[0] != dashHost {
			continue // the recap distribution; not this test's subject
		}
		checked++

		def, _ := cfg["DefaultCacheBehavior"].(map[string]any)
		if _, ok := def["FunctionAssociations"]; !ok {
			t.Errorf("%s DefaultCacheBehavior has no FunctionAssociations; /invite will 403 "+
				"and the universal-link fallback page will not render", logicalID)
		}
		for _, b := range asSlice(cfg["CacheBehaviors"]) {
			beh, _ := b.(map[string]any)
			if _, ok := beh["FunctionAssociations"]; ok {
				t.Errorf("%s behavior %v carries a FunctionAssociation; the /invite rewrite must "+
					"not reach /v1/*, whose 401/403 responses the iOS sign-in gate depends on",
					logicalID, beh["PathPattern"])
			}
		}
	}
	if checked != 1 {
		t.Fatalf("matched %d dashboard distributions, want exactly 1 — the test is not "+
			"watching what it thinks it is", checked)
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

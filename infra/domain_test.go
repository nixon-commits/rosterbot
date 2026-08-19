package main

import (
	"encoding/json"
	"strings"
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
//
// This counted AWS::Route53::RecordSet resources outright until the iCloud mail
// records were added, which was a faithful proxy for exactly as long as every
// record in the stack happened to be an alias. It is not any more, and a bare
// count bumped from 6 to 10 would have kept the number passing while silently
// dropping the property the comment above claims — a seventh alias could then
// land as long as a mail record left. Filtering on AliasTarget keeps the
// original assertion; the total below keeps the "no stray records" half, which
// nothing else covers now that the stack writes non-alias records.
func TestAliasRecords_AreExactlyTheSixWeIntend(t *testing.T) {
	_, raw := infraTemplate(t)
	var aliases, total int
	for _, r := range raw["Resources"].(map[string]any) {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::Route53::RecordSet" {
			continue
		}
		total++
		if props, _ := res["Properties"].(map[string]any); props["AliasTarget"] != nil {
			aliases++
		}
	}
	if aliases != 6 {
		t.Errorf("alias records = %d, want 6 (recaps/dash/apex x A/AAAA)", aliases)
	}
	// 6 aliases + 4 mail records (MX, apex TXT, DKIM CNAME, DMARC TXT).
	if total != 10 {
		t.Errorf("Route53 record sets = %d, want 10 — a record this stack writes is live DNS, so adding one should be a deliberate edit here", total)
	}
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

// txtRecordsAt returns every synthesized TXT RRSet at a record name, with its
// raw values. The raw template rather than Template.HasResourceProperties
// because the invariant under test is a COUNT — and HasResourceProperties
// passes as soon as ONE resource matches, which is exactly the state a second,
// conflicting RRSet would produce.
func txtRecordsAt(t *testing.T, name string) [][]string {
	t.Helper()
	_, raw := infraTemplate(t)
	var out [][]string
	for _, r := range raw["Resources"].(map[string]any) {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::Route53::RecordSet" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		if props["Type"] != "TXT" || props["Name"] != name {
			continue
		}
		var vals []string
		for _, v := range asSlice(props["ResourceRecords"]) {
			s, ok := v.(string)
			if !ok {
				t.Fatalf("TXT value at %s is a %T, not a literal — a token here means the record is computed at deploy time, which no DNS policy record should be", name, v)
			}
			vals = append(vals, s)
		}
		out = append(out, vals)
	}
	return out
}

// DNS permits one RRSet per (name, type), so Apple's domain-verification string
// and its SPF policy — two separate rows in the setup wizard — have to
// synthesize as ONE TXT record carrying two values. Transcribing them as two
// NewTxtRecord calls synths clean and dies during deploy with "RRSet of type
// TXT with DNS name rosterbot.dev. already exists", after the MX record has
// already landed, leaving the zone half configured. The count is the assertion;
// a passing HasResourceProperties would miss precisely this.
func TestMailRecords_ApexTxtValuesShareOneRecordSet(t *testing.T) {
	got := txtRecordsAt(t, apexHost+".")
	if len(got) != 1 {
		t.Fatalf("apex TXT RRSets = %d, want exactly 1 — a second one fails the deploy, not the synth; got %v", len(got), got)
	}
	if len(got[0]) != 2 {
		t.Fatalf("apex TXT values = %d, want 2 (Apple domain verification + SPF); got %v", len(got[0]), got[0])
	}
}

// The silent one. Apple's wizard shows the SPF row already wrapped in quotes,
// and CDK's TxtRecord wraps every value again (JSON.stringify, in
// aws-route53/lib/record-set.js formatTxt). Passing the displayed string
// through verbatim publishes ""v=spf1 ..."", which synths green, deploys green
// and resolves green, and is unparseable to every receiver — surfacing weeks
// later as mail landing in spam with nothing pointing back here. Exactly one
// layer of quoting is right; this fails on two.
func TestMailRecords_TxtValuesCarryExactlyOneLayerOfQuoting(t *testing.T) {
	got := txtRecordsAt(t, apexHost+".")
	if len(got) != 1 {
		t.Fatalf("apex TXT RRSets = %d, want 1", len(got))
	}
	// The expectation is SHAPE, not `"` + spfTxt + `"`. Deriving it from the
	// same constant makes the test pass on the very bug it exists to catch:
	// paste Apple's displayed value with its quotes into spfTxt and both the
	// published record and the expected string gain the extra layer together,
	// and the comparison still matches. Assert instead that the constants are
	// quote-free and that CDK added exactly one layer.
	for _, c := range []string{appleVerifyTxt, spfTxt, dmarcTxt} {
		if strings.Contains(c, `"`) {
			t.Errorf("constant %q contains a quote — Apple's wizard displays the SPF row pre-quoted, and CDK quotes every value again", c)
		}
	}
	want := map[string]bool{appleVerifyTxt: false, spfTxt: false}
	for _, v := range got[0] {
		inner, ok := strings.CutPrefix(v, `"`)
		if !ok {
			t.Errorf("apex TXT value %q is not quoted at all", v)
			continue
		}
		inner, ok = strings.CutSuffix(inner, `"`)
		if !ok {
			t.Errorf("apex TXT value %q is not quoted at all", v)
			continue
		}
		if strings.Contains(inner, `"`) {
			t.Errorf("apex TXT value %q carries more than one layer of quoting; receivers cannot parse it", v)
			continue
		}
		if _, known := want[inner]; !known {
			t.Errorf("unexpected apex TXT value %q", v)
			continue
		}
		want[inner] = true
	}
	for v, seen := range want {
		if !seen {
			t.Errorf("missing apex TXT value %q", v)
		}
	}
}

// A hard fail (-all) beside an include: rejects everything the include does not
// cover. This domain sent no mail before iCloud was connected, so -all was
// right then and is a mail-losing policy now. Apple specifies the softfail.
func TestMailRecords_SpfSoftFailsRatherThanHardFails(t *testing.T) {
	if !strings.HasSuffix(spfTxt, " ~all") {
		t.Errorf("SPF = %q, want it to end in ~all — -all beside an include: discards legitimate mail", spfTxt)
	}
	if !strings.Contains(spfTxt, "include:icloud.com") {
		t.Errorf("SPF = %q, want it to include icloud.com, the only sender this domain has", spfTxt)
	}
}

// Both iCloud exchangers, at the equal priority Apple specifies. Publishing one
// of the two makes a single host a point of failure for the address the privacy
// policy names for deletion requests.
func TestMailRecords_MxCoversBothIcloudExchangers(t *testing.T) {
	tpl, _ := infraTemplate(t)
	tpl.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
		"Name":            apexHost + ".",
		"Type":            "MX",
		"HostedZoneId":    hostedZoneID,
		"ResourceRecords": []any{"10 mx01.mail.icloud.com.", "10 mx02.mail.icloud.com."},
	})
}

// DKIM is what makes tightening DMARC past p=none survivable: without an
// aligned signature, raising the policy discards this domain's own outbound
// mail. Publishing the CNAME now is what lets that later edit be one word.
func TestMailRecords_DkimCnameIsPublishedForAlignment(t *testing.T) {
	tpl, _ := infraTemplate(t)
	tpl.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
		"Name":            dkimHost + "." + apexHost + ".",
		"Type":            "CNAME",
		"ResourceRecords": []any{dkimTarget},
	})
}

// p=none instructs receivers to do nothing, so this record asserts ownership
// without yet being the anti-spoofing control its type implies. Pinned so the
// gap is a stated position rather than an oversight — and so raising it is a
// deliberate edit made after outbound mail has been seen working.
func TestMailRecords_DmarcIsDeliberatelyInertForNow(t *testing.T) {
	got := txtRecordsAt(t, dmarcHost+"."+apexHost+".")
	if len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("_dmarc TXT = %v, want exactly one record with one value", got)
	}
	if !strings.Contains(got[0][0], "p=none") {
		t.Errorf("_dmarc = %q; if this was tightened deliberately, update this test and confirm DKIM signs first", got[0][0])
	}
}

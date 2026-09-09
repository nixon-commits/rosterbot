package main

import (
	"encoding/json"
	"slices"
	"sort"
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
	stack := NewCertStack(app, "Cert", &CertStackProps{awscdk.StackProps{Env: certEnv()}})

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
	stack := NewCertStack(app, "Cert", &CertStackProps{awscdk.StackProps{Env: certEnv()}})
	assertions.Template_FromStack(stack, nil).ResourceCountIs(jsii.String("AWS::Route53::HostedZone"), jsii.Number(0))
}

// rosterbot-klbh: CDK's built-in CrossRegionReferences exporter silently
// no-ops on a cert REPLACEMENT (aws/aws-cdk#38059 — its Update handler diffs
// exported SSM parameters by NAME, not value, so a stable export name with a
// changed ARN is filtered out of what it writes). This pins the producer-side
// workaround: NewCertStack must also write the cert ARN into a plain,
// separately-named SSM parameter via a hand-rolled AwsCustomResource, keyed so
// a value-only change is unambiguously an Update.
//
// The Custom::AWS resource's Create/Update payload is an Fn::Join'd JSON
// string spread across literal fragments plus embedded {Ref: ...} tokens, not
// a plain JSON string — asserting on it needs the fragments re-assembled
// (concatenating string fragments and standing tokens in for their {Ref}
// logical id), not a naive `.(string)` type assertion, which does not even
// compile against what CDK actually synthesizes here.
func TestCertStack_PublishesCertArnToPlainSSMParam(t *testing.T) {
	app := awscdk.NewApp(nil)
	stack := NewCertStack(app, "Cert", &CertStackProps{awscdk.StackProps{Env: certEnv()}})
	_, raw := certRawTemplate(t, stack)
	resources, _ := raw["Resources"].(map[string]any)

	certLogicalID := certificateLogicalID(t, resources)

	var (
		matches   int
		lastProps map[string]any
	)
	for _, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "Custom::AWS" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		matches++
		lastProps = props
	}
	if matches != 1 {
		t.Fatalf("found %d Custom::AWS resource(s) in the cert stack, want exactly 1 — "+
			"no Custom::AWS resource publishing the cert ARN to %s found", matches, siteCertArnParamName)
	}

	// OnCreate and OnUpdate must both carry the same call (rosterbot-klbh's
	// integrator decision 4): assert both "Create" and "Update" independently
	// rather than assuming CDK's documented OnUpdate-covers-OnCreate fallback,
	// since this construct sets both explicitly.
	for _, key := range []string{"Create", "Update"} {
		joined, _ := joinedFnJoinString(t, lastProps[key])
		for _, want := range []string{
			`"service":"SSM"`,
			`"action":"putParameter"`,
			`"region":"us-west-1"`,
			`"Name":"` + siteCertArnParamName + `"`,
			`"Overwrite":true`,
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("Custom::AWS %s payload = %q, missing %q", key, joined, want)
			}
		}
		// physicalResourceId must reference the CERTIFICATE's own logical id
		// SPECIFICALLY — not merely have some Ref to it somewhere in the
		// payload (the "Value" field already carries one of those). A fixed
		// literal in physicalResourceId would defeat the whole fix
		// identically to the bug this works around: a value-only ARN change
		// would no longer force a CloudFormation Update. The code below
		// extracts exactly the physicalResourceId.id fragment's content and
		// compares it against a bracketed marker standing in for {Ref:
		// certLogicalID} — see joinedFnJoinString's doc comment for why the
		// marker is bracketed.
		const physField = `"physicalResourceId":{"id":"`
		idx := strings.Index(joined, physField)
		if idx < 0 {
			t.Fatalf("Custom::AWS %s payload = %q has no physicalResourceId.id field", key, joined)
		}
		rest := joined[idx+len(physField):]
		end := strings.Index(rest, `"}`)
		if end < 0 {
			t.Fatalf("Custom::AWS %s payload = %q has an unterminated physicalResourceId.id field", key, joined)
		}
		wantRef := "[" + certLogicalID + "]"
		if got := rest[:end]; got != wantRef {
			t.Errorf("Custom::AWS %s payload's physicalResourceId.id = %q, want %q (a {Ref} to "+
				"the certificate resource, not a fixed literal) — otherwise a cert replacement "+
				"does not change this resource's own physical id and CloudFormation has no "+
				"reason to re-invoke the SDK call", key, got, wantRef)
		}
	}

	// The IAM policy backing the custom resource's Lambda must be scoped to
	// the one parameter it writes, never AwsCustomResourcePolicy_ANY_RESOURCE
	// ("*") — mirroring the least-privilege precedent for the other
	// /rosterbot/* parameters (RP_ID/RP_ORIGIN) elsewhere in this package.
	var sawScopedStatement bool
	for _, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::IAM::Policy" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		doc, _ := props["PolicyDocument"].(map[string]any)
		for _, s := range asSlice(doc["Statement"]) {
			stmt, _ := s.(map[string]any)
			action, _ := stmt["Action"].(string)
			if action != "ssm:PutParameter" {
				continue
			}
			resource := stmt["Resource"]
			if resource == "*" {
				t.Errorf("CertArnParamWriter's IAM policy grants ssm:PutParameter on Resource \"*\"; "+
					"want it scoped to %s", siteCertArnParamArn)
				continue
			}
			resStr, isStr := resource.(string)
			if !isStr {
				t.Errorf("CertArnParamWriter's IAM policy Resource = %#v, want the literal scoped ARN string", resource)
				continue
			}
			if resStr != siteCertArnParamArn {
				t.Errorf("CertArnParamWriter's IAM policy Resource = %q, want %q", resStr, siteCertArnParamArn)
			}
			sawScopedStatement = true
		}
	}
	if !sawScopedStatement {
		t.Fatal("no IAM policy statement granting ssm:PutParameter found for the cert-ARN writer")
	}
}

// certRawTemplate synthesizes stack's template to the same raw
// map[string]any shape infraTemplate uses, without infraTemplate's
// once-shared InfraStack scope — this test needs the CERT stack's own
// resources, not InfraStack's.
func certRawTemplate(t *testing.T, stack awscdk.Stack) (assertions.Template, map[string]any) {
	t.Helper()
	tpl := assertions.Template_FromStack(stack, nil)
	raw, err := json.Marshal(tpl.ToJSON())
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal template: %v", err)
	}
	return tpl, m
}

// certificateLogicalID returns the logical id of the one
// AWS::CertificateManager::Certificate resource in the template.
func certificateLogicalID(t *testing.T, resources map[string]any) string {
	t.Helper()
	var ids []string
	for logicalID, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] == "AWS::CertificateManager::Certificate" {
			ids = append(ids, logicalID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("found %d AWS::CertificateManager::Certificate resource(s), want exactly 1", len(ids))
	}
	return ids[0]
}

// joinedFnJoinString re-assembles a synthesized {"Fn::Join": [sep, [...]]}
// value into its concatenated string form, substituting each embedded
// {"Ref": "<logicalID>"} token with that id (bracketed, so it cannot be
// confused with literal text) and also returning the set of logical ids it
// referenced. This is the shape CDK actually emits for a custom resource's
// Create/Update property — inspecting it needs the fragments walked, not a
// direct `.(string)` assertion, which would panic (this property is never a
// plain Go string).
func joinedFnJoinString(t *testing.T, v any) (joined string, refs []string) {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("Custom::AWS payload = %#v (%T), want an Fn::Join map", v, v)
	}
	fj, ok := m["Fn::Join"].([]any)
	if !ok || len(fj) != 2 {
		t.Fatalf("Custom::AWS payload = %#v, want a 2-element Fn::Join", m)
	}
	sep, _ := fj[0].(string)
	parts := asSlice(fj[1])
	strs := make([]string, 0, len(parts))
	for _, p := range parts {
		switch pv := p.(type) {
		case string:
			strs = append(strs, pv)
		case map[string]any:
			if ref, ok := pv["Ref"].(string); ok {
				refs = append(refs, ref)
				strs = append(strs, "["+ref+"]")
				continue
			}
			t.Fatalf("Fn::Join fragment %#v is a map with no Ref key", pv)
		default:
			t.Fatalf("Fn::Join fragment %#v has unexpected type %T", p, p)
		}
	}
	return strings.Join(strs, sep), refs
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
		// enableBuild mirrors the real deploy: the buildspec always passes
		// `-c enableBuild=true`, so the deployed stack always carries the
		// CodeBuild project. Synthesizing without it would leave that project
		// untestable — the codebuild_test.go pins need it in this template.
		app := awscdk.NewApp(&awscdk.AppProps{
			Context: &map[string]interface{}{"enableBuild": "true"},
		})
		stack := NewInfraStack(app, "TestStack", &InfraStackProps{
			StackProps: awscdk.StackProps{Env: env()},
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
//
// The scoping now carries a second passenger: the deprecated-hostname 301.
// Letting that reach /v1/* would break every POST that followed it — a 301
// preserves neither method nor body — and would close the one break-glass the
// DashboardCdnDefaultUrl output exists to keep open.
func TestInviteRewrite_IsScopedToTheDefaultBehaviorOnly(t *testing.T) {
	logicalID, cfg := dashboardDistribution(t)

	def, _ := cfg["DefaultCacheBehavior"].(map[string]any)
	assocs := asSlice(def["FunctionAssociations"])
	if len(assocs) == 0 {
		t.Errorf("%s DefaultCacheBehavior has no FunctionAssociations; /invite will 403 "+
			"and the universal-link fallback page will not render", logicalID)
	}
	// The EVENT TYPE, not merely the presence of an association. A
	// viewer-response function can neither rewrite request.uri nor
	// short-circuit a request, so flipping this one constant silently returns
	// /invite to a 403 and puts the deprecated hostname back to serving a
	// dashboard nobody can sign into — with every other assertion in this file
	// still passing, because the association exists, the allowlist still
	// matches the aliases and the source still says 301.
	for _, a := range assocs {
		assoc, _ := a.(map[string]any)
		if got := assoc["EventType"]; got != "viewer-request" {
			t.Errorf("%s DefaultCacheBehavior function association EventType = %v, want "+
				"viewer-request; a viewer-response function cannot rewrite the URI or return "+
				"the deprecated-hostname redirect, so both behaviours silently stop working",
				logicalID, got)
		}
	}

	// The /v1/* behavior must EXIST, not merely be free of associations.
	// Without this the loop below is vacuous: asSlice(nil) is nil, so deleting
	// or renaming the behavior makes this test pass while /v1/* falls through
	// to DefaultBehavior, where the function 301s every API request — dropping
	// method and body on every POST and closing the DashboardCdnDefaultUrl
	// break-glass that the redirect was scoped to preserve.
	var sawAPIBehavior bool
	for _, b := range asSlice(cfg["CacheBehaviors"]) {
		beh, _ := b.(map[string]any)
		if beh["PathPattern"] == "/v1/*" {
			sawAPIBehavior = true
		}
		if _, ok := beh["FunctionAssociations"]; ok {
			t.Errorf("%s behavior %v carries a FunctionAssociation; the viewer-request function "+
				"must not reach /v1/*, whose 401/403 responses the iOS sign-in gate depends on "+
				"and whose POSTs a 301 would break", logicalID, beh["PathPattern"])
		}
	}
	if !sawAPIBehavior {
		t.Errorf("%s has no /v1/* cache behavior; the API now falls through to DefaultBehavior "+
			"and inherits the viewer-request function, so every API request on a deprecated "+
			"hostname 301s and every POST loses its method and body", logicalID)
	}
}

// dashboardDistribution returns DashboardCdn's logical id and synthesized
// DistributionConfig.
//
// It matches on the apex being SOMEWHERE in Aliases rather than at a fixed
// position. The prior spelling asserted aliases[0] == dashHost, which is a
// property of the argument order in one jsii.Strings call and not of anything
// CloudFront cares about — so reordering that call made the loop match nothing
// and the test would have reported PASS by examining no distribution at all,
// if the trailing count check had not caught it. The count check stays, here,
// where every caller inherits it.
func dashboardDistribution(t *testing.T) (string, map[string]any) {
	t.Helper()
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)

	var ids []string
	var cfgs []map[string]any
	for logicalID, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::CloudFront::Distribution" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		cfg, _ := props["DistributionConfig"].(map[string]any)
		if !slices.Contains(hostStrings(asSlice(cfg["Aliases"])), apexHost) {
			continue // the recap distribution; not the dashboard
		}
		ids = append(ids, logicalID)
		cfgs = append(cfgs, cfg)
	}
	if len(ids) != 1 {
		t.Fatalf("matched %d distributions carrying the apex alias %q, want exactly 1 — "+
			"the test is not watching what it thinks it is", len(ids), apexHost)
	}
	return ids[0], cfgs[0]
}

// hostStrings is the one place a synthesized host list (a template Aliases
// array) becomes []string. Both the distribution lookup and the
// allowlist-equality test read the same field, so a change in how CDK renders
// it — a token instead of a literal, say — has to be handled once rather than
// in two spellings that can be fixed independently.
func hostStrings(vals []any) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
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

// The redirect allowlist and the distribution's aliases must name the SAME set.
//
// They are one Go slice at the call site, so this looks like it cannot fail —
// which is the reason to assert it against the synthesized template instead of
// against that slice. The two values land in unrelated corners of the output
// (a CloudFront function's inline source, and a distribution's Aliases array),
// and either can be edited without the other. Both directions are real bugs
// and neither surfaces at deploy time:
//
//   - alias served but NOT allowlisted -> that hostname 301s away from itself,
//     so the name is advertised, certificated, DNS'd, and unusable.
//   - allowlisted but NOT served -> nothing, today; it is dead configuration
//     that quietly stops being dead the day someone adds the alias back.
func TestDashboardRedirect_AllowlistMatchesTheDistributionAliases(t *testing.T) {
	_, cfg := dashboardDistribution(t)

	aliases := hostStrings(asSlice(cfg["Aliases"]))
	allow := redirectAllowlist(t)

	sort.Strings(aliases)
	sort.Strings(allow)
	if !slices.Equal(aliases, allow) {
		t.Errorf("distribution aliases %v != redirect allowlist %v; a served name missing from "+
			"the allowlist 301s away from itself", aliases, allow)
	}
}

// The redirect target must be a member of its own allowlist.
//
// This is the one failure here that is not merely a broken hostname: if the
// apex were served but absent from the allowlist, every apex request would 301
// to the apex, forever, and the dashboard would be gone for everyone at once.
// It reads as pedantic — the target and the first allowlist entry are the same
// constant today — and it is the difference between a deploy and an outage the
// moment they stop being.
func TestDashboardRedirect_TargetsAHostInsideItsOwnAllowlist(t *testing.T) {
	code := dashboardFunctionCode(t)

	const marker = "'https://"
	i := strings.Index(code, marker)
	if i < 0 {
		t.Fatal("no redirect target in the viewer-request function; the deprecated " +
			"*.cloudfront.net hostname serves the dashboard again")
	}
	rest := code[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		t.Fatal("unterminated redirect target in the viewer-request function")
	}
	target := rest[:j]

	if !slices.Contains(redirectAllowlist(t), target) {
		t.Errorf("redirect target %q is not in the allowlist %v; requests to it would 301 to "+
			"themselves in an unbreakable loop", target, redirectAllowlist(t))
	}
	if target != apexHost {
		t.Errorf("redirect target = %q, want the apex %q — the apex is the WebAuthn RP ID, so "+
			"it is the only name a deprecated hostname may hand a signing-in user to",
			target, apexHost)
	}
	// The statusCode, not the digits. A bare Contains(code, "301") matches the
	// literal anywhere in the source — a comment, a future max-age=3016, a
	// hostname like d301abc.cloudfront.net added to the allowlist — so the
	// redirect could become a 302 while the assertion still passed.
	if !strings.Contains(code, "statusCode: 301") {
		t.Error("the hostname redirect is not a 301; a temporary redirect tells clients the " +
			"deprecated name is coming back, so link-rewriting clients never update their " +
			"references and the hostname can never actually be retired")
	}
}

// dashboardFunctionCode returns the inline source of the function the DASHBOARD
// distribution actually associates, found by following its FunctionARN rather
// than by assuming the stack contains exactly one CloudFront function.
//
// The distinction matters the day a second function appears — the recap
// distribution has none today, but retiring ITS *.cloudfront.net name is
// explicitly deferred work. A whole-template count would then fail every test
// in this group with "found 2, want 1", naming neither distribution and
// pointing the reader at the wrong change.
//
// The code is asserted to be a plain string, not an Fn::Join: it interpolates
// only Go constants, so a token appearing here would mean someone reached for a
// resource attribute and reintroduced the Function -> Distribution cycle.
func dashboardFunctionCode(t *testing.T) string {
	t.Helper()
	_, cfg := dashboardDistribution(t)
	_, raw := infraTemplate(t)
	resources, _ := raw["Resources"].(map[string]any)

	def, _ := cfg["DefaultCacheBehavior"].(map[string]any)
	assocs := asSlice(def["FunctionAssociations"])
	if len(assocs) != 1 {
		t.Fatalf("dashboard DefaultCacheBehavior has %d function associations, want exactly 1",
			len(assocs))
	}
	assoc, _ := assocs[0].(map[string]any)

	// FunctionARN renders as {"Fn::GetAtt": ["<logicalID>", "FunctionARN"]}.
	att, _ := assoc["FunctionARN"].(map[string]any)
	parts := asSlice(att["Fn::GetAtt"])
	if len(parts) == 0 {
		t.Fatalf("dashboard function association FunctionARN is %v, not an Fn::GetAtt — "+
			"cannot resolve which function the distribution uses", assoc["FunctionARN"])
	}
	logicalID, _ := parts[0].(string)

	res, _ := resources[logicalID].(map[string]any)
	if res == nil || res["Type"] != "AWS::CloudFront::Function" {
		t.Fatalf("association names %q, which is not a CloudFront function in this template",
			logicalID)
	}
	props, _ := res["Properties"].(map[string]any)
	code, ok := props["FunctionCode"].(string)
	if !ok {
		t.Fatalf("FunctionCode is %T, not a literal string — an unresolved token here means "+
			"the function references a resource attribute, which is the circular dependency "+
			"the allowlist exists to avoid", props["FunctionCode"])
	}
	return code
}

// redirectAllowlist parses the canonical-host array out of the function source.
func redirectAllowlist(t *testing.T) []string {
	t.Helper()
	code := dashboardFunctionCode(t)

	const marker = "var canonical = ["
	i := strings.Index(code, marker)
	if i < 0 {
		t.Fatalf("no %q in the viewer-request function; without it every hostname is canonical "+
			"and the deprecated *.cloudfront.net name serves the dashboard again", marker)
	}
	rest := code[i+len(marker):]
	j := strings.Index(rest, "]")
	if j < 0 {
		t.Fatal("unterminated canonical-host array in the viewer-request function")
	}

	var hosts []string
	for _, f := range strings.Split(rest[:j], ",") {
		hosts = append(hosts, strings.Trim(strings.TrimSpace(f), "'"))
	}
	return hosts
}

// ---- rosterbot-klbh, consumer half: InfraStack reads the parameter the cert
// stack writes, and nothing crosses the region boundary through CDK's exporter.

var (
	appOnce   sync.Once
	appCert   awscdk.Stack
	appInfra  awscdk.Stack
	appCertJS map[string]any
	appInfraJ map[string]any
)

// deployedApp synthesizes BOTH stacks through buildStacks — the same function
// main() calls — so these tests see the app CodeBuild deploys rather than a
// hand-assembled look-alike. That matters here more than elsewhere: whether
// CDK emits its cross-region export plumbing is decided at app synth, from
// which references actually cross between the two stacks, and a test that
// synthesized InfraStack alone could never observe it either way.
func deployedApp(t *testing.T) (certStack, infraStack awscdk.Stack, certRaw, infraRaw map[string]any) {
	t.Helper()
	appOnce.Do(func() {
		app := awscdk.NewApp(&awscdk.AppProps{
			Context: &map[string]interface{}{"enableBuild": "true"},
		})
		appCert, appInfra = buildStacks(app)
		_, appCertJS = certRawTemplate(t, appCert)
		_, appInfraJ = certRawTemplate(t, appInfra)
	})
	return appCert, appInfra, appCertJS, appInfraJ
}

// The consumer half of the fix. Every distribution's viewer certificate must be
// the literal CloudFormation dynamic reference on the SAME parameter name the
// producer writes — not a Fn::GetAtt on CDK's ExportsReader (the buggy path),
// not a {Ref} to an AWS::SSM::Parameter::Value<String> template Parameter
// (which `cdk deploy --previous-parameters` freezes at its first value, the
// same silent-staleness class one level up), and not a literal ARN (which
// cannot follow a replacement at all). Sharing the constant with the producer
// test is what makes a rename on either side fail here instead of at deploy.
func TestInfraStack_ReadsTheCertArnAsADynamicReferenceOnTheProducersParameter(t *testing.T) {
	_, _, _, infraRaw := deployedApp(t)
	resources, _ := infraRaw["Resources"].(map[string]any)
	want := "{{resolve:ssm:" + siteCertArnParamName + "}}"

	var distributions int
	for logicalID, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "AWS::CloudFront::Distribution" {
			continue
		}
		distributions++
		props, _ := res["Properties"].(map[string]any)
		cfg, _ := props["DistributionConfig"].(map[string]any)
		vc, _ := cfg["ViewerCertificate"].(map[string]any)
		got, isStr := vc["AcmCertificateArn"].(string)
		if !isStr {
			t.Errorf("%s AcmCertificateArn = %#v, want the literal string %q "+
				"(a structured value here is a GetAtt on the ExportsReader or a Ref to a "+
				"template Parameter — both re-open rosterbot-klbh)", logicalID, vc["AcmCertificateArn"], want)
			continue
		}
		if got != want {
			t.Errorf("%s AcmCertificateArn = %q, want %q", logicalID, got, want)
		}
	}
	if distributions != 2 {
		t.Fatalf("found %d AWS::CloudFront::Distribution resource(s), want 2 (SiteCdn and DashboardCdn) — "+
			"the assertion above ran over fewer distributions than it thinks", distributions)
	}
}

// With the reference gone, CDK must synthesize NEITHER half of its cross-region
// export plumbing: the writer whose name-keyed Update diff is the upstream bug,
// and the reader that resolved the stale value. This is also what makes
// CrossRegionReferences removable from both stacks — and once it is removed, a
// future edit that hands InfraStack the certificate construct again fails at
// synth ("Set crossRegionReferences=true to enable cross region references")
// instead of quietly reinstating the path this bead exists to close.
func TestCertHandoff_SynthesizesNoCrossRegionExportPlumbing(t *testing.T) {
	_, _, certRaw, infraRaw := deployedApp(t)
	if n := countResourcesOfType(certRaw, crossRegionWriterType); n != 0 {
		t.Errorf("InfraCertStack synthesizes %d %s resource(s), want 0 — a reference still crosses", n, crossRegionWriterType)
	}
	if n := countResourcesOfType(infraRaw, crossRegionReaderType); n != 0 {
		t.Errorf("InfraStack synthesizes %d %s resource(s), want 0 — a reference still crosses", n, crossRegionReaderType)
	}
}

// countResourcesOfType counts a raw template's resources of one CloudFormation
// type. Preferred over assertions.ResourceCountIs here because that panics
// through jsii on a mismatch, aborting every test after it in the binary.
func countResourcesOfType(raw map[string]any, typ string) int {
	resources, _ := raw["Resources"].(map[string]any)
	var n int
	for _, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] == typ {
			n++
		}
	}
	return n
}

const (
	crossRegionWriterType = "Custom::CrossRegionExportWriter"
	crossRegionReaderType = "Custom::CrossRegionExportReader"
)

// Positive control for the test above. A zero count proves nothing if the type
// names are not what CDK actually emits, so this builds the smallest app in
// which a certificate ARN really does cross from us-east-1 to us-west-1 and
// checks that exactly those two resource types appear. If CDK ever renames
// them, this fails and the zero-count test is known to have gone blind.
func TestCrossRegionPlumbingDetector_SeesBothHalvesWhenAReferenceCrosses(t *testing.T) {
	app := awscdk.NewApp(nil)
	producer := awscdk.NewStack(app, jsii.String("Producer"), &awscdk.StackProps{
		Env: certEnv(), CrossRegionReferences: jsii.Bool(true),
	})
	cert := awscertificatemanager.NewCertificate(producer, jsii.String("Cert"), &awscertificatemanager.CertificateProps{
		DomainName: jsii.String("positive-control.invalid"),
	})
	consumer := awscdk.NewStack(app, jsii.String("Consumer"), &awscdk.StackProps{
		Env: env(), CrossRegionReferences: jsii.Bool(true),
	})
	awscdk.NewCfnOutput(consumer, jsii.String("CertArn"), &awscdk.CfnOutputProps{Value: cert.CertificateArn()})

	_, producerRaw := certRawTemplate(t, producer)
	_, consumerRaw := certRawTemplate(t, consumer)
	if n := countResourcesOfType(producerRaw, crossRegionWriterType); n != 1 {
		t.Errorf("producer synthesizes %d %s resource(s), want 1 — the detector is not seeing CDK's writer", n, crossRegionWriterType)
	}
	if n := countResourcesOfType(consumerRaw, crossRegionReaderType); n != 1 {
		t.Errorf("consumer synthesizes %d %s resource(s), want 1 — the detector is not seeing CDK's reader", n, crossRegionReaderType)
	}
}

// Nothing in the templates orders the two stacks any more — the dynamic
// reference is a string CloudFormation resolves at deploy time, not a CDK
// reference — so the ordering the exporter used to imply has to be stated.
// Without it `cdk deploy --all` may update InfraStack before the parameter it
// reads has been (re)written, which on a cert change means attaching the
// certificate that is being replaced away.
func TestInfraStack_DeploysAfterTheCertStack(t *testing.T) {
	certStack, infraStack, _, _ := deployedApp(t)
	for _, dep := range *infraStack.Dependencies() {
		if *dep.StackName() == *certStack.StackName() {
			return
		}
	}
	t.Fatalf("InfraStack does not depend on %s; nothing orders the parameter's writer before its reader", *certStack.StackName())
}

// The parameter carries a Description naming its writer and reader, so an
// operator running `aws ssm describe-parameters` learns what depends on it
// before touching it. Adding the field is also the first Update the writer
// ever performs, which is the live proof the ADR's step 1 asks for: the
// parameter's version must advance on the deploy that ships this.
func TestCertArnParamWriter_DescribesTheParameter(t *testing.T) {
	_, _, certRaw, _ := deployedApp(t)
	resources, _ := certRaw["Resources"].(map[string]any)
	for _, r := range resources {
		res, _ := r.(map[string]any)
		if res["Type"] != "Custom::AWS" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		for _, key := range []string{"Create", "Update"} {
			joined, _ := joinedFnJoinString(t, props[key])
			const field = `"Description":"`
			idx := strings.Index(joined, field)
			if idx < 0 {
				t.Errorf("Custom::AWS %s payload has no Description field: %q", key, joined)
				continue
			}
			desc := joined[idx+len(field):]
			if end := strings.Index(desc, `"`); end >= 0 {
				desc = desc[:end]
			}
			for _, name := range []string{"InfraStack", "InfraCertStack"} {
				if !strings.Contains(desc, name) {
					t.Errorf("Custom::AWS %s Description = %q, does not name %s", key, desc, name)
				}
			}
		}
		return
	}
	t.Fatal("no Custom::AWS resource found in the cert stack")
}

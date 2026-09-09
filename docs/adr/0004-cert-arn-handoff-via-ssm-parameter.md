# 4. Hand off the cert ARN via a plain SSM parameter, not a CDK cross-region reference

Date: 2026-09-07
Status: Accepted — producer shipped 2026-09-07, consumer switched 2026-09-09 (PR #206), all four live checks observed on the real account 2026-09-09

## Context

`InfraStack` (us-west-1) consumes `InfraCertStack`'s certificate (us-east-1)
through CDK's built-in cross-region reference machinery: `CrossRegionReferences:
true` on both stacks makes CDK generate an `ExportsWriter` custom resource in
the producing stack that publishes the ARN into an SSM parameter the consuming
stack later reads.

**Measured 2026-08-19, during rosterbot-jloe.6's apex cutover (rosterbot-klbh):**
the certificate was replaced (a new SAN added), CloudFormation recorded both
`SiteCert` and the `ExportsWriter` custom resource as `UPDATE_COMPLETE`, and the
generated SSM parameter `/cdk/exports/InfraStack/InfraCertStackuseast1RefSiteCert...`
was left holding the OLD certificate's ARN — confirmed via
`get-parameter-history`: version 1 is the only version that has ever existed.
`InfraStack` resolved the stale ARN, attached the old certificate to
`DashboardCdn`, and CloudFront rejected the apex alias with an error naming the
**certificate** — which was healthy — rather than the actual broken component,
the export. Two deploys were spent chasing ACM before the export was suspected.

**Root cause is upstream, not in this repo.** Desk research (2026-09-04, filed
in the bead's NOTES) traced this to aws-cdk's own
`cross-region-ssm-writer-handler`: its Update handler computes
`newExports = except(exports, oldExports)` — a diff by SSM parameter **name**
existence, not value. The export name derives from the stable logical id, so a
certificate **replacement** keeps the same export name; the changed ARN is
filtered out of what the writer decides to publish, the Lambda succeeds,
CloudFormation records `UPDATE_COMPLETE`, and nothing is written. This is a
regression from `aws/aws-cdk#38059` (merged 2026-06-02, present in our pinned
v2.267.0 and still on `main` as of 2026-09-04): that PR deleted the
`throwIfAnyInUse`/`isInUse` guard that used to fail loudly on a changed export
(`#30771`) in order to fix an orphaned-parameter bug (`#27251`), and put
nothing in its place for "value changed on an existing key." No upstream issue
yet names this exact symptom; related: `#34813` (deadly embrace on reference
removal).

**Do not "fix" this with `ssm.StringParameter_ValueForStringParameter`.** That
helper emits an `AWS::SSM::Parameter::Value<String>` CloudFormation template
Parameter whose Default is the parameter *name*, and `cdk deploy`'s default
`--previous-parameters` behavior tells CloudFormation to `UsePreviousValue`
whenever the template's Parameter is unchanged — the same silent-staleness
failure mode, one level up (maintainer-confirmed on `#7722`; a whole-stack
variant is `#8888`). `valueFromLookup` is worse still: it resolves at synth
time and caches into `cdk.context.json`, so a value change requires clearing
context by hand.

## Decision

**Producer (this ADR, `InfraCertStack`, us-east-1):** write the certificate ARN
into a plain, separately-named SSM parameter, `/rosterbot/SITE_CERT_ARN`, in
us-west-1, via a hand-rolled `AwsCustomResource` (`publishCertArnParam` in
`infra/domain.go`) doing `ssm:putParameter` with `Overwrite: true`. Its
`PhysicalResourceId` is keyed on the certificate's own ARN token
(`cert.CertificateArn()`), not a fixed literal — that is the property the
built-in exporter lacks: a certificate replacement changes what that token
resolves to, which changes this resource's own physical id, which forces
CloudFormation to invoke the SDK call again on every subsequent deploy where
the cert changed, regardless of what CDK's own name-based export diff decides.
IAM is scoped from the start to
`arn:aws:ssm:us-west-1:476646938644:parameter/rosterbot/SITE_CERT_ARN` — never
`AwsCustomResourcePolicy_ANY_RESOURCE` — mirroring the existing least-privilege
precedent for the other `/rosterbot/*` parameters (`DASHBOARD_RP_ID`,
`DASHBOARD_RP_ORIGIN`) in `infra/infra.go`. `InstallLatestAwsSdk` is explicitly
`false`: the default installs the latest AWS SDK v3 from npm at deploy time,
a real network dependency this call does not need since the Lambda runtime's
bundled SDK already supports `ssm:PutParameter`.

**Consumer (`InfraStack`, us-west-1):** `importSiteCert` in `infra/domain.go`
hands each CloudFront distribution the certificate as
`Certificate_FromCertificateArn` over the CloudFormation **dynamic reference**
`{{resolve:ssm:/rosterbot/SITE_CERT_ARN}}` — the literal string in the template,
built with `awscdk.NewCfnDynamicReference`, not a template Parameter and not a
synth-time lookup. CloudFormation resolves it against the parameter's latest
version on every update of the distribution and records the version it
resolved (the deployed template of the old reader showed exactly this,
`{{resolve:ssm:…:2:<timestamp>}}`, pinned by CloudFormation rather than by
CDK), which is what lets a later update see that the value moved.
`InfraStackProps.Certificate` is gone: the import is unconditional inside
`NewInfraStack`, because "no cert ⇒ skip the alias domains" is the silent
failure the old REQUIRED panic existed to prevent, and a missing parameter now
fails the distribution update loudly, naming the parameter.

**No CDK reference crosses any more, so CDK's export plumbing is gone with
it.** This was not a choice available to stage. CDK emits the `ExportsWriter`
and `ExportsReader` custom resources only for a reference that actually crosses
stacks at synth; `CrossRegionReferences: true` is permission, not plumbing. The
moment `InfraStack` stopped receiving the certificate construct, both halves
left the templates, so the bead's "switch the consumer, then remove the
plumbing" was always one deploy. The flag is removed from both stacks for the
same reason it can be: with it gone, a future edit that hands `InfraStack` the
construct again fails at synth (`Set crossRegionReferences=true to enable cross
region references`) instead of quietly reinstating the buggy path.
`TestCertHandoff_SynthesizesNoCrossRegionExportPlumbing` pins the absence, and
`TestCrossRegionPlumbingDetector_SeesBothHalvesWhenAReferenceCrosses` is its
positive control — a zero count is only evidence if the detector can see the
thing it counts.

**Ordering is now stated, because nothing implies it.** `buildStacks` (the one
place both stacks are declared; `main` and the tests share it so the tests
synthesize the app CodeBuild deploys) calls `infraStack.AddDependency(certStack)`.
The exporter used to order the two through the reference; a parameter name is
a plain string and orders nothing. `TestInfraStack_DeploysAfterTheCertStack`
went red the moment the reference was removed and green when the dependency
was added, which is the whole reason it exists.

**The parameter carries a Description, and adding it was the ADR's step-1
proof.** CloudFormation invokes a custom resource's Update handler only when
that resource's own properties change, so the first deploy of the consumer
switch is also the first Update the writer ever performs — the parameter's
version advancing from 1 to 2 (same value, new Description) proves this
writer's Update path works where CDK's exporter did not. It is permanent
rather than the temporary nudge the earlier Status proposed: `aws ssm
describe-parameters` now says what writes it and what reads it.

## Status

**Consumer switched 2026-09-08** (this change); **producer shipped 2026-09-07**
(PR #203). The deploy delta was measured before merge by creating and
inspecting real change sets against both live stacks (not `cdk diff`, whose
change-set mode on CLI 2.1140 silently omitted the distribution and
custom-resource modifications the change set itself listed — use
`--method=template`, or create the change set):

- `InfraCertStack`: **Modify** `CertArnParamWriter` (`Custom::AWS`, static,
  direct modification of both Create and Update payloads — the Description);
  **Remove** `ExportsWriter`, its handler Lambda and its role.
- `InfraStack`: **Modify** `SiteCdn` and `DashboardCdn` (`DistributionConfig`,
  static, **Replacement: False** — the property text changes from a
  `Fn::GetAtt` on the reader to the dynamic reference while the resolved ARN
  stays `…/42c8ff20-…`); **Remove** `ExportsReader`, its handler Lambda and
  its role.

Deploy order is `InfraCertStack` then `InfraStack` (the explicit dependency).
Both deployed handlers are CDK 2.267.0's, so the writer's Delete deletes the
`/cdk/exports/InfraStack/…` parameter unconditionally and the reader's Delete
tolerates its absence (`InvalidResourceId` is caught) — checked in the
upstream handler sources, since the earlier NOTES flagged this as uncertain
after `#38059` removed the in-use check. The previously feared "deadly
embrace" therefore cannot occur in either order.

**Live checks, observed 2026-09-09 after PR #206 deployed (CodeBuild
`53e014a0`, `InfraCertStack` UPDATE_COMPLETE 16:21:09Z, `InfraStack`
UPDATE_COMPLETE 16:22:09Z).** Every one of the four passed; the commands are
kept because they are the recipe for the next certificate change:

1. `aws ssm get-parameter-history --name /rosterbot/SITE_CERT_ARN --region us-west-1`
   shows **version 2**, same value, with the Description. Version still 1
   means the writer's Update handler did not run and step 1 is NOT proven —
   stop and investigate before trusting the next cert change to it.
2. `aws cloudformation list-stack-resources --stack-name InfraStack --region us-west-1`
   lists no `ExportsReader`, and
   `aws cloudformation list-stack-resources --stack-name InfraCertStack --region us-east-1`
   lists no `ExportsWriter`.
3. `aws ssm get-parameters-by-path --path /cdk/exports/InfraStack/ --region us-west-1`
   returns nothing. If the old export parameter survives, the writer's Delete
   did not run to completion; delete it by hand, it has no reader.
4. `aws cloudfront get-distribution-config --id E135ZMD24EU5ON` reports
   `ViewerCertificate.ACMCertificateArn` = the ARN in the parameter, and both
   `https://rosterbot.dev` and `https://recaps.rosterbot.dev` present that
   certificate.

What was seen: (1) version 2 written 16:20:44Z, same ARN, Description
present — the writer's Update handler ran on the first property change it
was ever given, which is precisely what CDK's exporter failed to do; (2) the
`ExportsWriter` trio deleted 16:20:47–16:21:09Z and the `ExportsReader` trio
16:21:48–16:22:08Z, both cleanly; (3) the old export path empty; (4) both
distributions on `…/42c8ff20-…`, all three hostnames answering 200 with the
certificate whose serial ACM reports. Two things the change sets did not
predict: CloudFormation resolved the dynamic reference, found the ARN already
attached, and **did not update either distribution at all** (their last
update is still 2026-08-19, and the `InfraStack` update took 50 s), so the
change set's `Modify` on `SiteCdn`/`DashboardCdn` was conservative — while
the stored template now reads `{{resolve:ssm:/rosterbot/SITE_CERT_ARN:2:<ms>}}`
on both distributions, CloudFormation's own pin of the version it resolved,
which is what a later update compares the live parameter against; and the
deploy itself was delayed a day because the CodeBuild webhook was paused for
rosterbot-k2w0's drift positive control, which is unrelated to this ADR but
is the first thing to check when a merge produces no build.

## Consequences

- The certificate ARN is the only value crossing between the two regions, and
  it crosses as a parameter name. Neither stack carries `CrossRegionReferences`
  and no CDK-managed custom resource, Lambda or role exists for the handoff;
  the writer that remains is this repo's own, scoped to one parameter.
- **A certificate change propagates only through an `InfraStack` update.**
  CloudFormation re-resolves a dynamic reference when the resource that holds
  it is updated, and `cdk deploy` skips a stack whose template is unchanged.
  In this repo a certificate changes because a hostname was added, which is
  always a new alias on a distribution here, so the two arrive in one PR — but
  a change that touched `InfraCertStack` alone would sit unpropagated until
  `InfraStack` next deployed for any reason. CDK's exporter had the identical
  limitation; this ADR documents it rather than removes it.
- **A certificate REPLACEMENT still stalls the cert stack's cleanup.**
  `InfraCertStack` deploys first and tries to delete the old certificate while
  both distributions still use it; ACM refuses until `InfraStack` has moved
  them, which only happens after `InfraCertStack` completes. That is
  rosterbot-ck9y (the CodeBuild timeout) and predates this ADR; the parameter
  handoff neither causes nor fixes it.
- Reverting is a plain revert. `InfraCertStack` would re-create the writer
  (its Create handler writes the export parameter fresh) before `InfraStack`
  re-creates the reader (whose Create tags that parameter), because the
  dependency keeps that order; the distributions would fall back to a
  `Fn::GetAtt` on the re-created reader.
- `docs/aws-deployment.md`'s us-east-1 bootstrap bullet and `buildspec.yml`'s
  `cdk deploy --all` comment both describe the explicit dependency now, not the
  reference.

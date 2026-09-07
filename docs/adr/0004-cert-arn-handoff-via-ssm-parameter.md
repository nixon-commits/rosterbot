# 4. Hand off the cert ARN via a plain SSM parameter, not a CDK cross-region reference

Date: 2026-09-07
Status: Producer shipped; consumer switch PENDING (see Status below)

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

**Consumer (future work, not this change):** `InfraStack` will import the
certificate via `awscertificatemanager.Certificate_FromCertificateArn` on the
CloudFormation **dynamic reference** `{{resolve:ssm:/rosterbot/SITE_CERT_ARN}}`
— not a CFN template Parameter (immune to `UsePreviousValue`, since a dynamic
reference is resolved fresh on every Create/Update/Delete of the resource that
uses it, not cached into the template) — and `CrossRegionReferences` will be
removed from both stacks once the producer is confirmed live.

This is **deliberately staged**. Nothing reads `/rosterbot/SITE_CERT_ARN` yet.
This repo's dominant bug class is a control written but read by nothing, and
that is exactly what this change is on its own — the reader is the named next
step, not an oversight. It is staged this way because the alternative (writing
producer and consumer in one change) cannot be verified hermetically: the
consumer's dynamic reference would fail to resolve at deploy time unless the
parameter already exists in the live account, which requires the producer to
be deployed and confirmed *first* — an ordering hazard that is a property of
the live AWS account, not of the CDK template shape, so no synth-only test can
stand in for it.

## Status

**Producer shipped** (this change): `publishCertArnParam`, called once at the
end of `NewCertStack`. `InfraStackProps.Certificate`, `CrossRegionReferences`,
and `NewInfraStack` are untouched — `InfraStack` still imports the certificate
through the (buggy) built-in cross-region path until the consumer switch below
is confirmed live.

**Consumer switch is PENDING a live check only a human can run**, in two
gated deploys (a "deadly embrace" migration — removing a cross-region
reference is not a plain revert):

1. After this producer ships, confirm via
   `aws ssm get-parameter-history --name /rosterbot/SITE_CERT_ARN --region us-west-1`
   that the parameter's version actually advances on the next `InfraCertStack`
   update — proving this Update handler fires where CDK's own exporter (stuck
   at version 1 forever per the bead) does not. A real cert replacement is the
   honest trigger; if none is due, the nudge must touch THIS resource's own
   properties (temporarily add a `Description` field to
   `certArnPutParameterCall`'s `Parameters` map and deploy), because
   CloudFormation only re-invokes a custom resource's handler when its own
   properties change — a nudge elsewhere in `InfraCertStack` leaves this
   resource byte-identical and proves nothing.
2. Switch `InfraStack`'s certificate import to the dynamic reference and
   redeploy, confirming `DashboardCdn` still resolves the correct certificate.
3. Only then remove `CrossRegionReferences` from both stacks, confirming
   CloudFormation actually permits deleting the old export — the NOTES flag
   this as "now uncertain" since `#38059` removed the in-use check entirely,
   so it must be checked empirically against the real stack, not assumed.

The one-time manual workaround (`aws ssm put-parameter --overwrite` against
the CDK-generated export parameter) remains in force for the *current*
outage until step 2 above lands; this ADR does not retroactively fix an
already-stale export, it prevents the next one from recurring silently once
the consumer switch is complete.

## Consequences

- One more Lambda-backed custom resource (a CDK "singleton Lambda" shared by
  every `AwsCustomResource` in `InfraCertStack`) deploys into `InfraCertStack`
  on the next `cdk deploy --all` after this merges. Verify with
  `aws ssm get-parameter --name /rosterbot/SITE_CERT_ARN --region us-west-1`.
- Because the writer only runs when `InfraCertStack` itself changes, this rides
  on the next deliberate cert-stack touch rather than landing (or being
  observably correct) the moment it merges.
- Until the consumer switch lands, this parameter is inert — a real risk that
  it gets treated as "done" and forgotten. The staged-migration framing above
  is deliberate and should be preserved rather than read as a completed fix.
- `docs/aws-deployment.md`'s us-east-1 bootstrap bullet (the one describing the
  `InfraCertStack` cross-region reference) points here.

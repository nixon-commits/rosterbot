# User registration

How a person gets an account on the rosterbot dashboard.

Passkeys are the only login method. There is no password to set, no password to
reset, and no email verification — which means the whole flow is: an admin mints
a link, hands it over, and the person enrolls a passkey with it.

> **Status.** The full tenancy is live in production: per-user state
> (rosterbot-crq.11), self-serve Fantrax connect (rosterbot-crq.12), per-tenant
> job fan-out, and the admin Tenants tab. Onboarding someone gives them their
> own bot, propose-only until they enable writes.

## The model

```
admin mints an invite  ──▶  hands the token over  ──▶  user enrolls a passkey
   rosterbot invite          out-of-band (text/DM)      in their browser
```

Three properties are worth understanding before using it, because each one
shapes what you can and cannot do afterwards.

**The token is shown once and is not recoverable.** Only its SHA-256 is stored,
so a leak of the identity table yields no usable links. If a token is lost,
mint another and let the first expire — there is nothing to look up.

**A link is single-use and scoped to one person.** It authorizes exactly one
passkey enrollment, for exactly one user id. It cannot be used to add a passkey
to somebody else's account, which is why the old global bootstrap token no
longer enrolls anyone.

**Out-of-band delivery is what attests the email.** There is no verification
mail. You type the address when minting, and hand the link to the person you
believe owns it. At connect time (once rosterbot-crq.12 lands) Fantrax's own
`UserInfo.Email` corroborates it.

## Minting an invite

```bash
rosterbot invite --email dave@example.test --name Dave --team team-7
```

The dashboard's admin **Tenants** tab mints the same link (`POST
/v1/tenants/invite`) — the invite form at the top of the tab shows the link
once with a copy button. Either path produces the identical record: member
role, active, `auto_apply` off.

| flag | meaning |
|---|---|
| `--email` | required, unique across users. Cannot be changed later without a migration. |
| `--name` | display name shown in the OS passkey picker. Defaults to the email. |
| `--team` | the Fantrax team this person manages. Still optional to the flag, but a user without one **cannot connect Fantrax** — see below. |
| `--ttl` | how long the link stays valid. Default 72h. |
| `--dry-run` | print what would be created, mint nothing. |

Output:

```
user    fTAmgMmFCgUUTWrSsFRM4dWQvKaEz6lUnEbazvmoHlk3Qay4mkJsGVlz5CCixBmb...
email   dave@example.test
team    team-7 (proven against Fantrax at connect time)
expires 2026-08-17T23:59:17Z

token   G2lfuIS1_AG6Ug1XBnLPbD5c7HkKaTBzHPeHjWoWAyk

Shown once — only its hash is stored. Deliver it out-of-band.
```

The user record is created **now**, not at redemption. That is deliberate: the
email and team uniqueness constraints are enforced while you are still looking
at the terminal, rather than surfacing as a confusing failure when the invitee
clicks their link.

### A user with no team cannot connect

`--team` remains optional to the flag, because a record is still useful without
one — the invitee can sign in and read the dashboard. What they cannot do is
connect their Fantrax account: `connect` fails with `no_team`.

That refusal is deliberate and it is not a formality. The connect task exists to
prove ownership against Fantrax's own `MyTeamIDs`, and an empty team gives it
nothing to compare. Before this was enforced, an empty team skipped the
ownership check entirely and the connection was recorded **verified** having
proven nothing (rosterbot-crq.18).

`no_team` is an admin error wearing a user's clothes. The invitee's credentials
are fine and re-entering them cannot help — someone has to assign the team. Note
that the operator's own record is affected: neither `migrate-identity` nor the
bootstrap profile sets a `TeamID`, so **the operator must have a team assigned
before their own connect will succeed.**

On AWS the command needs `STATE_BUCKET` and `IDENTITY_TABLE`; locally it writes
to `.lineup/` and needs neither.

## Enrolling

The invitee opens the dashboard with the token and enrolls a passkey. Their
device prompts for Touch ID / Windows Hello / a security key, and on success
they are logged in immediately.

Under the hood, `register/begin` reads the link to learn who the ceremony is
for, and `register/finish` **redeems** it. The ordering matters: if the user
abandons the ceremony — closes the tab at the biometric prompt — the link is
still unspent and they can retry. Burning it at `begin` would destroy a
single-use link on a ceremony that never happened.

## Logging in

Login is *discoverable* (usernameless). The user clicks "Sign in with passkey",
their authenticator offers whichever resident credential matches the site, and
the server learns who they are from the credential itself.

There is nothing to type, and `login/begin` deliberately returns a challenge
whether or not anyone is registered — so it cannot be used to probe which
accounts exist.

## Adding a second device

A logged-in user can enroll another passkey from the **Passkeys** section of
the Settings page — where each passkey can also be named ("Jon's phone") and
shows its registration date. No invite link is needed; the existing session
authorizes it.

This is worth encouraging at onboarding. A user with two devices has a recovery
path that does not involve you.

## Recovery: a lost device

An invite and a recovery are the same primitive — an enrollment link scoped to
an existing account. Mint one from the admin **Tenants** tab ("Recovery link"
on the user's row, `POST /v1/tenants/{id}/recovery`). Do **not** re-run
`rosterbot invite` with the same email: invite *creates* a user, so it refuses
with `email already claimed` rather than minting a recovery link.

They enroll a new passkey with it. Then revoke the lost credential from the
Passkeys section of the Settings page — revocation removes both the credential and its lookup index, so a
revoked passkey stops working immediately rather than merely being hidden.

If **everyone** is locked out (for the CloudFront caveat below, or any other
reason), the operator's bearer token still reaches the API and can be used to
mint links. It is deliberately independent of the user store: it keeps working
when DynamoDB is unreachable, when a migration has not run, and when
authorization itself is broken.

```bash
ROSTERBOT_API_TOKEN=$(aws ssm get-parameter --region us-west-1 \
  --name /rosterbot/ROSTERBOT_API_TOKEN --with-decryption \
  --query Parameter.Value --output text)
curl -H "Authorization: Bearer $ROSTERBOT_API_TOKEN" https://<DashboardUrl>/v1/runs
```

## What each account can do

| | member | admin |
|---|---|---|
| Own lineup, runs, trades, reports | ✅ | ✅ |
| `GET /v1/infra` (deployment health) | ❌ 403 | ✅ |
| Launch own-team jobs (`optimize`) | ✅ | ✅ |
| Launch league-wide jobs (`archive`, `recap-site`, …) | ❌ 403 | ✅ |
| Mint invites | ❌ | ✅ (CLI or Tenants tab) |

New users are created as **members** with `auto_apply` **off**. Auto-apply is
the setting that lets the bot write to a roster, and it is turned on
deliberately by a person — never inherited from onboarding.

Sessions last 30 days. Incrementing a user's `token_version` invalidates every
session they hold, for that user alone.

## Troubleshooting

| symptom | cause |
|---|---|
| `403 not authorized to register a passkey` | Link expired, already redeemed, or wrong token. Mint a new one. |
| `403` when using the **bearer token** to register | Expected. The bootstrap token no longer enrolls passkeys — use an invite link. |
| `400 passkey registration failed` | Origin mismatch. The RP origin is fixed; locally `serve` only works on `http://localhost:8080`. |
| `401` on every route right after a successful login | The session's user record is missing. On a fresh deployment run `rosterbot migrate-identity --apply`. |
| `login failed` on a credential that used to work | The credential is not in the identity table. Login reads DynamoDB only; the legacy `webauthn/identity.json` is no longer consulted. |
| Everyone logged out at once | Expected after a `SessionSecret` rotation, or after the deploy that added a subject to session cookies. Log in again. |

## Known risk: passkeys are bound to the domain

Passkeys are cryptographically bound to their **RP ID**, which is currently the
CloudFront distribution's hostname. There is no migration path for that binding.

If the distribution is ever *replaced* rather than updated, its hostname changes
and **every enrolled passkey stops working at once**. Recovery is an invite link
per person, which is why the bearer token matters.

Two mitigations: treat the `DashboardCdn` logical id as load-bearing
infrastructure, and alarm on any change to the SSM parameter
`/rosterbot/DASHBOARD_RP_ID` — that change *is* the mass-invalidation event.

Moving to a custom domain would remove the risk permanently, and would have to
happen **before** onboarding people, since it invalidates existing passkeys too.

## What users should be told

Two things were decided deliberately and should be stated plainly to anyone
being onboarded, rather than discovered later:

- **The admin can read any user's data**, including their trade valuations and
  lineup decisions. The admin is also a competitor in the league.
- **Once Fantrax connect ships, the bot stores the user's Fantrax password**
  (encrypted; only the job runner can decrypt it, never the public API) so it
  can re-authenticate unattended.

Someone who would decline either deserves to decline before handing anything
over.

# Sleeper memberships — design

Date: 2026-08-21
Status: Draft (design settled by interview; not yet decomposed into issues)
Extends `2026-08-12-multiuser-passkey-tenancy-design.md`, which built the
per-tenant identity this hangs leagues off. Absorbs the app-facing half of
`rosterbot-z1g`.

## Problem

A tenant today belongs to exactly one league, and that league is a property of
the *deployment* rather than of the tenant: `FANTRAX_LEAGUE_ID` is one env var
(`internal/config/config.go:48`, wired from SSM at `infra/infra.go:356`, read
directly by `cmd/connect.go`). `User.TeamID` (`internal/lineupapi/user.go:94`)
names their team inside it. There is no way to express "I am also in these
other leagues", and no way for a user to tell the system which leagues those
are without an admin typing an id.

The ask: let a user enter their Sleeper username, see the leagues that username
belongs to, pick the ones they care about, and have that stored on their tenant
— the flow every other fantasy site already offers. And, more broadly, support
belonging to more than one league at all.

## What the codebase already decides for you

Four findings from reading both trees that constrain this more than any
preference could.

**Sleeper's API is read-only.** `internal/sleeper/client.go` is seven exported
methods, every one a GET against `https://api.sleeper.app/v1`, and Sleeper
publishes no write endpoints. RosterBot's central act on Fantrax — applying a
lineup, with `AutoApply`, the `dry_run` guardrail and the mutating-job
confirmations built around it — has no counterpart on Sleeper and cannot be
given one without reverse-engineering a private path. A Sleeper league is
therefore a fundamentally different kind of thing from a Fantrax league, and a
design that pretends otherwise will keep tripping over that.

**The credential machinery has nothing to do.** A Sleeper username is a public
identifier; league, roster and user data are world-readable. There is no
password to seal, no chromedp login, no `needs_reconnect` lifecycle, and no
`connect` job. The single scariest step in today's onboarding simply does not
exist on this platform.

**Everything expensive about multi-league serves writes and history.** The
thirteen `PerTenant: true` artifacts in `internal/statestore/layout/layout.go`
exist because jobs produce durable state per user. A league dimension beside the
existing `user=` segment is only needed if something *writes* per league. Nothing
read-only does.

**`User.TeamID` is load-bearing and worth not disturbing.** It is set from the
invite, proven against Fantrax's own `MyTeamIDs` at connect time rather than
trusted from the request (`internal/lineupapi/connect.go:81`), and carries the
store's one-claimant-per-team constraint (`ErrTeamTaken`). Migrating it into a
collection would put all three properties back in play for no gain.

## Decisions

1. **A Sleeper league is a read-only companion.** It yields analysis and
   lineup *recommendations* the user applies by hand in Sleeper. Fantrax stays
   the only platform RosterBot writes to. The `dry_run`/`AutoApply` apparatus is
   untouched, because there is nothing new for it to guard.

2. **`Membership` becomes the model now, with Sleeper as its first real
   citizen.** Paying the abstraction cost once, while the only alternative
   implementation is a projection, is much cheaper than retrofitting it after a
   second platform is already special-cased everywhere.

3. **`User.TeamID` stays authoritative for Fantrax.** A read-time accessor
   projects it into a `Membership`; `Memberships []Membership` on the record
   holds Sleeper entries only. Callers see one uniform list. There is no dual
   write, so there is nothing to drift.

4. **Sleeper data is fetched on demand and never persisted per league.** No new
   artifacts, no `league=` partition dimension, no scheduled jobs, no per-tenant
   compute multiplier. The client's existing `RosterTTL` cache absorbs repeats.

5. **Sleeper memberships are unverified and non-exclusive**, unlike Fantrax's
   `ErrTeamTaken`. See *Trust* below.

6. **The iOS app keeps Sleeper out of its five existing tabs.** See *Surfaces*.

7. **Fantrax remains one league per deployment.** `FANTRAX_LEAGUE_ID` is not
   de-globalised here. Decision 2 is what makes that a deferral rather than a
   dead end.

## Schema

```go
// Platform names a fantasy provider. Stored rather than inferred, because a
// capability decision that depends on a string comparison at each call site is
// a capability decision that a third platform will eventually get wrong.
type Platform string

const (
    PlatformFantrax Platform = "fantrax"
    PlatformSleeper Platform = "sleeper"
)

// Membership is one league a tenant belongs to, on any platform.
type Membership struct {
    Platform    Platform  `json:"platform"`
    LeagueID    string    `json:"league_id"`
    // TeamID is the Fantrax team id, or the Sleeper roster owner id. It is the
    // answer to "which of the teams in this league is yours", on both.
    TeamID      string    `json:"team_id,omitempty"`
    DisplayName string    `json:"display_name,omitempty"`
    // Writable is whether RosterBot can act on this league. False for every
    // Sleeper membership, permanently, because Sleeper has no write API.
    Writable    bool      `json:"writable"`
    AddedAt     time.Time `json:"added_at"`
}
```

On `User`:

```go
    // Memberships holds SLEEPER leagues only. The Fantrax membership is
    // projected from TeamID by AllMemberships rather than stored here, so the
    // proven-at-connect team and its uniqueness constraint keep exactly one
    // home. Two copies of the same fact is how they disagree.
    Memberships []Membership `json:"memberships,omitempty"`
```

And the accessor every caller uses:

```go
// AllMemberships returns the tenant's leagues across platforms, Fantrax first.
//
// It PROJECTS TeamID rather than reading a stored Fantrax membership. That is
// the whole trick: the API and the app get a uniform list, while the field the
// connect flow proves and the store enforces uniqueness on stays singular and
// untouched.
func (u *User) AllMemberships() []Membership
```

Migration: none. `Memberships` is absent on every existing record and decodes to
nil, which `AllMemberships` renders as "Fantrax only" — the correct answer for
every tenant that exists today.

## API

Two new methods on the existing client, matching the endpoints `rosterbot-z1g`
already identified:

```go
func (c *Client) UserByName(username string) (*User, error)          // GET /v1/user/<username>
func (c *Client) LeaguesForUser(userID, sport, season string) ([]League, error)
                                                                     // GET /v1/user/<id>/leagues/<sport>/<season>
```

Four routes, none added to `adminOnlyRoutes` — that list is an allowlist of
admin paths, so omission is what makes them member-reachable, and it is the
loud-failure direction the list was built for:

```
GET    /v1/sleeper/leagues?username=X&sport=nfl&season=YYYY   discovery proxy
GET    /v1/memberships                              the tenant's list
POST   /v1/memberships                              add a Sleeper league
DELETE /v1/memberships/{platform}/{leagueID}        remove one
```

Discovery is proxied server-side for two reasons: the client never needs to
learn Sleeper's API shape or its base URL, and the lookup goes through one
place that can be cached or rate-limited rather than N app installs hitting
Sleeper directly. Note the caching is weaker than it first appears — `serve`
disables it entirely (`New("")`) and Lambda's is per-execution-environment
`/tmp`, so a picker still reaches Sleeper across cold containers.

`sport` and `season` are explicit because Sleeper scopes leagues by both — a
username resolves to a different league set per sport per year. They default to
`nfl` and the current season, matching the only Sleeper usage in the tree today
(`internal/sleeper` is NFL-only: `PlayersNFL`, `NFLState`). Making them
parameters now costs nothing and is what lets a tenant add an NBA league later
without a route change.

`POST /v1/memberships` refuses `platform: "fantrax"`. Fantrax membership is
established by the invite and proven by connect; accepting one here would be a
route for a caller to assert the exact fact the connect task exists to prove.

`DELETE` on a Fantrax membership is likewise refused — removing it is an admin
tenant operation (`DELETE /v1/tenants/{id}`), not a self-service one.

## Trust

Sleeper memberships get **no verification and no uniqueness constraint**. This
is a deliberate departure from `ErrTeamTaken` and needs saying out loud, because
next to that invariant it otherwise reads as an oversight.

The justification is that the constraint would protect nothing. A Sleeper
username is public, and league/roster/user data is world-readable — claiming a
league you are not in grants access to data anyone could already fetch
unauthenticated. Meanwhile exclusivity would actively break the common case:
two testers in the same Sleeper league must both be able to add it.

The asymmetry with Fantrax is real and correct. There, the claim gates a stored
password and the ability to write to somebody's roster.

## Surfaces

**iOS.** A `Leagues` screen reached from Settings lists the tenant's
memberships, Fantrax first, with a Sleeper picker (username → league list →
select) and a read-only detail view per Sleeper league.

The five existing tabs are untouched, and that is a safety decision rather than
a scoping one. `LineupView` carries a one-tap Optimize that applies to a real
roster; `FunctionsListView` badges mutating jobs. Surfacing a Sleeper league
inside those means every write affordance needs a per-membership capability
check, and a user who sees Optimize beside their Sleeper lineup will reasonably
expect it to work. Keeping the read-only platform on its own surface means the
question never arises.

**Dashboard.** The same list and picker in `settings.js`, beside the existing
Fantrax card. Not required for the first cut.

## Testing

`baseURL` in `internal/sleeper/client.go` is already a package var, commented
"A var so tests can point it at an httptest server" — the seam exists.

- Go: table tests for `UserByName` and `LeaguesForUser` against an httptest
  stub, including the unknown-username 404; membership add/remove including both
  refusals above; and a test asserting no Sleeper membership ever reports
  `Writable`, since that field is what the app trusts.
- Go: `AllMemberships` on a record with nil `Memberships` returns exactly the
  projected Fantrax entry — the every-existing-tenant case.
- Swift: decode tests for `Membership`, `MockAPI` conformance, and a test that
  the Leagues screen offers no action for a read-only membership.
- Contract: re-run the `jobs.go` ↔ `API.md` ↔ `Models/*.swift` three-way read.
  Drift there is a runtime decode failure no compiler in either language
  catches, and it is fully checkable by reading all three.

## Sequencing

1. `Platform`, `Membership`, `AllMemberships` + tests. No behaviour change.
2. `UserByName` / `LeaguesForUser` + httptest tests.
3. The four routes + `API.md`.
4. iOS models and `LeaguesView` / picker.
5. iOS Sleeper league detail view.
6. Dashboard parity in `settings.js`.

1–3 are backend and independently shippable. 4–5 are the app. 6 is optional.

## Out of scope

No Sleeper push notifications — nothing runs on a schedule, so nothing can
detect a change to notify about. No history or trend analysis, for the same
reason. No `layout.go` changes. No de-globalising `FANTRAX_LEAGUE_ID`. Fantrax
stays one league per deployment.

The football pipeline's own `SLEEPER_LEAGUE_ID` env var is untouched; replacing
that with a picked league is the other half of `rosterbot-z1g` and remains open.

## Risks accepted

**No notifications is the feature people will ask for first.** Push is the thing
that makes the Fantrax side feel alive, and Sleeper leagues will feel inert next
to it. Accepted deliberately: adding it means persisting enough per-league state
to diff against, which is the layout migration this design exists to avoid. When
one surface earns it, that single artifact takes the `league=` dimension — not
all thirteen.

**On-demand means Sleeper's availability is ours.** A Sleeper outage renders
those screens empty where a persisted copy would have degraded gracefully. The
client's cache softens it. Judged acceptable for a read-only companion.

**`Writable` is denormalised.** A stored bool can in principle disagree with its
platform. Mitigated by it being set in exactly one constructor and asserted by
test, and chosen over the alternative — a string comparison repeated at every
call site that gates a write.

## Tracking

Epic plus children to be filed in the `rosterbot` tracker after this spec is
approved; the iOS-side issues live in the `rbapp-` tracker and will reference
these as plain text, since the two Dolt databases share no dependency edges.

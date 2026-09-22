# Sleeper multi-league pickups and pending offers — design

Date: 2026-09-22
Status: Approved by interview (2026-09-20..22); not yet decomposed into issues.
Extends `docs/dynasty-football.md` (the single-league, read-only football
spine) and revises one claim in `2026-08-21-sleeper-memberships-design.md`:
Sleeper *does* have a write path, undocumented, and this design builds the
seam for it without using it.

## Problem

Dynasty Nerds' lineup optimizer now submits lineup changes to Sleeper (App
Store v2.8, 2026-09-08: "Review and submit Sleeper lineup changes
individually or across multiple leagues"). The operator asked whether
rosterbot could extend to Sleeper in the same spirit, with two priorities that
matter more than lineups: **pickup opportunities** in dynasty leagues, and
**evaluating and alerting on trades**.

The football side today covers one league (`SLEEPER_LEAGUE_ID`), grades only
*completed* trades, and has no pickup signal at all.

## What the spike established

Every claim below was measured, not assumed. Sources are recorded in bd
memories `sleeper-write-path-spike-2026-09-20`,
`sleeper-pickup-signal-measured-2026-09-21`,
`sleeper-multi-league-format-signals-2026-09-21` and
`sleeper-graphql-schema-introspectable-2026-09-22`.

**How Dynasty Nerds writes.** Not through Sleeper's official partner channel.
The public Minis SDK (`blitzstudios/sleeper-mini-core`,
`declarations/sleeper_actions.d.ts`) exposes exactly `navigate`,
`requestLocation`, `showToast`, `scheduleNotification` and
`cancelNotification`: nothing mutates fantasy state. The inferred path is the
user's own Sleeper session against the undocumented GraphQL API, which is
what Sleeper's web app itself uses. A private partner deal is possible and
unprovable.

**The undocumented API.** `POST https://sleeper.com/graphql`, JSON body, a
`User-Agent` header mandatory (403 without one). Auth is the account-scoped
JWT the web app stores in local storage under `token`, sent raw in an
`authorization` header (no `Bearer`); it lasts about a year and grants full
account access. Schema introspection is **open without a token**, using
snake_case meta fields (`of_type`, `query_type`, not `ofType`). Roots:
`RootQueryType` (244 fields), `RootMutationType` (355). Pending offers come
from `league_transactions_by_status(status: String!, leg: Int!, league_id:
Snowflake!)`, returning `LeagueTransaction` with the same shape as the REST
transaction plus `creator` and `consenter_ids`. The query is token-gated for
**every** status, not only `pending`. A bad token answers HTTP 401; a gated
field without a token answers 200 with `errors[].code == "unauthorized"`.
There is a `me()` query. Mutations exist by name for `accept_trade`,
`reject_trade`, `propose_trade`, `submit_waiver_claim`, `update_waiver_claim`,
`cancel_waiver_claim` and `update_matchup_leg` (the real lineup write;
`roster_update_starters` succeeds, persists and changes nothing that scores).
The public REST API is Cloudflare-cached and can never confirm a write.

**Pending offers are invisible publicly.** The operator's dynasty league's
REST transaction feed returns only `complete` and `failed` rows, and five of
the six leagues have `trade_review_days: 0`, so a completed-trade alert is
always after the fact.

**Pickup signals differ by format.** Since 2026-08-25 the dynasty league made
30 adds; the ones that drew real FAAB were role changes (Saylors $100, Wentz
$35, Lock $35, TeSlaa $35, Singletary $29, Sanders $25) that StatsGuy priced
at 40-169 on a 10,000 scale with ~0 seven-day change. StatsGuy's value lags
the news. Sleeper's `/players/nfl` dump carries the signal: Lock and Wentz
both read `depth_chart_position = QB, depth_chart_order = 1`. A same-day scan
of unrostered skill players at order 1 returned three names. In the
guillotine league, Sleeper files eliminations as transaction type `chopped`;
the 2026-09-15 chop released Drake London (4156), A.J. Brown (3696), Tyler
Warren (2617) and Drake Maye (2496) in one event. The redraft league's real
drops reached 1360 (Mayfield); the deep dynasty league's topped out at 139. A
`trade` transaction lists its swapped players under `drops`, so a drop
detector that does not exclude trades reports every trade half as a release.
Sleeper's trending-adds feed is useless here: 42 of the top 50 were already
rostered and the other 8 were kickers and defenses the league has no slot for.

**The operator's leagues are heterogeneous.** Six 2026 leagues under user id
`738883211463155712`: two dynasty superflex (`settings.type` 2, one with a
5-slot taxi), one keeper with K and DEF (type 1), two redraft (type 0), one
18-team guillotine (type 3, FAAB 1000, `trade_review_days` 2). `settings.type`
is undocumented; superflex is derivable from `roster_positions` and is
reliable.

## Scope

In: **A** pending offers made to the operator, graded before they respond;
**B** completed trades across all the operator's leagues; pickup alerts from
public data; the token seam designed so writes can be added later.

Out, deliberately: **C** alerts on offers the operator sent; **D** a trade
finder (its own later project); extending the Dynasty Value Store to more
than one league; any write to Sleeper; per-tenant Sleeper tokens (this is the
operator's account only).

## Approach

Three jobs and one token boundary (Approach 1 of three considered). A single
`football-scan` job was rejected because the three cadences conflict (player
data once a day, offers hourly) and a token failure would fail the whole
scan. Replacing the public trade polling with authenticated reads was
rejected because it would make the stable alert depend on a yearly-expiring
token and an unsupported API for no gain.

```
SLEEPER_USER_ID ──► LeaguesForUser(nfl, season) ──► []League + LeagueProfile each
                                                          │
   football-trades   (public, 6-hourly)  ◄────────────────┤ completed trades, all leagues
   football-offers   (token,  hourly)    ◄────────────────┤ pending offers to me
   football-pickups  (public, daily)     ◄────────────────┘ role changes, drops, chops
```

## Section 1: the token boundary — `internal/sleeperauth`

A new leaf package holding the authenticated GraphQL client. It imports
`internal/sleeper` for the `Transaction` type only.

**Config.** One env var, `SLEEPER_TOKEN`. Locally from `.env`; on Fargate
injected only into the `football-offers` task from Secrets Manager through
the same `secret()` helper `infra/infra.go` already uses for
`SLEEPER_LEAGUE_ID`. The operator creates the secret value; nothing in the
implementation reads or prints it. `FromEnv()` returns `ErrNoToken` when the
var is unset, so the command fails fast with a plain message.

**Client.** `Query(ctx, doc string, vars map[string]any, out any) error`
posts JSON to `https://sleeper.com/graphql` with an explicit `User-Agent`
naming rosterbot and the token in `authorization`. **No disk cache**: the
read exists for freshness, the volume is a dozen calls an hour, and it
removes any chance of a token-derived cache key. Two typed methods in v1:
`Me(ctx) (userID string, err)` and `PendingTrades(ctx, leagueID string, leg
int) ([]sleeper.Transaction, error)`, the latter wrapping
`league_transactions_by_status(status: "pending")` and selecting the fields
`dynasty.BuildTradeSidesAll` consumes plus `creator`, `consenter_ids`,
`roster_ids`, `status`, `created`, `transaction_id`.

**Errors.** HTTP 401 and a 200 carrying `code: "unauthorized"` both map to
one sentinel, `ErrUnauthorized`. No error string may contain the token: a
test drives an `httptest` server that answers 401 and asserts the token never
appears in the returned error. Other GraphQL errors are joined into one
error carrying each `message`.

**The write seam, built later.** `Mutate` does not exist in v1. When added it
will take a `WriteAuthorization` value obtainable only through an explicit
opt-in (an `--apply`-style flag plus an env switch), mirroring
`lineuprun.Emit`'s `applyAuthorization`, so a write cannot happen by
statement order. Two tests pin the boundary now: no `internal/*` package
imports `sleeperauth`, and the only file under `cmd/` that does is
`cmd/football_offers.go`.

**Type change.** `sleeper.Transaction` gains `Creator string
json:"creator"` and `ConsenterIDs []int json:"consenter_ids"`, matching the
JSON both APIs return.

## Section 2: league discovery and multi-league `football-trades`

**Discovery.** New env var `SLEEPER_USER_ID`, the operator's account id,
stable where a username is not. `loadFootballConfig` reads it; both
multi-league jobs call `sleeper.Client.LeaguesForUser(ctx, userID, "nfl",
season)` with `season` from the NFL state call, and skip leagues whose
status is `complete`. `SLEEPER_LEAGUE_ID` stays for `football-values`.

**Per-league profile.** `dynasty.LeagueProfile{LeagueID, Name, Superflex,
Kind, Format}` is a pure derivation from `sleeper.League`: `Superflex` from
`roster_positions` containing `SUPER_FLEX`; `Kind` from `settings.type` with
the observed mapping `0 redraft, 1 keeper, 2 dynasty, 3 guillotine`
(`unknown` otherwise); `Format` is the StatsGuy column that grades the
league's trades and ranks its pickups, computed by `formatFor(kind,
superflex)`. **`formatFor` is the operator's contribution at implementation
time** (the keeper league is the ambiguous case). Because `settings.type` is
undocumented, an optional `SLEEPER_FORMAT_OVERRIDES` env var
(`<league_id>=<format>,...`) wins over the derivation, and every run prints
the column each league resolved to. `sleeper.League` gains whatever fields
the derivation needs (`roster_positions`, `status`, `settings`) if not
already present.

**The loop.** `football-trades` iterates the discovered leagues and runs its
existing per-league body unchanged (rosters, users, transactions for
`pollWeeks`, grade, alert, mark, log). Markers keep keying on the
transaction id, a global Sleeper snowflake. The alert title gains a league
prefix (`[Chopped] Trade: favors ...`), and the per-league `Format` replaces
`DYNASTY_FORMAT` as the headline format; `TradeLogRow.AlertFormat` records
it per row as it does now. The player dump and the StatsGuy bundle are
loaded once per run.

**Failure isolation.** One league's error is printed and the loop continues;
the run exits non-zero at the end if any league failed, so ops alerting sees
it while the healthy leagues' alerts still go out (`cmd/archive.go`'s
per-source pattern).

**Log and dashboard.** `TradeLogRow` gains `LeagueID` and `LeagueName`, both
`omitempty`. Rows written before this change stay blank and render as a
dash; the Football tab's trade table gains a league column. No backfill.

## Section 3: `football-offers`

**Job.** New command and hourly EventBridge schedule `FootballOffers`, the
only task that receives `SLEEPER_TOKEN`. Classified year-round in
`seasonPolicies` alongside the other football commands.

**Identity check first.** `Me()` must equal `SLEEPER_USER_ID` or the run
fails naming the mismatch, so a token from the wrong account, or a wrong
user id, is a loud failure rather than a job that runs green and never finds
an offer.

**Per league.** The operator's roster is the one whose `owner_id` equals
`SLEEPER_USER_ID`; a league with none is printed and skipped. Pending
transactions are queried for the current leg and the one before it. Keep
rows with `type == "trade"`, `status == "pending"`, the operator's roster in
`roster_ids`, and `creator != SLEEPER_USER_ID`.

**Grade and alert.** `BuildTradeSidesAll` and `GradeTrade` price all four
formats; the league profile's `Format` picks the headline. The alert is
written from the operator's side: assets received and given with values,
the verdict and column, and who has already accepted from `consenter_ids`.
A countered offer is a new transaction id and alerts as a new offer; an
accepted one surfaces through `football-trades` within six hours.

**Dedup.** One marker per transaction id under a new layout artifact
`FootballOffers` (`football/offers/` ↔ `.football/offers`): durable, no
`MaxAge`, absent from `All()`, on `FootballTrades`' reasoning. Order is
check → send → mark; a marker-store failure yields a duplicate, never
silence. `--dry-run` skips both the send and the mark. Delivery is
`notify.Send` with kind `transactions`; the title distinguishes an offer from
a trade. A dedicated kind is an iOS-visible change and is not bundled here.

**Failure policy.** `ErrUnauthorized` fails the run (token expired or
revoked); ops alerting escalates on the third consecutive failure, so the
operator hears within about three hours with no new plumbing. Any other
single-league error prints and continues, exiting non-zero at the end.

**Cost.** Twelve GraphQL calls an hour plus cached public reads.

## Section 4: `football-pickups`

**Job.** New command on a daily schedule at 15:15 UTC, after
`FootballValues` (14:45) has warmed the StatsGuy cache. Public data only; the
player dump is fetched once.

**Snapshot.** A filtered slice of the player dump — `id`, `name`, `team`,
`position`, `depth_chart_position`, `depth_chart_order`, `injury_status`,
`status` — for every player on an NFL team at a position any discovered
league rosters. Written through the archive store as source
`sleeper-players`, partition `dt=YYYY-MM-DD/players.json` (~300 KB/day),
via `archive.Writer` locally and `s3archive` on Fargate. The archive layout
is already `NoBackfill`, which is the right semantic: Sleeper keeps no
history. The Infra tab will show the source as a coverage chip; its gaps are
attributed to the archive producer name, an accepted mislabel.

**Diff.** Each run loads the most recent prior partition, not strictly
yesterday, so a missed day widens the window rather than losing the diff.
The first run writes a baseline, prints that it did, and alerts nothing.

**Three detectors**, each a pure function in `internal/dynasty` over
`(prev, cur snapshot, league transactions, league rosters, profile)`:
- **Role change.** A player unrostered in the league who reached
  `depth_chart_order == 1` from lower, from no recorded order, or from no
  team.
- **Valued drop.** A player dropped by a completed `waiver` or `free_agent`
  transaction created after the prior snapshot's date, priced in the
  league's `Format`, and still unrostered now. `trade` is excluded by type.
- **Chop.** The same for `chopped`, reported as a group: the eliminated
  roster and its priced players.

**Digest.** One per league, only when non-empty: chops, then drops by value
descending, then role changes with quarterbacks first in superflex leagues.
An unvalued role change is listed with its depth-chart fact, never hidden.
Sections fit Pushover's limit through `pushover.Builder`. **The ordering
rule between a valued drop and an unvalued role change is the operator's
second contribution.**

**Dedup and delivery.** A marker per event under `FootballPickups`
(`football/pickups/` ↔ `.football/pickups`; durable, no `MaxAge`, absent
from `All()`): role changes key on `(league, player, snapshot date)`; drops
and chops on `(league, transaction, player)`. Check → send → mark;
`--dry-run` does neither. Delivery is `notify.Send` with kind `waivers`.

**Coverage line.** Every run prints per league, unconditionally: prior
partition date, players compared, unrostered count, and each detector's
count including zeros.

**Deferred.** A standing "best available by value" section — measured at
132 of 10,000 in the dynasty league, it is noise there and needs
value-change dedup; the drop detector already catches the moment such a
player becomes available.

## Cross-cutting

- **Run ledger and ops alerting** come free from the existing command
  wiring; neither new job publishes a `jobwire` output document, matching
  `football-trades`.
- **`make run-all`** gains `football-pickups --dry-run` and, only when
  `SLEEPER_TOKEN` is set, `football-offers --dry-run`, printing a loud
  `SKIPPED: SLEEPER_TOKEN unset` line otherwise; a silent skip is the
  false-confidence failure the smoke target exists to catch.
- **Infra.** Two task definitions and two EventBridge schedules in
  `infra/infra.go`; `FootballPickups` gets the archive prefix write grant
  the `Archive` task has; `FootballOffers` alone gets the `SLEEPER_TOKEN`
  secret. `JOB_SCHEDULES` gains both.
- **Docs.** `docs/dynasty-football.md` gains sections for the three jobs and
  the token boundary; `README.md` gains the commands and env vars;
  `CLAUDE.md`'s Dynasty Football pointer is unchanged.
- **Verification without the token.** Every detector, the profile
  derivation, the grader path and the client's error mapping are hermetic.
  The one thing only the operator can run is the `diag`-tagged probe
  (`internal/sleeperauth/diag_pending_test.go`, `SLEEPER_TOKEN` from env,
  never printed) that records a real pending offer's shape and which leg it
  files under; it is the first implementation task and its output tunes the
  leg window in Section 3.

## Risks

- **Unsupported API.** Sleeper may change the schema or act against accounts
  that use it. v1 is read-only and low-volume under an honest User-Agent;
  each step up in capability raises the stakes and gets its own decision.
- **Token lifetime.** About a year, with no refresh path; expiry surfaces as
  a failing `football-offers` run within three hours.
- **`settings.type`.** Undocumented; the override env var and the printed
  column are the mitigation.
- **Depth-chart quality.** Sleeper's chart is editorial and can lag; the
  role-change detector reports what Sleeper says, and the digest names the
  source fact so the operator can judge it.

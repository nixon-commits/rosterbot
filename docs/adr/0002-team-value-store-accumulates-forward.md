# 2. The Team Value Store accumulates forward; no historical backfill

Date: 2026-07-12
Status: Accepted

## Context

We want a time plot of each fantasy team's aggregate HKB dynasty value on the
projection-site (`value.html`), broken out by MLB vs minors and hitter vs
pitcher, with a team selector. The value comes from joining each team's rostered
players (Fantrax `GetFullPlayerPool`, keyed by `FantasyTeamID`) to their HKB
`Value` by normalized name — the same `playername.Normalize` join `internal/claims`
and `internal/transactions` already use.

Two upstream facts constrain how the history can be assembled:

- **HKB serves only *current* values.** The rankings page (`harryknowsball.com`)
  exposes today's `Value` per player and a rolling 30-day delta, but no addressable
  history. The durable archive (`internal/archive`) snapshots the raw
  `rankings.html` daily, but only since 2026-07-02.
- **Fantrax rosters are never archived.** Nothing in the store records who was on
  which team on a past date; rosters churn daily via trades/waivers/call-ups.

So "Team X's value on June 1" is unrecoverable — we have neither the HKB values
nor the roster composition for arbitrary past dates. Any backfill would have to
assume today's roster held in the past (it didn't), producing counterfactual
points.

## Decision

The **Team Value Store accumulates forward**: the `team-values` command computes
today's per-team aggregate once per day and appends one date-partitioned NDJSON
record. The series begins the day the job first runs and grows one point per day.
There is **no backfill** — a thin early chart is accepted as honest, and the page
shows a "collecting data since <date>" note while sparse.

Supporting decisions:

- **Store shape mirrors the Analysis Store** (`internal/analysis`): date-partitioned
  NDJSON (`dt=YYYY-MM-DD/values.ndjson`), an isolated S3 adapter
  (`internal/teamvalue/s3teamvalue`) keeping the AWS SDK out of the leaf, and a
  glob-reader. Writes are one file per day (no read-modify-write), so a same-day
  re-run is idempotent (last write wins). No Athena table initially — the data is
  tiny (~12 rows/day) and always read wholesale to draw the plot.
- **Minors split = Fantrax `MinorsEligible`.** It is available league-wide in the
  single `GetFullPlayerPool` call we already make (zero extra API cost) and tracks
  farm/prospect status as a stable roster-construction attribute. The alternatives
  were rejected: literal minors-slot placement needs ~12 per-team roster fetches and
  flips daily as players shuttle; HKB's own Prospect/Level reflects where a player is
  *playing*, decoupled from how you've rostered them.
- **Hitter/pitcher split = `positions.IsPitcherSlot`** over the player's Fantrax
  eligibility IDs (the canonical helper). A two-way player (any pitcher eligibility,
  e.g. Ohtani) resolves to pitcher — a deterministic, documented tiebreak.
- **Team name + logo are denormalized into each row** (from
  `GetScoringPeriodsAndTeams`) so the read+render path (`projection-site`) needs no
  Fantrax call.

## Consequences

- The chart is empty on day one and fills in daily; this is a feature (every point
  is a real, dated snapshot), not a gap to paper over.
- Value totals **undercount by unmatched players** (rostered names with no HKB
  entry, e.g. deep streamers). Each row stores `RosteredCount` and `MatchedCount`
  so join coverage is visible on the page rather than silently trusted.
- If HKB ever exposes dated history, a one-time reconstruction is *still* blocked by
  the missing historical rosters — so this ADR should not be revisited on an HKB
  change alone.
- The store is queryable later (add an Athena table like `rosterbot_analysis.grades`)
  if aggregate auditing is ever wanted; the partition layout already supports it.

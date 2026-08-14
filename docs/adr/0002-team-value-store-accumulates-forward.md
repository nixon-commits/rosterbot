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
  glob-reader. (Both the adapter and the reader have since moved — see the
  Amendments below; the store shape itself is unchanged.) Writes are one file per
  day (no read-modify-write), so a same-day
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

## Amendments

**2026-07-27 (rosterbot-oye): "an isolated S3 adapter" now means an isolated
*Store shape*, not an isolated copy of the SDK plumbing.**

This ADR asked for a per-store S3 adapter so the AWS SDK stayed out of the leaf.
That goal is unchanged and still enforced — `internal/teamvalue` and
`internal/ndjsonstore` have no SDK dependency. What changed is where the adapter
lives and how much of it is this store's own:

- The Team Value Store never got its own `internal/teamvalue/s3teamvalue`. It
  shares `internal/ndjsonstore/s3ndjson` with the Analysis Store, since the two
  differ only in partition layout — which is domain code in each package, not
  adapter code.
- `s3ndjson` no longer drives the AWS SDK itself. It, `internal/cachestore/s3store`
  and the seven stores in `internal/lineupapi/s3lineup` are all thin shims over
  one shared **`internal/s3blob`** Blob, which owns credential resolution, prefix
  joining, not-found mapping, body reads and list pagination. Eight copies of that
  plumbing had drifted apart — only one of them paginated its listing correctly.

The **Store interfaces stay distinct** and must not be unified: `cache.Store` is a
delete-capable keyed TTL cache with no enumeration; `ndjsonstore.Store` is an
enumerable append-only partitioned log with no delete. Those shapes encode the real
lifecycle difference between the Cache and the durable stores that ADR-0001 and this
ADR rest on. Only the SDK plumbing beneath them is shared.

---

**2026-08-14 (rosterbot-psu): the first realised consequence — a league-wide
value discontinuity that cannot be restated.**

This ADR's whole point is that the series accumulates forward and cannot be
backfilled: HKB publishes no history and rosters are not archived, so every
partition written before a correction keeps its pre-correction value
permanently. On 2026-08-14 that stopped being hypothetical.

`rosterbot-5z7` (merged `a3f01a1`) fixed the HKB namesake join. The old
`BuildLookup` keyed players by `playername.Normalize` and assigned
unconditionally, so where two players collapsed to one key **the last row in
scrape order won**. 1752 HKB players collapse to 1748 keys; `luis garcia`
covers both Luis García Jr. (NYY, MLB, value 1142) and a rookie-ball namesake
(value 10).

The correction is visible in the store as a **step change on 2026-08-14**, and
it looks exactly like a trade or a waiver haul. It is not. Nothing in the data
distinguishes the two, which is why this is written down.

**Measured precisely, because both versions of that day's partition survive.**
The scheduled 14:30 UTC run wrote `dt=2026-08-14` with the pre-fix image; a
re-run at 20:59 UTC, 54 minutes after the 5z7 image finished building at
20:05 UTC, overwrote it. S3 object versioning is Enabled on the state bucket,
so the same day's before and after can be diffed directly — no cross-day
comparison, and therefore no confounding with a day of roster or HKB drift:

| team | pre-5z7 | post-5z7 | delta |
|---|---:|---:|---:|
| DillonP33 | 48827 | 49932 | **+1105** |
| jimmydyl | 63955 | 63751 | **−204** |
| Intentional Balk | 50545 | 50663 | +118 |
| Voradakis | 42638 | 42521 | −117 |
| Yordan's Schlong | 87021 | 87119 | +98 |
| Houston Swang and Bang | 82444 | 82513 | +69 |
| Pfaadt Wood Kings | 103368 | 103396 | +28 |
| BT95 | 55313 | 55296 | −17 |
| Finding Nimmo | 50379 | 50366 | −13 |
| BigZ4 | 33554 | 33544 | −10 |

Three things this record fixes about the original prediction:

- **Every team moved, not one.** rosterbot-psu expected DillonP33 to shift and
  named the rest as "the same, smaller". All ten moved, in **both directions**,
  spanning +1105 to −10. Any team-vs-team comparison that straddles
  2026-08-14 is comparing two different measurement regimes.
- **jimmydyl was predicted unchanged and moved −204.** The prediction noted
  that its Max Muncy resolved correctly before the fix "only by luck of scrape
  order". That is precisely what a scrape-order dependency means: the luck is
  re-rolled on every scrape, so "correct yesterday" was never a property of the
  data. A same-day diff catches this; a cross-day one would have attributed it
  to roster churn.
- **Join coverage reported perfect health throughout.** `matched_count` is
  unchanged across the fix for every team — DillonP33 sat at 49/49 before and
  after. A wrong match is worse than a miss in exactly this way: a miss is
  absent and renders as unknown, while a wrong match renders as a confident
  number *and counts toward coverage*. The instrument built to expose
  undercounting displayed 49/49 over a miscount.

**Consequence, stated plainly:** partitions `dt` ≤ 2026-08-13 carry
namesake-corrupted values for any team holding a player on a colliding key, and
there is no way to restate them. Read the series as two regimes joined at
2026-08-14, not as one continuous history.

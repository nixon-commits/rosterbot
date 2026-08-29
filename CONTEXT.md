# rosterbot

Domain glossary for the fantasy automation. Originally fantasy-baseball-only; the **Dynasty Football** section below covers the second sport (Sleeper + StatsGuy). Unqualified terms above it are baseball-specific (Fantrax); terms below it are football-specific (Sleeper) unless noted. Terms are added lazily as design decisions resolve them — this file is not exhaustive.

## Language

### Scoring

**Scoring Weights**:
The league's map of stat short-name → point value (e.g. `HR → 4`, `SO → -1`). The single source of how production converts to fantasy points. Lives in `internal/scoring` as `Weights`; `fantrax.ScoringWeights` is an alias.
_Avoid_: scoring settings, point values, rules.

**Stat Line**:
A neutral set of raw counting stats for one scope (a season projection or a single game), independent of where it came from. `HitterLine` / `PitcherLine` in `internal/scoring`. Adapters build a Stat Line from a `Projection` or an MLB game log; the scorer derives `1B`/`XBH`/`TB` from it and applies the Scoring Weights.
_Avoid_: stat map, box score, stat dict.

**Expected Points**:
The per-game fantasy-point value of a Stat Line: `ApplyHitter(line, w) / G`. The optimizer ranks players by Expected Points.
_Avoid_: projected points, FPG (use only in field names), value.

**Single-Game FPts**:
The fantasy points a player actually scored in one game — a Stat Line scored without per-game division. Used by the backtest/recap backfill, not the optimizer.
_Avoid_: daily points, game score.

### Positions

**Position ID**:
A Fantrax numeric string identifying a position or slot (e.g. `"001"` = C, `"008"` = INF, `"015"` = SP). The single source of their semantics is `internal/positions`, which fills the two IDs the upstream `auth_client` omits (`"003"` = 2B, `"008"` = INF).
_Avoid_: position code, slot code, pos number.

**Slot**:
One fillable spot in the active lineup, named by its league key (C, 1B, INF, OF, UT, SP, RP, P). A Slot has a Position ID; a player is eligible when their Position IDs satisfy the slot's acceptance rule (UT accepts any hitter; INF accepts 1B/2B/3B/SS).
_Avoid_: roster spot, lineup position.

**Eligibility Bucket**:
A reporting grouping a hitter falls into by eligibility precedence C > INF > OF > UT (the scarcest defensive role wins); pitchers bucket by role (SP/RP). Used by the backtest's per-position accuracy table.
_Avoid_: position group, category.

### Periods

Fantrax exposes two unrelated numbering schemes that both call themselves a
"scoring period". They are distinct Go types (`DailyPeriod` / `WeeklyPeriod`),
so crossing them is a compile error. Three incidents — rosterbot-uv6,
rosterbot-z3b, rosterbot-48z — were all "wrong axis or wrong resolver".

**Daily Period**:
The number Fantrax keys roster snapshots, lineup applies and GS snapshots by. Advances **exactly one per calendar day** — 2026-03-25 is 1, 2026-09-27 is 187. Despite the folklore, this axis has never merged or inserted a period: verified 2026-07-25 against the full-season periodList map, it is strictly 1:1 with the calendar. `DailyPeriod` in `internal/fantrax`.
_Avoid_: scoring period (ambiguous), period number, roster period.

**Weekly Period**:
The number Fantrax's standings and matchups are organised by — one H2H matchup per period, ~7 days, and the axis that genuinely **does** merge: weekly period 16 spans 2026-07-13..07-26 across the All-Star break, and period 1 spans a 12-day opener. Used for GS limits (`GetGSLimits`) and standings questions. `WeeklyPeriod` in `internal/fantrax`; carried by `ScoringPeriod`.
_Avoid_: matchup number, week number, scoring period.

**Period Map**:
The authoritative date → Daily Period mapping, parsed from `getTeamRosterInfo`'s `periodList` dropdown (`"104 (Mon Jul 6)"` — the label for period N *is* the date whose snapshot lives at N, so it self-corrects if Fantrax ever does insert one). Covers the whole season, cached season-stable plus in-memory memoised. `fantrax.DailyPeriodFor(seasonStart, date)` is the **single** entry point to the daily axis; the Period Map is its first tier and season-start day math its only fallback. Resolution depends on nothing but `(seasonStart, date)` — notably not on "today" or on Fantrax's reported current period, which lagged a day on 2026-07-16 and caused a silent apply failure.
_Avoid_: period resolver, period lookup, anchor.

**Matchup Week**:
The inclusive calendar-date span of one H2H matchup, derived per-team by grouping consecutive same-opponent entries from `GetAllMatchups` (`MatchupWeekBounds`). Measured 2026-07-25, this agrees with the Weekly Period list **exactly** — same count, same bounds, same numbering, including both irregular weeks — so it is the *same concept reached by a second route*, not a rival axis. Prefer the Weekly Period list when you need Fantrax's own number (GS limits); prefer Matchup Week when you need date bounds without a standings fetch. If they ever disagree, the standings caption is authoritative.
_Avoid_: matchup period, week bounds, fantasy week.

### Schedule

**Confirmed Starter**:
A pitcher MLB's probables feed names as starting *for his own club* on a date — the only start the optimizer values at full weight and the GS forecast counts exactly. Classified by `playername.MatchProbable(name, club, probables)`, the single shared join (NotProbable / ConfirmedStarter / TeamMismatch); a TeamMismatch — name announced, club disagrees — signals a lagging trade and is surfaced by the IL-start check rather than dropped.
_Avoid_: probable (the feed lists probables; confirmation is the club-equality check), starter flag, is-starting.

### Statcast

**Statcast Bundle**:
The joined set of Baseball Savant expected-stats and quality-of-contact slices for one day, keyed by MLBAM ID — season wOBA/xwOBA/barrel/hard-hit for hitters, season ERA/xERA/xwOBA for pitchers, plus a 14-day hitter window and a 30-day pitcher window. `statcast.Bundle` in `internal/statcast`, loaded by `LoadBundle` (cached 24 h, matching the Savant recompute cadence). The raw CSVs are the source; the Bundle is the in-memory join every consumer reads. Lives in its own leaf so `waivers`, `claims`, and `recap` depend on the data — not on each other's command package.
_Avoid_: savant data, statcast blob, CSV bundle.

**Statcast Signal**:
A classification of why a player is worth surfacing, derived from a Statcast Bundle against tunable Thresholds: BUY-LOW (expected stats outrun surface stats), HOT (recent production backed by quality contact), BOTH, or None. `statcast.Signal` plus `TagHitter`/`TagPitcher`, which return the Signal and a `SignalMetrics` carrying the facts behind it. Consumed by the waivers report and the claims recap; independent of any command.
_Avoid_: tag, flag, waiver signal, alert.

### Storage

**Cache**:
Ephemeral, TTL-evicted, regenerable upstream data behind `cache.FileCache[T]` — FanGraphs projections, Fantrax rosters, MLB schedules, Savant CSVs. Safe to wipe anytime; a miss just re-fetches. Distinct from durable history (see _Analysis Store_, not yet built).
_Avoid_: store (the bytes layer is the Store), datastore, persistence.

**Store**:
The storage seam behind the Cache: a byte get/put/remove-by-key interface. `FileCache[T]` keeps the deep behaviour (TTL, envelope, stale-fallback, Notify) and delegates raw bytes to a Store adapter — `fsStore` (local `.cache/`), `s3Store` (S3 `cache/` prefix, in its own package so the AWS SDK stays out of the leaf), `memStore` (tests). Selected by `cmd` from config; `fantrax.Client` holds the interface, not an adapter.
_Avoid_: backend, driver, provider, repository.

**Analysis Store**:
Durable, append-only, date-partitioned history of model performance in S3, queried by Athena (SQL) — the opposite lifecycle to the Cache (never TTL-evicted). Holds Graded Snapshots as NDJSON under `analysis/grades/dt=YYYY-MM-DD/`. Written by the daily `grade` command; read by ad-hoc SQL for model auditing (accuracy trends by position/role/week). Athena table is CDK-managed with partition projection on `dt` (no crawler).
_Avoid_: warehouse, archive, history DB, datalake.

**Graded Snapshot**:
The materialized fact behind the Analysis Store: one row per (date, player) pairing the projected Expected Points with the actual Single-Game FPts and their signed error, plus dimensions — Eligibility Bucket, role, was-started. Computed by reusing `internal/backtest`'s projection grading. The grain model-audit queries aggregate.
_Avoid_: grade row, result, scorecard.

**Team Value Store**:
Durable, date-partitioned NDJSON of each fantasy team's aggregate HKB dynasty value, one row per (date, team), under `analysis/team-values/dt=YYYY-MM-DD/`. Written daily by `team-values`. Unlike every other durable store here it **accumulates forward and cannot be backfilled** — HKB publishes no history and rosters are not archived, so a missed or wrong day is permanently missed or wrong (`NoBackfill: true`; see `docs/adr/0002`).
_Avoid_: value history, dynasty index, standings store.

**Namesake Re-baseline (2026-08-14)**:
The one known discontinuity in the Team Value Store. `rosterbot-5z7` fixed the HKB namesake join, which had been resolving colliding normalized names by scrape order — so partitions `dt` ≤ 2026-08-13 carry namesake-corrupted values and cannot be restated. **All ten teams shifted, in both directions** (+1105 to −10), measured from the two surviving S3 object versions of that day's own partition. It reads exactly like a trade or waiver haul and nothing in the data distinguishes it, so a team-vs-team comparison straddling this date is comparing two measurement regimes. Full table and reasoning in `docs/adr/0002`'s 2026-08-14 amendment.
_Avoid_: the HKB bug, the Garcia fix (it was never one team).

**Alert Marker**:
The durable dedup record behind a one-shot operational alert, and the discipline that governs it: check → send → mark (never claim-then-send), every marker-store failure degrading to a duplicate alert and never to silence, a nil store disabling dedup rather than the alert, and dry-run skipping both send and mark. The discipline lives once in `internal/alertmarker` (a stdlib-only leaf; `lineupapi.BlobStore` satisfies its `Store` structurally); what stays per-site is the key grammar — which facts identify "this alert" (player+date, season+period, cache key+episode, transaction id) — and the dry-run guard. Sites: the stale-cache, IL-start, GS-floor and football-trade alerts, plus the `opsnotify` Lambda's task/heartbeat/drift/alarm markers under `layout.OpsAlertMarkers`.
_Avoid_: dedup flag, sent-file, alert cache (it is durable state, not a Cache — a marker is never TTL-evicted while its condition stands).

## Dynasty Football

Sleeper (league/roster state, public read-only API, no key) + StatsGuy (dynasty valuations, `api.statsguyfantasy.com`, no key). Read-only throughout: Sleeper has no lineup-write endpoint, so the optimize-and-apply spine (Slot, Weekly Period, ApplyLineup, …) has no football counterpart — football's two commands are a value store and a trade monitor, not an optimizer.

**Asset**:
A player or a draft pick — the two things a Sleeper roster or trade can hold. `dynasty.Row.AssetType` is `"player"` or `"pick"`; `dynasty.TradeAsset` is the same distinction inside one trade. A pick's identity is `(year, round)`, priced generically (see _Pick Price_) since it has no draft-slot standing yet.
_Avoid_: item, entry, piece.

**Dynasty Value**:
An Asset's StatsGuy value in one of four formats (`sf_dynasty` / `non_sf_dynasty` / `sf_redraft` / `non_sf_redraft`). Every `dynasty.Row` carries all four leaves — not just the league's configured format — so the dashboard derives the format toggle client-side without re-aggregating, the same pattern `teamvalue.Row` uses for hitter/pitcher × MLB/minors. `DYNASTY_FORMAT` (default `sf_dynasty`) picks which leaf a command's own summary/headline reads.
_Avoid_: player value, score, rating.

**Starters Value**:
The Dynasty Value of a roster's starting lineup only (`Row.IsStarter == true`), as opposed to the full-roster total. **The headline number**, not the full-roster total — the 2026-08-10 coverage-gate spike measured starters coverage near-100% across the league (StatsGuy's ~13% player universe still covers nearly every real starter) against a full-roster coverage spread of 21.1pp (best 100.0%, worst 78.9%) that fails a 10pp target. Reporting the full-roster total as-is would silently understate rosters with more unvalued bench/depth players, without saying so — the same shape of failure as rosterbot-hx5 (a metric that can only emit one value on the data that matters isn't a measurement). The full-roster total still ships, but visually demoted and always beside its matched/rostered coverage counts. See bd memory `dynasty-football-coverage-gate-2026-08-10`.
_Avoid_: total value, roster value (ambiguous between starters and full roster — say which).

**Coverage Spread**:
The gap between the best and worst team's full-roster StatsGuy join-coverage percentage (`matched / rostered`) across the league — the metric the 2026-08-10 gate measured (21.1pp) to decide whether the full-roster total was trustworthy enough to headline. Distinct from a single team's *Coverage* (`dynasty.Coverage`: one team's `RosteredCount`/`MatchedCount`/`Unmatched`) — Spread is the league-wide range across every team's coverage, not one team's count.
_Avoid_: match rate, join rate (say *which* team's, or say Coverage Spread if it's the league-wide range).

**Pick Price**:
An unresolved future draft pick's generic per-round value: StatsGuy's `"mid"` variant tier for that `(year, round)`, ignoring the `"early"`/`"late"` tiers and the resolved-slot entries (`"2027 1.01"`) a settled draft order would use instead. There is no team standing yet to prefer `"early"` or `"late"` over `"mid"`, so every unresolved pick — in the Dynasty Value Store and in a trade — prices the same way (`dynasty.MidVariantPrice`, one function, so the store and a trade alert can never disagree). Every priced pick is flagged `Estimated: true` for this reason.
_Avoid_: pick value (say Pick Price when specifically the mid-tier estimate), draft value.

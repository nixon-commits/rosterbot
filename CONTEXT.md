# rosterbot

Domain glossary for the fantasy-baseball automation. Terms are added lazily as design decisions resolve them — this file is not exhaustive.

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

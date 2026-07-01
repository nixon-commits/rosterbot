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

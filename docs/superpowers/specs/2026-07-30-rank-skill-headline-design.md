# Rank-skill headline for the projection dashboard

**Issue:** rosterbot-aei
**Date:** 2026-07-30
**Status:** Approved, ready for implementation planning

## Problem

The projection dashboard leads with MAE, reported to three decimals, as its
scoreboard. Measured over the 850 production grade rows (`LEGACY` +
`depthcharts-ros`), MAE is near-blind to the model's actual skill:

| Metric | Value |
|---|---|
| Hitter projection MAE | 4.380 |
| MAE of a constant projection at the sample mean | 4.356 |
| MAE of the (MAE-minimising) sample median | 4.132 |
| Skill vs constant-mean baseline | **-0.5%** |
| Skill vs median baseline | **-6.0%** |

The cause is a variance mismatch: hitter projections barely vary
(`sd(proj) = 0.66`) against enormous daily outcome variance
(`sd(actual) = 5.52`). MAE at player-day grain is therefore almost entirely
irreducible single-game noise. It measures baseball, not the model.

The model does have real skill, but it is **rank** skill — which is exactly and
only what a lineup optimiser consumes. Within-day Spearman correlation between
projected and actual:

| Role | mean rho | se | t | days positive |
|---|---|---|---|---|
| Hitters | +0.1218 | 0.0373 | +3.26 | 30/40 |
| Pitchers | +0.4292 | 0.0982 | +4.37 | 18/23 |

Both independently reproduced.

## Goal

The dashboard leads with decision-relevant metrics:

1. **Mean within-day Spearman rho by role** — does the model order a day's
   roster correctly?
2. **Realized-vs-hindsight-optimal lineup gap** — the only metric denominated
   in points the league actually awards.

MAE remains available as a calibration diagnostic, not the headline.

## Non-goals

- Changing any projection model, blending weight, or optimiser behaviour. This
  changes measurement and presentation only.
- Backfilling lineup-gap history before the store's first write.
- Replacing the Athena-queryable `rosterbot_analysis.grades` table or its
  `GradeRow` shape.

---

## 1. Rank skill (`internal/report`)

New file `internal/report/rank.go`.

```go
// RhoStat is within-day rank skill for one role: the mean of per-day Spearman
// correlations between projected and actual, with the spread across days that
// says whether the mean is real.
type RhoStat struct {
    Rho          float64 `json:"rho"`          // mean of per-day rho
    SE           float64 `json:"se"`           // sd(daily rho) / sqrt(days)
    Days         int     `json:"days"`         // days meeting minDayRows
    DaysPositive int     `json:"daysPositive"`
}
```

Three load-bearing implementation constraints:

### 1.1 Tie-corrected Spearman is mandatory

The textbook shortcut `1 - 6*sum(d^2)/(n(n^2-1))` is valid only when there are
no ties. Graded rows are full of ties: every player who did not play sits at
`actual = 0`, frequently a third of the day's rows.

`rank.go` therefore assigns **average ranks within tie blocks** and computes
Pearson correlation over those ranks. Using the shortcut would silently bias
every number on the new headline.

A day whose projected values are all tied, or whose actual values are all tied,
has undefined rho (zero variance in one rank vector). Such days are **excluded**
from the mean, not counted as zero.

### 1.2 Minimum day size

**`minDayRows = 5` was the original proposal in this section and it was
wrong.** It was invented, not measured — flagged as such at the time — and it
failed the §4.1 reproduction gate on the pitcher role during implementation
(task 9): most graded pitcher-days carry only 3-4 rows, so a 5-row floor
discards 15 of the 23 usable pitcher-days, collapsing the sample to 8 days and
inflating rho to +0.53 (se .106) instead of reproducing the +0.4292 measured
during triage.

A threshold sweep against the same 850 production rows, run to resolve the
gate failure, pins the correct value:

| threshold | hitters | pitchers |
|---|---|---|
| 1-2 | +0.0944 se .0456 t+2.07  41d | +0.4647 se .1063 t+4.37  32d |
| **3** | **+0.1218 se .0373 t+3.26  40d** | **+0.4292 se .0982 t+4.37  23d** |
| 4 | +0.1218 se .0373 t+3.26  40d | +0.4929 se .0947 t+5.20  18d |
| 5 (original proposal) | +0.1218 se .0373 t+3.26  40d | +0.5299 se .1062 t+4.99   8d |
| 6 | +0.1196 se .0382 t+3.13  39d | +0.8143 se .0429 t+19.0   2d |

**`minDayRows = 3`** is the calibrated floor: it is the only value that
reproduces both roles' triage statistics exactly, and the shape of the curve
around it explains why — below 3, thin 1-2-row days enter the hitter mean and
inject pure noise; above 3, the pitcher sample collapses toward whichever
handful of days happened to carry more rows, turning the mean into a
near-single-day statistic (rho +0.81 at t=+19 on just 2 days by threshold 6)
rather than a measurement of skill. Days with fewer than `minDayRows = 3` rows
in the role are dropped from the mean. `RhoStat.Days` reports how many days
survived, so a thin window reads as thin rather than as confident.

### 1.3 Never pooled across roles

`withinDayRho` is only ever called on a role-filtered slice. Pooling hitters and
pitchers would let a system that merely knows "today's starting pitcher
outscores a bench catcher" post a high rho while carrying zero within-role
skill — and the optimiser runs hitters and pitchers in separate passes, so
cross-role ordering is information it never uses.

`View.Rho` is a `*RhoStat` and is **nil** when `role == "all"`. The constraint is
enforced by the type, not by a comment. The UI reads the hitters and pitchers
views for its two tiles.

### 1.4 What does not change

`Metrics` (MAE / Bias / RMSE / N) keeps its exact current meaning and its
existing tests. `computeMetrics` is not restructured. Rho attaches alongside on
`View` and on `SystemScore`, so the prior-window delta and the head-to-head
comparison inherit it without a signature change.

### 1.5 Skill score on MAE

`Metrics` gains one field:

```go
SkillVsMean float64 `json:"skillVsMean"` // 1 - MAE_model / MAE_constantAtSampleMean
```

Computed inside `computeMetrics` from the same row slice: take the mean of
`Actual` over the slice, compute the MAE of that constant against every
`Actual`, and express the model's MAE as a skill score against it. Zero rows or
a zero baseline yields 0.

This converts the demotion from an assertion in a commit message into a fact
the dashboard recomputes daily. If a future system ever posts genuine positive
skill there, it becomes visible rather than needing re-derivation by hand.

### 1.6 Ranking change

`rankSystems` orders by `Rho` **descending** (was: MAE ascending). MAE, Bias and
RMSE remain columns in the table but stop deciding the winner.

`Best` is flagged only when the leader's rho exceeds the runner-up's by more
than the combined standard error:

```
rho_lead - rho_next > sqrt(se_lead^2 + se_next^2)
```

Otherwise no system is flagged and the panel reads **too close to call**.
Systems with no rows in the window continue to sort last and are never `Best`.

`SystemScore.Rho` is a `*RhoStat`, nil under the same condition as `View.Rho`.

Under `role == "all"` a pooled ranking has no defensible order, so
`Model.Compare[viewKey(w, "all")]` is left **nil** — the same
enforced-by-the-type treatment as `View.Rho`. The comparison panel under "All"
reads `Compare[w|hitters]` and `Compare[w|pitchers]` and renders **two stacked
tables**, each ranked by its own rho. `Model.CompareTrends` is unchanged and
still populated for all three roles, since a trend of MAE over time remains a
legitimate diagnostic under any role filter.

---

## 2. The Lineup Gap Store (`internal/lineupgap`)

A new durable, date-partitioned NDJSON store — one row per team-day.

```go
// Row is one day's lineup decision graded against hindsight.
type Row struct {
    Dt         string  `json:"dt"`
    ActualPts  float64 `json:"actual_pts"`
    OptimalPts float64 `json:"optimal_pts"`
    Gap        float64 `json:"gap"`       // actual - optimal; negative = left on bench
    StartedN   int     `json:"started_n"`
    BenchedN   int     `json:"benched_n"` // scored but not started
}
```

`Efficiency()` (`ActualPts / OptimalPts`) is a derived method, not a stored
field — the same call `teamvalue.Row` makes with `TotalValue()`.

### 2.1 Package shape

`internal/lineupgap` owns `Row`, `Writer`/`Reader` over `internal/ndjsonstore`,
**and** `BuildModel`. This follows `internal/recaplog` (store seam plus pure
model builder in one leaf) rather than the `teamvalue`/`valuereport` split,
because the model here is a few dozen lines of windowed sums rather than
`valuereport`'s chart shaping.

### 2.2 No system dimension

Exactly one lineup was applied each day, and hindsight-optimal does not depend
on a projection system. The partition key is `dt=` only, with the entity name in
the prefix — structurally identical to `teamvalue`. This is why
`internal/analysis`'s system-partition machinery is not reused, and why the
store is not folded into `internal/analysis` as a second row type: that package
is deliberately the store of one fact, and the Athena table
`rosterbot_analysis.grades` rests on that.

Consequence worth stating plainly: **rho answers "which projection system",
the gap answers "how much is the whole pipeline costing us".** They are
complementary, not comparable. The gap cannot discriminate between systems.

### 2.3 Producer

`cmd/grade.go`. It already holds `days` (the `ft.DailyFantasyPoints` result at
`grade.go:65`) and a live Fantrax client, so the addition is:

1. `ft.GetHitterSlots()` / `ft.GetPitcherSlots()` — both `stableTTL`-cached
   (7d), so one cold fetch per week.
2. `backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)` — already
   exists, unchanged.
3. Map `[]backtest.LineupDayResult` to `[]lineupgap.Row` and write one partition
   per date.

Zero new upstream calls on a warm cache.

### 2.4 Failure policy: soft

A gap-write failure logs a warning and returns nil. It must never fail the
`grade` run: grades are the irreplaceable artifact, and the gap is recomputable
from immutable past-period snapshots.

### 2.5 Layout and wiring

```go
LineupGaps = Artifact{
    Name: "Lineup Gap Store", S3Prefix: "analysis/lineup-gaps/",
    LocalDir: ".lineupgap", Durable: true, MaxAge: 2 * Day,
    Producer: "Grade", Partitioned: true,
}
```

Added to `layout.All()`. Plus `LineupGapWriter()` / `LineupGapReader()` on
`statestore.Selector`, both one-liners through the existing generic `pick` and
`s3ndjson.New`.

**`NoBackfill: false`**, unlike Team Values. This is a real distinction rather
than a copy-paste default: past-period roster snapshots are immutable and cached
at `pastPeriodTTL`, so a missed day is recoverable with `grade --dates`. The
Infra tab's existing `NoBackfill` logic therefore ranks a gap here below a Team
Value gap for free.

**No `cmd/sync.go` change.** `statePairs` covers only bulk dir syncs;
`.analysis` and `.teamvalue` are absent because typed per-key stores handle
them, and `.lineupgap` is the same.

---

## 3. Dashboard

### 3.1 `gap.json`

`cmd/projection-site.go` gains `renderGapSite(outDir)` alongside
`renderValueSite` and `renderViewsSite`: its own reader, its own file, and
**soft-failing independently** so a gap hiccup never blocks the accuracy
dashboard deploy.

```go
type Model struct {
    GeneratedAt string
    LatestDate  string
    Windows     map[string]WindowSummary // keyed "7","14","30","0" — matches report's stdWindows
    Series      []Point                  // daily, ascending
}

type WindowSummary struct {
    Days                          int
    ActualPts, OptimalPts, GapPts float64
    Efficiency                    float64
}

// Point is one day on the gap chart.
type Point struct {
    Dt         string  `json:"dt"`
    GapPts     float64 `json:"gapPts"`
    ActualPts  float64 `json:"actualPts"`
    OptimalPts float64 `json:"optimalPts"`
}
```

`Windows` is keyed by the decimal string of each `report.stdWindows` entry
(`"7"`, `"14"`, `"30"`, `"0"`), so the existing window toggle indexes it
directly with no mapping table. `"0"` is the season window, matching
`report.windowLabel`'s convention.

No precomputed "worst day": the UI takes a max over `Series`, which is
reshaping, not recomputing.

### 3.2 The headline block

A **Decision quality** block at the top of the Projections tab, driven by the
existing window toggle. The role toggle does not apply to the gap — there is one
lineup.

- **Points left on bench** — e.g. `-48.3 pts` / *over 30 d*
- **Lineup efficiency** — e.g. `96.4%`
- **Hitter rank skill** — e.g. `rho +0.122 +/-0.037 - 30/40 days positive`
- **Pitcher rank skill** — e.g. `rho +0.429 +/-0.098 - 18/23 days positive`
- A daily gap chart beneath.

### 3.3 Empty state

The gap block is now the first thing on the page, and `gap.json` will
legitimately 404 on local dev and on the first deploy before `grade` next runs.
In that case the block renders the rho tiles alone plus a muted "no lineup gap
data yet" — never a broken headline.

### 3.4 MAE demotion

The existing scorecard stays in place below the headline, relabelled
**Calibration diagnostics**, carrying `SkillVsMean` as a subtitle on the MAE
tile (`-0.5% vs constant baseline`) and one line of standing explanation of why
MAE is not the headline. The explanation exists so MAE does not quietly get
re-promoted in a later change.

---

## 4. Testing and verification

Everything new is pure and testable without credentials.

- **`rank_test.go`** — Spearman against hand-computed **tied-rank** cases. A
  no-ties-only test would pass with the broken shortcut, so tied cases are the
  point. Plus: perfect concordance yields +1, perfect inversion yields -1, an
  all-tied day is excluded rather than NaN, sub-`minDayRows` days are dropped,
  and `View.Rho` is nil for `role == "all"`.
- **`rankSystems`** — a leader within the combined standard error produces no
  `Best` flag.
- **`SkillVsMean`** — a constant-projection input scores 0; a strictly better
  model scores positive; an empty slice scores 0 without dividing by zero.
- **`lineupgap`** — round-trip through `ndjsonstore.NewMemStore()`, plus
  `BuildModel` window math, matching `teamvalue`'s existing test shape.
- **`cmd/grade.go`** — the gap-write failure path returns nil and still writes
  grades.
- **End-to-end** — `make run-all` (which already exercises `grade --dry-run`),
  then `go vet ./...` and `go mod tidy`.

### 4.1 Reproduction gate

Against the 850 production grade rows, the implementation must produce:

| Role | mean rho | se | days |
|---|---|---|---|
| Hitters | ~ +0.1218 | ~ 0.0373 | 40 |
| Pitchers | ~ +0.4292 | ~ 0.0982 | 23 |

If it does not, the implementation is wrong — most likely the tie correction,
an unfiltered/pooled slice, or (as actually happened — see §1.2) a
`minDayRows` that doesn't match this data's per-day density. These figures
were independently reproduced twice during triage, so they are a trustworthy
fixed point, and this gate is what makes the whole feature believable.

**This gate is not a formality — it caught a real defect.** The implementation
first shipped with `minDayRows = 5` (§1.2's original, unmeasured proposal) and
the gate correctly failed on pitchers: 8 qualifying days at rho +0.53 instead
of 23 days at +0.4292. The day count is as much a pass/fail criterion as the
rho value — a rho that lands in a plausible range from a materially different
day count is not a pass, it's a different (and less trustworthy) measurement
that happens to be numerically close. Recalibrating to `minDayRows = 3` (the
sweep in §1.2) reproduced all four statistics for both roles exactly.

---

## Files touched

**New**

- `internal/report/rank.go`, `internal/report/rank_test.go`
- `internal/lineupgap/` (row + store + `BuildModel` + tests)

**Modified**

- `internal/report/model.go` — `View.Rho`, `SystemScore.Rho`
- `internal/report/aggregate.go` — `Metrics.SkillVsMean`, `rankSystems` ordering
- `internal/statestore/layout/layout.go` — `LineupGaps` artifact, `All()`
- `internal/statestore/statestore.go` — `LineupGapWriter` / `LineupGapReader`
- `cmd/grade.go` — compute and write gap rows (soft-fail)
- `cmd/projection-site.go` — `renderGapSite`
- `web/dashboard/projections.js` — headline block, ranking by rho, MAE demotion
- `web/dashboard/api.js` — `gap.json` fetch
- `CLAUDE.md`, `README.md` — new store and changed headline semantics

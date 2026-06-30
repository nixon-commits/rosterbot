# Projection Accuracy Dashboard (`projection-site`) — Design

**Date:** 2026-06-29
**Status:** Approved (design); pending spec review → implementation plan

## Goal

A daily-updating, single-page interactive HTML site that visualizes how well the
**production projection system** predicts actual fantasy points over time. It
turns the existing Graded Snapshots (`analysis/grades/`) into an ongoing
"how is my projection model doing, and where can it improve?" dashboard — the
projection analog of the weekly recap site, but ongoing (daily) rather than
per-week.

### In scope (v1)

- Read the existing Graded Snapshots (projected vs actual per player per day).
- Visualize accuracy over selectable timeframes, by position, and by role.
- Surface where the model systematically over/under-projects (calibration).
- Auto-generate plain-language insight callouts from the aggregates.
- Deploy on AWS exactly like the recap site (S3 + CloudFront), updated daily.

### Explicitly out of scope (v1) → v2 follow-on

Comparing **alternative projection systems** (Steamer / TheBatX / DepthCharts)
or **alternative blending weights** head-to-head. The grades store only records
**one** projected value per player per day — the production blend — so there is
no historical record of what an alternative system *would* have projected.
Answering "how could other systems / different weighting improve things" needs a
new daily **multi-system capture pipeline** that must accumulate weeks of data
before any comparison is meaningful. Filed as a separate PRD (see §8).

## Architecture & data flow

```
analysis/grades/dt=YYYY-MM-DD/grades.ndjson   (S3 on AWS, .analysis/ locally)
        │  analysis.Reader  (new; S3 adapter in s3grades, mirrors Writer)
        ▼
internal/report  ── Aggregate ──►  compact per-day building blocks (JSON)
        │                          + GenerateInsights (rule-based)
        ▼
   template.html   (embeds JSON; vanilla JS re-aggregates client-side;
        │           charts via a small CDN charting lib)
        ▼
   ./report/index.html  ── entrypoint.sh sync ──►  REPORT_BUCKET ──► CloudFront
```

The report is read-only over the durable Analysis Store. It does **not** touch
Fantrax or any live upstream — it depends only on grades already written by the
daily `grade` command. This keeps it deterministic and cheap.

### Why read NDJSON directly (not Athena)

The dataset is tiny (~tens of MB for a full season of small NDJSON files). A
direct S3 list + parse reuses the existing S3 store seam, has zero query
cost/latency, is deterministic, and is trivial to unit-test against a local
temp dir. Athena remains available for ad-hoc model auditing; the daily site
does not need it.

## Embedded data shape (enables client-side timeframe toggling)

Go pre-aggregates grades into **per-day building blocks**. Because sums compose
across days, the client can compute any timeframe (7 / 14 / 30 / season) by
summing the relevant day slice — no server round-trip per timeframe.

```
Model {
  generatedAt: string
  seasonStart, latestDate: string
  days: [
    {
      date: "YYYY-MM-DD",
      buckets: {                      // keyed by C/INF/OF/UT/SP/RP
        "<bucket>": { n, sumAbsErr, sumSignedErr, sumSqErr }
      },
      calibBins: [                    // ~10 fixed projected-value bins
        { loProj, hiProj, n, sumActual, sumProj }
      ],
      topMisses: [                    // capped: ~top 25 over + 25 under by |diff|
        { playerID, name, mlbTeam, bucket, isPitcher, projected, actual, diff }
      ]
    }
  ]
}
```

Derived metrics (computed client-side and in Go tests):

- **MAE** = ΣabsErr / Σn
- **Bias** = ΣsignedErr / Σn   (signed = actual − projected; positive ⇒ under-projecting)
- **RMSE** = √(ΣsqErr / Σn)
- **n** = Σn (sample size; player-days)

Bucket filtering (hitters / pitchers / all) is just choosing which bucket keys
to sum. `topMisses` are merged across the window and re-sorted client-side; the
per-day cap bounds the payload while preserving the true extremes for any window
(a day's global top-N over/under always contains the window's top-N for that day).

Estimated payload: season × 6 buckets × small structs + capped misses ≈ 1–2 MB,
acceptable to embed inline in one HTML file.

## Panels

Global controls (apply client-side to all panels): **timeframe** (7 / 14 / 30 /
season) and **role** (hitters / pitchers / all).

1. **Headline scorecard + trend** — MAE / Bias / RMSE / n for the selected
   window, each with a Δ vs the prior equal-length window (is it improving?).
   Plus a rolling line chart of MAE and bias across the season.
2. **Accuracy by position** — MAE and bias bars per bucket (C / INF / OF / UT /
   SP / RP). Surfaces which positions the model handles worst.
3. **Calibration scatter** — binned projected (x) vs mean actual (y) with a y=x
   reference line. Bins drifting above/below the diagonal reveal systematic
   under/over-projection across the scoring range. The key "how to improve" view.
4. **Worst misses** — table of the biggest over- and under-projected
   player-days in the window (player | date | bucket | proj | actual | diff),
   to spot patterns (ramp-ups, injuries, role changes).
5. **Auto-generated insights** — rule-based plain-language callouts derived from
   the same aggregates, e.g. "SP bias −1.2 over 14d — systematically
   over-projecting starters" or "Accuracy improved 6% vs the prior 14d window."

The insight rules are intentionally simple and live in one place
(`GenerateInsights`) so they are easy to tune. Exact thresholds/wording are an
implementation detail to be decided during the plan (a good candidate for a
focused, reviewable contribution).

## Code layout

- **`internal/analysis`** — add a `Reader` interface and `FileReader`
  implementation (list `grades/dt=*/`, parse NDJSON into `[]GradeRow`).
  `GradeRow` already exists. Mirrors the existing `Writer` / `FileWriter`.
- **`internal/analysis/s3grades`** — add S3 read (list keys under the `grades/`
  prefix + get objects) alongside the existing writer. Keeps the AWS SDK out of
  the `analysis` leaf, exactly as today.
- **`internal/report`** (new; mirrors `internal/recap`):
  - `Aggregate(rows []analysis.GradeRow) Model` — pure, no I/O.
  - `GenerateInsights(m Model) []Insight` — pure, rule-based.
  - `Render(m Model, w io.Writer) error` — embeds JSON into an embedded
    `template.html`; charts drawn client-side via a small CDN charting lib.
  - Unit-tested in isolation (table-driven aggregation + golden render check).
- **`cmd/projection-site.go`** — new Cobra command. Selects the S3 reader when
  `STATE_BUCKET` is set, else the local `.analysis/` `FileReader`; writes
  `./report/index.html`. Flags: `--out` (default `report`), `--open`,
  `--no-cache`. Add a corresponding line to the `make run-all` recipe.

## Hosting (AWS)

A **separate** S3 bucket + CloudFront distribution dedicated to the report,
copy-pasting the recap's CDK block in `infra/infra.go`:

- New `awss3.Bucket` (private) → exported `REPORT_BUCKET` env on the task def.
- New `awscloudfront.Distribution` with `DefaultRootObject = index.html` and an
  origin-access-controlled S3 origin (identical to `SiteCdn`). Output its domain.
- `entrypoint.sh`: one new sync block —
  `[ -d ./report ] && [ -n "$REPORT_BUCKET" ] && aws s3 sync ./report/ "s3://$REPORT_BUCKET/" --delete --quiet || true`.
- New EventBridge schedule: `{"ProjectionSite", "cron(0 15 * * ? *)", ["projection-site", "--out", "report"]}` — daily, ~90 min after `grade` (13:30 UTC) so it always reads fresh grades.

**Why a separate distribution, not co-hosting under the existing one:** the
recap's `aws s3 sync ./dist/ s3://$SITE_BUCKET/ --delete` deletes everything at
the destination not present in the recap's `dist/`, so any co-hosted second site
at a prefix would be wiped on the next weekly recap run. A subpath would also
need CloudFront default-root-object handling for `/projections/`. A second
bucket+distribution is behaviorally simpler, fully isolated, independently
cache-invalidated, and is "just like the recap site." Marginal cost.

## Testing

- `internal/report` aggregation: table-driven — known `[]GradeRow` → expected
  MAE / bias / RMSE / calibration bins / top misses / insights. Pure, no network.
- `analysis.FileReader`: round-trip against a temp dir written by `FileWriter`.
- `s3grades` reader: against a mock/local S3 or the existing test seam used by
  the writer.
- Render: assert the embedded JSON parses and each panel's container renders
  (mirrors `recap/render_test.go`).
- Smoke: `make run-all` exercises `projection-site` in read-only mode.

## v2 follow-on (filed, not built here)

Separate PRD: snapshot multiple candidate systems (steamer / depthcharts /
thebatx + weighting variants) per player per day, grade each against actuals,
then overlay them in the same calibration / by-position panels for head-to-head
comparison. **Dependency:** v2 produces no useful output until the multi-system
capture has accumulated weeks of data, so we may choose to start that capture
early (a small change to the snapshot writer) even though the comparison UI
ships later. v1's calibration and by-position panels are designed so a
per-system overlay slots in without restructuring.

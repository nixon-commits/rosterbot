# Dashboard v2: native reports + glow-up + live run progress

Status: approved
Date: 2026-07-20

## Problem

The private dashboard SPA (`web/dashboard/`, passkey-gated, served from
`DashboardBucket`/`DashboardCdn`) has three shortcomings:

1. **Foreign report embeds.** Projections and Value surface as `<iframe>`s of
   standalone Go-rendered HTML pages (`/report/index.html`, `/report/value.html`)
   — own fonts, own Chart.js, own layout, awkward `calc(100vh - 4rem)` sizing.
   They don't share the SPA's design language.
2. **No cohesive visual system.** Lineup/Jobs/Runs/Passkeys grew tab-by-tab.
3. **No live run visibility.** Triggering a job flips the run ledger
   RUNNING → (minutes later) SUCCESS/FAILED with nothing in between; the log
   tail is captured only at the end. You can't watch a job execute.

Goal: fold reports in as native views fed by JSON, restyle the whole dashboard
into one design system, and add **live phased run progress** so triggered jobs
are watchable in real time.

## The easy/hard split (why the data layer differs per feature)

- **Reports (Projections/Value) are daily data.** A static JSON sidecar is the
  *correct* delivery, not a shortcut — making it "live" would re-run the whole
  Analysis-Store aggregation on every page load for numbers that change once a
  day. Keep it static.
- **Runs/Jobs are where "live" pays off.** This is the one place a real-time
  view adds genuine value, so it gets the live treatment.

## Architectural constraint

The `/v1/*` API is a **Lambda behind CloudFront** — no long-lived connection
holder. So live updates use **fast client polling of a progress record the
running task keeps updating**, not push (SSE/WebSocket would need API Gateway
WebSocket + a connection store — big infra, marginal gain for a single user's
single-digit-minute jobs). Depth chosen: **L1 (persisted phase progress)** — a
phased progress bar. Live log-tail (L2) and true push (L3) are out of scope.

## Decisions

1. **Static JSON sidecar for reports.** `projection-site` writes
   `report/model.json` + `report/value.json` (a `json.Encode` of the existing
   `*report.Model` / `*valuereport.Model`) alongside the dashboard assets under
   the `report/` key prefix. The SPA fetches and renders them natively. No
   Lambda/IAM change; data stays publicly fetchable by direct URL, same posture
   as today's `report/*.html`.
2. **SPA is the single render path for reports.** `report.Render` /
   `valuereport.Render` and both `template.html` files are deleted.
   `Aggregate` / `BuildModel` (the math) are unchanged and remain the single
   source of truth. Only presentation moves client-side — no two-renderer drift.
3. **Vendor Chart.js.** A self-hosted UMD build in `web/dashboard/vendor/`
   powers the data-dense plots (trends, calibration scatter, multi-team series).
   No CDN dependency (works against local `serve`). Tiles/tables/badges/toggles
   are native design-system primitives; Chart.js is only for the plots.
4. **Whole-dashboard glow-up.** Refreshed design tokens + primitives apply to
   every tab, so the app reads as one system.
5. **Live progress via persisted phases (L1).** Promote `internal/progress`
   with a `Recorder` global hook (mirrors the existing `OutputRecorder` /
   `notify.Recorder` pattern). `cmd` installs it keyed on `RUN_ID` to write
   `runs/<id>/progress.json`. New `GET /v1/runs/{id}/progress`. The SPA polls
   it while a run is RUNNING and renders a phased bar.
6. **Run status stays ledger-owned.** `progress.json` carries only phase detail;
   authoritative RUNNING→SUCCESS/FAILED transitions stay in the run ledger
   (written by `entrypoint.sh`). The SPA reads status from `/v1/runs`, phase
   from `/progress`. This keeps the crash-reaping semantics
   (`maxJobDuration` staleness) unchanged.

## Architecture

### A. Reports data pipeline

`cmd/projection-site.go` reads the Analysis/Team-Value stores, builds the two
Models, and today writes `report/index.html` + `report/value.html`. It now
writes `report/model.json` + `report/value.json` instead (plain
`json.NewEncoder`). Store reads, `Aggregate`, `BuildModel`, season-start floor,
and S3-vs-local reader selection are untouched. `entrypoint.sh` / `cmd/sync.go`
are unchanged — the `report/` dir still syncs into `DashboardBucket` under the
`report/` prefix, now carrying `.json`.

### B. SPA — reports views

- `vendor/chart.min.js` (new) — self-hosted Chart.js UMD build.
- `chart.js` (new) — thin Chart.js wrapper: theme-aware defaults derived from
  CSS custom properties, plus line/scatter/bar helpers. One home for chart
  theming.
- `projections.js` (new) — `renderProjections(root)`: fetch `/report/model.json`;
  render window(7/14/30/season) × role(all/hitters/pitchers) × system toggles,
  scorecard tiles (MAE/Bias/RMSE/N + prior-window deltas), by-position bars,
  calibration scatter, worst-misses table, per-system MAE trend, and the
  system-comparison ranking + overlaid trend lines. Ports `report/template.html`
  inline JS.
- `value.js` (new) — `renderValue(root)`: fetch `/report/value.json`; render the
  metric selector (Total/MLB/Minors/Hitter/Pitcher derived client-side), the
  multi-team time-series line, the legend toggle (+All/None), and the standings
  table with logos + join coverage. Ports `valuereport/template.html` inline JS.
- `reportview.js` (deleted). `app.js` routes `#projections`/`#value` to the new
  renderers.
- Each view shows a loading state, a friendly missing-file (`404` before first
  `projection-site` run) state, and an empty-store state.

### C. Live run progress (L1)

**`internal/progress`** gains a persistence seam without disturbing its terminal
output:

- New exported `Snapshot{ Phase string; Pct int; Phases []PhaseState; Status
  string; UpdatedAt string }` and `PhaseState{ Name string; State string }`
  (`state` ∈ pending/active/done/warn).
- New nil-safe global `var Recorder func(Snapshot)`. `Progress.Start/Done/Warn`
  call an internal `emit()` that builds the current snapshot and calls
  `Recorder` **in every mode** (the terminal drawing stays gated on
  `interactive`; emission does not — Fargate runs non-interactive, so today's
  `Start` no-op must not suppress the recorder).
- The ordered phase list is defined per progress instance (optimize's 7 phases
  come from an ordered slice, replacing the unordered `phaseWeight` map as the
  ordering source; the weight lookup stays for `pct`). Generic jobs construct a
  coarse two-phase progress (`Running` → `Done`).

**`cmd`** installs `progress.Recorder` alongside the existing `OutputRecorder`
wiring: a closure that marshals the snapshot and writes it under `RUN_ID` via a
`ProgressWriter` (S3 when `STATE_BUCKET` set, else local). When `RUN_ID` is
unset (local runs, tests) the recorder is nil → no-op, nothing else changes.

**`internal/lineupapi`** gains the read/write seam mirroring output.go:

- `ProgressStore` (`GetProgress(ctx, runID) ([]byte, bool, error)`) and
  `ProgressWriter` (`PutProgress(ctx, runID, data)`), a `FileProgressStore`
  local adapter, and an s3 adapter in `internal/lineupapi/s3lineup` (already
  owns the `runs/` prefix — progress lands at `runs/<id>/progress.json`).
- `handler.go`: `GET /v1/runs/{id}/progress` → 200 with the snapshot, 404 when
  absent (job hasn't emitted yet / older run), 502 on backend error.
- `types.go`: the wire `ProgressSnapshot` shape (snake_case, matching the
  package's `Snapshot`).

**Phase coverage:** `optimize` gets the full phased bar. The other 8 jobs emit
the coarse `Running → Done` progress so the live view is uniform; richer phases
are added opportunistically later (noted, not required here).

### D. SPA — live UX + glow-up

- `live.js` (new) — a small controller: background-polls `/v1/runs` (~5s); when
  any run is RUNNING it renders a **"Now Running" hero** (pinned above the
  current view) and polls that run's `/progress` (~2s) for the phased bar +
  elapsed timer. On the ledger flipping terminal it fires a completion toast,
  refreshes the runs list, and clears the hero. Intervals are cleared on route
  change and when no RUNNING runs remain; a RUNNING entry older than
  `maxJobDuration` (2h) is treated as stale and not polled.
- `jobs.js` — the Run button's 202 `{id}` response drops the user into the live
  view (hero + progress) instead of a static "submitted" note.
- A tiny toast primitive (success/fail) and a Runs nav badge (dot while any run
  is RUNNING).
- **Glow-up:** `style.css` gains a fuller token set (spacing scale, radius,
  shadow, a chart palette exposed as CSS vars so `chart.js` can read them) over
  the existing light/dark variables; refined active-tab treatment; unified
  `.card`/tables/badges; a `.stat-tile` primitive for scorecard/standings
  numbers; the progress bar + toast components. Lineup/Jobs/Runs/Passkeys adopt
  the refreshed primitives (no behavior change). Aesthetic direction is chosen
  during implementation via the `frontend-design` skill; this spec fixes
  structure, not palette.

## Component changes

- `cmd/projection-site.go`: swap the two `Render` calls for JSON encoding.
- `cmd/` (recorder wiring, near OutputRecorder): install `progress.Recorder`
  keyed on `RUN_ID`; choose S3 vs local `ProgressWriter` by `STATE_BUCKET`.
- `internal/report`: delete `render.go` + `template.html` + `render_test.go`;
  keep `aggregate.go`/`model.go`/`insights.go` + tests.
- `internal/valuereport`: delete `render.go` + `template.html` and the render
  portion of `valuereport_test.go`; keep `model.go` + `BuildModel` tests.
- `internal/progress`: add `Snapshot`/`PhaseState`/`Recorder`/`emit`; ordered
  phase slice; emission decoupled from `interactive`. New `progress_test.go`
  cases: recorder fires on Start/Done/Warn in non-interactive mode; snapshot
  phase/pct/state correctness.
- `internal/lineupapi`: `ProgressStore`/`ProgressWriter` + `FileProgressStore`,
  `ProgressSnapshot` wire type, `GET /v1/runs/{id}/progress` handler + tests.
- `internal/lineupapi/s3lineup`: s3 progress adapter + test.
- `web/dashboard/`: add `vendor/chart.min.js`, `chart.js`, `projections.js`,
  `value.js`, `live.js`; delete `reportview.js`; edit `app.js`, `index.html`,
  `style.css`, `jobs.js`, `runs.js`, `api.js` (add `runProgress(id)`).
- `README.md`: local-dev note (`projection-site --out web/dashboard/report`
  now writes JSON), live-progress endpoint, drop iframe phrasing.
- `CLAUDE.md`: `internal/report`/`internal/valuereport` now emit JSON consumed
  by the SPA (not HTML); document the `progress.Recorder` hook +
  `runs/<id>/progress.json` + the `GET /v1/runs/{id}/progress` endpoint.
- `docs/aws-deployment.md`: adjust the report-publish bullet if it names the
  HTML files.

## Testing / verification

- Go: `go build ./...`, `go vet ./...`, `go mod tidy`. `report`/`valuereport`
  aggregate/model tests pass; render tests removed with the render code.
  New progress + lineupapi progress-endpoint + s3 adapter tests pass.
- `projection-site --out /tmp/r` → valid `model.json` + `value.json` (`jq`).
- Local live-progress smoke: run a job locally with `RUN_ID` set + a
  `FileProgressStore` dir, confirm `progress.json` advances through phases and
  `GET /v1/runs/{id}/progress` returns it via `serve`.
- SPA manual: `projection-site --out web/dashboard/report` + `serve --web`;
  exercise Projections/Value toggles, loading/missing/empty states; trigger a
  job and watch the hero + phased bar advance to a completion toast; verify
  light/dark and cross-tab visual cohesion; verify polling stops on idle.
- `make run-all` unaffected (`projection-site` still runs; output shape only).
- No CDK/infra change (no new IAM — progress rides the existing `runs/` S3
  prefix the task role already read/writes), so no deploy gate beyond the
  normal push-to-main image build.

## Out of scope

- Recap site (`SiteBucket`/`SiteCdn`) — unchanged, separate + public.
- `report.Aggregate` / `valuereport.BuildModel` computation changes.
- Access-control hardening of the report JSON (still public).
- Live log tail (L2) and true push (L3).
- New report chart types/metrics beyond the current templates.
- Full phase instrumentation of all 8 non-optimize jobs (coarse now, rich later).

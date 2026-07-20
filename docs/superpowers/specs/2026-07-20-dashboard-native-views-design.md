# Native Projections/Value dashboard views + whole-dashboard glow-up

Status: approved
Date: 2026-07-20

## Problem

The private dashboard SPA (`web/dashboard/`, passkey-gated, served from
`DashboardBucket`/`DashboardCdn`) currently surfaces the projection-accuracy
report (`internal/report`) and team-value tracker (`internal/valuereport`) as
`<iframe>` embeds of standalone Go-rendered HTML pages
(`/report/index.html`, `/report/value.html`) — see the 2026-07-19
consolidation spec. The iframe embeds read as foreign documents: they ship
their own fonts, their own Chart.js, their own layout, and size awkwardly
(`calc(100vh - 4rem)`, double scrollbars). They don't share the SPA's theme
or design language.

Goal: replace the iframe embeds with **native, first-class SPA views** that
fetch data as JSON and render inside the dashboard's own design system, and
**polish the whole dashboard** (header/nav/theme + Lineup/Jobs/Runs/Passkeys)
into one cohesive visual system.

## Decisions

1. **Static JSON sidecar, not a live API.** `projection-site` (the daily
   producer) additionally writes the already-computed view models as JSON
   sidecars — `report/model.json` and `report/value.json` — alongside the
   dashboard's static assets under the `report/` key prefix. The SPA fetches
   these static files and renders natively. No new Lambda handler, no IAM
   change, daily cadence preserved. The data stays publicly fetchable by
   direct URL, exactly like today's `report/*.html` — this is a consolidation,
   not an access-control change (same posture as the prior spec's Decision 4).

2. **SPA is the single render path (A1).** `report.Render` /
   `valuereport.Render` and both `template.html` files are **deleted**.
   `report.Aggregate` / `valuereport.BuildModel` (the math) are unchanged and
   remain the single source of truth for MAE/bias/RMSE/trend/value
   computation. Only presentation moves to the client. This avoids maintaining
   two chart renderers (Go template + SPA JS) that would drift.

3. **Vendor Chart.js for data-dense charts.** The trend lines, calibration
   scatter, and multi-team time series are rendered with a self-hosted
   Chart.js UMD build in `web/dashboard/vendor/`. No CDN dependency (works
   against local `serve`). Scorecard tiles, tables, badges, and toggles are
   built natively with the SPA design system — Chart.js is only for the plots.

4. **Whole-dashboard glow-up.** The refreshed design-system tokens and
   primitives apply across every tab (Lineup, Jobs, Runs, Passkeys) plus the
   two new native views, so the app reads as one cohesive system rather than a
   polished pair of new tabs bolted onto older ones.

## Architecture

### Data pipeline

`cmd/projection-site.go` currently reads the Analysis Store + Team Value Store,
builds the two Models, and writes `report/index.html` + `report/value.html`
via `report.Render` / `valuereport.Render`.

After this change it writes **`report/model.json`** and
**`report/value.json`** instead — a plain `json.NewEncoder(f).Encode(model)`
of the same `*report.Model` / `*valuereport.Model` values. The store reads,
`Aggregate`, `BuildModel`, season-start floor logic, and S3-vs-local reader
selection are all untouched.

`entrypoint.sh` / `cmd/sync.go` are unchanged: the `report/` directory still
syncs into `DashboardBucket` under the `report/` prefix (now carrying `.json`
instead of `.html`). Final URLs become
`https://<dashboard-domain>/report/model.json` and `.../report/value.json`.

### SPA structure

New / changed modules under `web/dashboard/`:

- **`vendor/chart.min.js`** (new) — self-hosted Chart.js UMD build.
- **`chart.js`** (new) — a thin wrapper that constructs Chart.js instances
  with theme-aware defaults derived from the CSS custom properties (grid/tick
  colors, font), plus small helpers for the line / scatter / bar shapes the
  two views need. One home for chart theming so both views stay consistent.
- **`projections.js`** (new) — exports `renderProjections(root)`. Fetches
  `/report/model.json`, renders: the window (7/14/30/season) × role
  (all/hitters/pitchers) × system toggles, the scorecard tiles (MAE / Bias /
  RMSE / N with prior-window deltas), the by-position bar/table, the
  calibration scatter, the worst-misses table, the per-system MAE trend
  chart, and the system-comparison ranking table + overlaid trend lines. This
  ports the interaction logic that currently lives in `report/template.html`'s
  inline JS.
- **`value.js`** (new) — exports `renderValue(root)`. Fetches
  `/report/value.json`, renders: the metric selector
  (Total/MLB/Minors/Hitter/Pitcher derived client-side from the four leaves),
  the multi-team time-series line chart, the team legend toggle (+ All/None),
  and the current-standings table with logos and join coverage. Ports
  `valuereport/template.html`'s inline JS.
- **`reportview.js`** (deleted) — the iframe wrapper is removed.
- **`app.js`** — `ROUTES` maps `#projections` → `renderProjections` and
  `#value` → `renderValue` (imports switch from `reportview.js`).
- **`index.html`** — unchanged nav (already has `#projections` / `#value`
  links); add the `vendor/chart.min.js` `<script>` (or dynamic-import it from
  `chart.js`). The `viewport`/head stay as-is.

### Loading + error states

Each new view shows a lightweight loading state while fetching, and a friendly
empty/error state when the JSON is missing (`404` before the first
`projection-site` run) or empty (`value.json` with `"empty": true`, or a
`model.json` with no graded rows). These mirror the "collecting data" notes the
old templates showed, rendered with the SPA's own primitives.

### Design-system glow-up

`style.css` gains a fuller token set (spacing scale, radius, shadow, a chart
color palette exposed as CSS vars so `chart.js` can read them) layered on the
existing light/dark `--bg`/`--fg`/... variables. The header/nav get a refined
active-tab treatment; `.card`, tables, and badges are unified; a `.stat-tile`
primitive is added for the scorecard/standings numbers. Lineup/Jobs/Runs/
Passkeys are restyled only to the extent of adopting the refreshed primitives —
no behavior change to those views. Aesthetic direction is chosen during
implementation via the `frontend-design` skill; this spec fixes the structure,
not the palette.

## Component changes

- `cmd/projection-site.go`: swap the two `Render` calls for JSON encoding to
  `report/model.json` / `report/value.json`; drop the now-unused imports of
  `report.Render` / `valuereport.Render` paths (the packages are still imported
  for `Aggregate` / `BuildModel`).
- `internal/report`: delete `render.go` + `template.html` + `render_test.go`.
  Keep `aggregate.go`, `model.go`, `insights.go` and their tests.
- `internal/valuereport`: delete `render.go` + `template.html` and the render
  portion of `valuereport_test.go`. Keep `model.go` + `BuildModel` tests.
- `web/dashboard/`: add `vendor/chart.min.js`, `chart.js`, `projections.js`,
  `value.js`; delete `reportview.js`; edit `app.js`, `index.html`, `style.css`.
- `README.md`: update the local-dev note — `projection-site --out
  web/dashboard/report` now writes `model.json`/`value.json` (not HTML) for the
  native tabs; drop any "iframe" phrasing.
- `CLAUDE.md`: update the `internal/report` / `internal/valuereport` sections —
  they no longer render self-contained HTML; they produce Models serialized to
  JSON sidecars consumed by the SPA. Note the SPA is the render path.
- `docs/aws-deployment.md`: adjust the report-publish bullet if it names the
  HTML files specifically.

## Testing / verification

- Go: `go build ./...`, `go vet ./...`, `go mod tidy` after deleting the render
  code. `report`/`valuereport` aggregate + model tests still pass; render tests
  are removed with the render code.
- `projection-site` local run: `projection-site --out /tmp/r` produces valid
  `model.json` + `value.json` (spot-check with `jq`).
- SPA manual check: `projection-site --out web/dashboard/report` then `serve
  --web`; load `#projections` and `#value` against real JSON, exercise every
  toggle (window/role/system, metric selector, legend). Verify loading state,
  the missing-file `404` state, and the empty-store state. Confirm light/dark
  both read well and the new views match the restyled Lineup/Jobs/Runs tabs.
- `make run-all` unaffected (`projection-site` still runs; output shape
  changes only).
- No CDK / infra change, so no deploy gate here beyond the normal image build
  on push to main.

## Out of scope

- Recap site (`SiteBucket`/`SiteCdn`) — unchanged, still separate + public.
- Any change to `report.Aggregate` / `valuereport.BuildModel` computation.
- Access-control hardening of the report JSON (still public; revisit only if
  the data should become private — would mean the API-endpoint approach).
- New chart types or metrics beyond what the current templates already show —
  this is a port + polish, not a feature expansion.

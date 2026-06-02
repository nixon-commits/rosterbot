# RosterBot Portal — Design Spec
_2026-06-01_

## Overview

A mobile-first web portal extending the existing `rosterbot serve` command. Adds five new tabs (Home, Lineup, Waivers, Prospects, Backtest) alongside the existing developer tools (Projections, Compare, Blend Curves, Lineup Diff). Personal use only, view-only (no actions). Extends the `feature/web-gui` branch.

## Audience & Constraints

- **Single user** — personal tool, no authentication required
- **View-only** — no lineup apply, no player transactions
- **Mobile-first** — primary use is checking status on phone (home WiFi or local network)
- **Local server** — `rosterbot serve --port 8080`; remote access via home network IP or future `fly deploy`

## Architecture

The existing Go HTTP server (`internal/server/`) gains five new `/api/portal/*` endpoints. The existing `/api/projections`, `/api/compare` etc. are untouched. The portal UI is embedded in the same `web/static/index.html` as new tabs.

```
rosterbot serve --port 8080
  │
  ├── GET /                        → web/static/index.html (extended)
  ├── GET /api/projections          → existing, unchanged
  ├── GET /api/compare              → existing, unchanged
  │
  ├── GET /api/portal/summary       → Home dashboard
  ├── GET /api/portal/lineup        → Today's optimized lineup + GS budget
  ├── GET /api/portal/waivers       → Latest waiver picks from cache
  ├── GET /api/portal/prospects     → Latest prospect alerts from cache
  └── GET /api/portal/backtest      → Projection + lineup accuracy (7 days)
```

**Data source:** all portal endpoints read directly from `.cache/` and `.backtest/snapshots/` — the same files GHA populates hourly/daily. No export step, no stale JSON files. If a cache file is missing the endpoint returns a graceful `{"status":"not_available","reason":"..."}` response and the UI shows a "not yet run" empty state.

## Navigation

**Desktop:** horizontal tab bar across the top (existing pattern).

**Mobile:** bottom tab bar with 5 tabs pinned. Tab order: Home · Lineup · Waivers · Prospects · More (Backtest + existing dev tools behind More).

```
[ 🏠 Home ] [ ⚾ Lineup ] [ 🔥 Waivers ] [ ⬆️ Prospects ] [ ··· More ]
```

Breakpoint: `≤640px` triggers bottom tab bar; `>640px` uses existing top tabs.

## API Endpoints

### `GET /api/portal/summary`
Aggregates across all domains into one response. Powers the Home dashboard.

```json
{
  "generated_at": "2026-06-01T16:00:00Z",
  "lineup": {
    "status": "optimal",
    "changes": 0,
    "gs_used": 1,
    "gs_max": 12,
    "gs_projected": 5.4,
    "last_updated": "2026-06-01T14:00:00Z"
  },
  "waivers": {
    "pick_count": 2,
    "top_pick": { "signal": "HOT", "name": "Nathaniel Lowe", "team": "CIN", "gap": 0.77 },
    "last_updated": "2026-06-01T13:00:00Z"
  },
  "prospects": {
    "high_alert_count": 1,
    "top_alert": { "kind": "called-up", "player": "Jackson Chourio", "team": "MIL" },
    "last_updated": "2026-06-01T11:00:00Z"
  },
  "backtest": {
    "efficiency_pct": 91.2,
    "mae": 2.14,
    "window_days": 7,
    "last_updated": "2026-06-01T14:00:00Z"
  }
}
```

Missing domains return `null` for that key; the UI shows "—" placeholders.

### `GET /api/portal/lineup`
Runs the optimizer in dry-run mode against the cached roster/projections (same as `rosterbot optimize --dry-run --matchup`) and returns the result. The cached FanGraphs projections and Fantrax roster are reused if fresh; the optimizer is always re-run to produce current slot assignments.

```json
{
  "generated_at": "...",
  "date": "2026-06-01",
  "matchup_end": "2026-06-07",
  "gs_used": 1, "gs_max": 12, "gs_projected": 5.4,
  "status": "optimal",
  "changes": [],
  "active_hitters": [
    { "name": "Julio Rodriguez", "team": "SEA", "slot": "UT",
      "pts_per_game": 5.06, "has_game": true, "locked": false, "positions": "CF,OF" }
  ],
  "active_pitchers": [
    { "name": "Kyle Harrison", "team": "MIL", "slot": "P",
      "role": "SP", "pts_per_game": 16.75, "is_starter": true, "locked": false }
  ],
  "bench_hitters": [
    { "name": "Nico Hoerner", "team": "CHC", "pts_per_game": 5.21,
      "has_game": false, "bench_reason": "no_game" }
  ],
  "bench_pitchers": [...]
}
```

### `GET /api/portal/waivers`
Reads latest waivers cache output.

```json
{
  "generated_at": "...",
  "date": "2026-06-01",
  "picks": [
    { "signal": "HOT", "name": "Nathaniel Lowe", "team": "CIN",
      "drop_name": "Colt Emerson", "gap": 0.77, "detail": "wOBA .387 · 14.1% Brl · 46% HH",
      "is_pitcher": false }
  ]
}
```

### `GET /api/portal/prospects`
Reads latest prospects cache output.

```json
{
  "generated_at": "...",
  "alerts": [
    { "priority": "high", "kind": "called-up",
      "player": "Jackson Chourio", "team": "MIL",
      "detail": "Called up — move from Minors slot" }
  ],
  "upgrades": [
    { "source": "FanGraphs", "drop_name": "T. Sykora", "drop_rank": 113,
      "add_name": "Carson Williams", "add_rank": 27, "rank_gap": 86, "near_term": true }
  ],
  "minors_roster": [
    { "name": "Thomas White", "team": "MIA",
      "ranks": [{ "source": "FanGraphs", "rank": 10 }, { "source": "HKB", "rank": 104 }] }
  ]
}
```

### `GET /api/portal/backtest`
Reads `.backtest/snapshots/` for the trailing 7 days.

```json
{
  "generated_at": "...",
  "window_days": 7,
  "efficiency_pct": 91.2,
  "mae": 2.14, "bias": 0.3, "rmse": 3.1,
  "by_position": [
    { "bucket": "SP", "n": 42, "mae": 3.2, "bias": 0.8 },
    { "bucket": "C",  "n": 7,  "mae": 1.8, "bias": -0.2 }
  ],
  "top_misses": [
    { "name": "Kyle Harrison", "date": "2026-05-28", "bucket": "SP",
      "projected": 18.0, "actual": 2.0, "diff": -16.0 }
  ],
  "lineup_days": [
    { "date": "2026-05-28", "actual_pts": 84.2, "optimal_pts": 92.1, "efficiency_pct": 91.4 }
  ]
}
```

## UI Pages

### Home
- 4-card status grid: Lineup status + GS budget, top waiver pick, top prospect alert, last backtest efficiency
- Each card is a tap target that navigates to its full page
- "Updated Xh ago" per domain using `last_updated` timestamp
- Graceful empty states when a domain hasn't run yet

### Lineup
- GS budget progress bar (used / max, projected remaining)
- Active hitters table: name, team, slot badge, pts/game, 🔒 locked indicator, ✓ has game
- Active pitchers table: same fields + SP/RP role badge
- Bench section: dimmed, shows bench reason (no game / lower projection / GS budget)
- Benched-due-to-GS-budget rows highlighted in amber

### Waivers
- One card per pick: signal badge (HOT/BUY-LOW/BOTH in color), player + team, drop target, FPG gain in green, stat detail on second line
- Sorted by FPG gain descending
- Staleness badge if data is >24h old

### Prospects
- Alerts section: priority badge (HIGH in amber, MEDIUM in yellow), emoji kind icon, player + team, detail text
- Upgrades section: per ranking source, drop → add with rank gap
- Minors Roster section: player list with rank badges per source
- Empty state: "No alerts today" with last-checked time

### Backtest
- Summary stats row: Efficiency %, MAE, Bias, RMSE
- Per-position MAE bar chart (horizontal bars, SP/RP/C/INF/OF/UT)
- Top misses list: player, date, bucket, projected vs actual vs diff (signed, over-projected first)
- Per-day efficiency sparkline: simple bar chart of the trailing 7 days

## Styling

Dark theme applied to the existing `style.css`, matching the recap template's CSS variables:
- `--bg: #0b0f1a`, `--bg-card: #131a2c`, `--border: #243049`
- `--text: #e6ecff`, `--text-dim: #8b9bbd`, `--accent: #f97316`
- `--good: #4ade80`, `--bad: #f87171`, `--gold: #fbbf24`

The existing dev-tool tabs (Projections, Compare, Blend Curves, Lineup Diff) retain their current styling — no changes there.

## File Changes

```
cmd/serve.go                      — no changes (serve command stays as-is)
internal/server/handlers.go       — add 5 portal handlers
internal/server/portal_data.go    — new: data fetching/shaping for portal endpoints
internal/server/server.go         — register 5 new routes
web/static/index.html             — add 5 portal tab sections + mobile nav HTML
web/static/style.css              — dark theme overhaul + mobile bottom tab bar
web/static/app.js                 — add portal tab fetch/render logic
```

No new commands. No new GHA workflows. No changes to existing API endpoints.

## Error Handling

- Missing cache file → `{"status":"not_available","reason":"cache not populated"}` → UI shows empty state with last-known timestamp if available
- Cache file corrupt/unparseable → log warning, return not_available
- Backtest snapshots missing → return available days only, note missing days
- All errors are non-fatal at the page level — one failed domain doesn't break the others

## Out of Scope

- Authentication
- Write actions (apply lineup, add/drop players)
- Push notifications from the server
- Remote hosting / deployment (can be done later with `fly deploy`)
- Recap tab (existing recap site stays at its own GitHub Pages URL)

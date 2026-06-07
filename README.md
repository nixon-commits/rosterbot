# RosterBot

Fantasy baseball roster automation for Fantrax head-to-head points leagues. Optimizes daily lineups, monitors minor league prospects, and tracks league-wide game start violations.

## What It Does

- **Daily lineup optimization** — Backtracking optimizer finds the globally optimal hitter slot assignment. Pitcher optimizer accounts for probable starters and weekly GS budgets. Blends FanGraphs projections with recent rolling stats so hot/cold streaks factor into decisions.
- **Real-life lineup awareness** — Checks MLB starting lineups so players sitting out (rest days, etc.) get benched in favor of active hitters.
- **Prospect monitoring** — Scans MLB transactions, MiLB performance breakouts, and prospect rankings (MLB Pipeline / FanGraphs) to surface call-up alerts, hot streaks, and upgrade recommendations.
- **Trade monitoring** — Fetches recent league trades, values each side using HKB player rankings, and sends a Pushover notification with the trade report.
- **Statcast-driven waiver picks** — Cross-references league free agents against Baseball Savant data to surface buy-low candidates (xStats outpacing surface stats) and confirmed hot streaks (recent production backed by barrel/hard-hit quality). Ranks by Steamer-projected fantasy points per game.
- **GS violation detection** — Tallies game starts across all league teams and sends Pushover notifications when a team exceeds the cap.
- **Roster hygiene** — Flags healthy players stuck in IL slots, called-up players still in Minors slots, and injured players occupying active slots.
- **Backtesting** — Grades past lineup moves against the hindsight-optimal lineup and measures projection accuracy against actual fantasy points.
- **Weekly recaps** — Sleeper-style HTML recaps with a Game of the Week win-probability chart, plus Heart Attack (most lead changes) and Comeback (winner with mid-week WP < 0.30) awards. Includes Top Single Day Performances (top 5 batters/pitchers, badged with the owning team's logo) and a League Leaders board ranking all rostered players by season-to-date wOBA (hitters) and FIP (pitchers).

## Quick Start

### Prerequisites

- Go 1.26+
- Chrome (for Fantrax authentication via headless browser)

### Setup

1. Clone the repo and create a `.env` file (gitignored):

```
FANTRAX_USERNAME=your_username
FANTRAX_PASSWORD=your_password
FANTRAX_LEAGUE_ID=your_league_id
FANTRAX_TEAM_ID=your_team_id
FANTRAX_IL_SLOTS=3
FANTRAX_MINORS_SLOTS=5
```

2. Build:

```bash
make build    # produces ./rosterbot
make install  # installs to $GOPATH/bin
```

### Usage

```bash
# Optimize today's lineup (dry run)
rosterbot optimize --dry-run

# Optimize a specific date
rosterbot optimize --dry-run --dates 2026-04-01

# Optimize a date range
rosterbot optimize --dry-run --dates 2026-03-26:2026-03-28

# Optimize remaining days in current matchup period
rosterbot optimize --dry-run --matchup

# Show full hitter adjustment pipeline (base → blend → park → platoon → opp SP → final)
rosterbot optimize --dry-run --pipeline

# Switch projection system (steamer, depthcharts, thebatx, steamer-ros, depthcharts-ros, thebatx-ros)
rosterbot optimize --dry-run --projections steamer
rosterbot optimize --dry-run --projections steamer-ros   # Rest-of-Season variant

# Bypass API cache (force fresh data)
rosterbot optimize --dry-run --no-cache

# Run prospect report
rosterbot prospects --dry-run

# Check recent trades with HKB valuations
rosterbot transactions --dry-run

# Identify Statcast-driven waiver wire pickups
rosterbot waivers --dry-run
rosterbot waivers --dry-run --top 25            # bigger list
rosterbot waivers --dry-run --positions OF,SP   # filter to specific slots

# Check GS violations
rosterbot gs-check --dry-run --force

# Backtest last completed matchup week (lineup + projection accuracy)
rosterbot backtest

# Backtest a specific window
rosterbot backtest --dates 2026-04-13:2026-04-19

# Compare recency-weighting strategies (YTD vs 14d/30d/decay) by lineup Gap (hitters)
rosterbot backtest --recency-experiment --dates 2026-05-01:2026-05-14

# Archive today's projections so a future backtest can grade them exactly
rosterbot optimize --dry-run --archive-projections

# Render Sleeper-style HTML recap of the most recently completed matchup week
rosterbot recap --out /tmp/recap.html

# Recap a specific window
rosterbot recap --dates 2026-04-20:2026-04-26 --out /tmp/recap.html

# Build a multi-week static site (one HTML per completed week + index.html)
rosterbot recap-site --out dist

# Print league scoring weights
rosterbot scoring
```

Remove `--dry-run` to apply changes.

## How the Optimizer Works

### Hitter Optimization

The hitter optimizer uses backtracking with pruning to find the slot assignment that maximizes total expected points. It respects position eligibility (C, 1B, 2B, 3B, SS, INF, OF, UT) and prefers fewer roster moves when assignments tie.

Players whose team isn't playing, who are confirmed out of the real-life MLB starting lineup, or who are injured/in the minors contribute 0 points and get benched.

### Pitcher Optimization

Pitchers are scored based on probable starter data. SPs confirmed as starters get full value. SPs not listed as probable starters get a 0.10x discount so RPs are preferred for limited P slots. When a weekly GS limit is set (`GS_MAX`), the GS budget gate allocates starts proportionally across the matchup period, keeping the highest-value starters.

### Projection Blending

Projections blend FanGraphs season projections with recent Fantrax scoring data using PA-based dynamic weights:

| Season Point | Steamer Weight | Recent Weight |
|---|---|---|
| Early (4 GP) | 94% | 6% |
| Mid-season (66 GP) | 50% | 50% |
| Full season (150+ GP) | 30% (floor) | 70% |

Requires a minimum of 4 games played before recent stats are factored in. Falls back to 100% Steamer when no recent data is available.

Matchup adjustments (opposing pitcher FIP + platoon splits) are layered on top.

## Optional Configuration

| Env Var | Default | Description |
|---|---|---|
| `GS_MAX` | 0 (disabled) | Max game starts per matchup week — used by optimizer (weekly GS budget) and gs-check (violation detection) |
| `GS_MIN` | 0 (disabled) | Min game starts per matchup week — used by gs-check to flag teams below the floor |
| `PROSPECT_ROLLING_DAYS` | 14 | Days of MiLB stats for breakout detection |
| `PROSPECT_MIN_GAMES` | 8 | Minimum games for prospect breakout eligibility |
| `PROSPECT_RANK_CACHE_HOURS` | 168 | Hours to cache prospect rankings |
| `PROSPECT_UPGRADE_RANK_THRESHOLD` | 20 | Prospect rank threshold for upgrade alerts |
| `PUSHOVER_USER_KEY` | — | Pushover user key for notifications (trades, lineup) |
| `PUSHOVER_GROUP_KEY` | — | Pushover group key for GS violation alerts |
| `PUSHOVER_API_TOKEN` | — | Pushover API token for notifications |
| `BACKTEST_ARCHIVE` | — | Set to `1` to archive every `optimize` run's projections to `.backtest/snapshots/` for later grading (same as `--archive-projections`) |

## Caching

Network calls (Fantrax, MLB statsapi, FanGraphs, Baseball Savant, HKB,
MLB Pipeline) are cached on disk under `.cache/` as JSON files. File
names follow `<source>-<entity>-<scope>.json` — for example
`fantrax-pitcher-gs-<teamID>-<period>.json` or
`mlb-schedule-<YYYY-MM-DD>.json`. Three TTL tiers cover most data:

- **30 days** for past-period data that's immutable once a scoring
  period closes (per-period roster snapshots, recent stats, pitcher
  game starts, MLB schedules for past dates, MLB player IDs).
- **15 minutes** for "today, drifts during the day" data (current
  roster, FA pool, current period, pending/recent trades). Long
  enough to make hourly GHA reruns and local-dev iteration cheap;
  short enough that intra-day waiver pickups show up promptly.
- **7 days** for season-invariant config (slot counts, scoring
  weights, season date range).

Provider-specific caches use their own TTLs: FanGraphs Steamer (12 h),
MLB handedness (7 d), Baseball Savant CSVs (12 h), HKB rankings (8 h),
prospect rankings (`PROSPECT_RANK_CACHE_HOURS`, default 168 h),
in-season MiLB game logs (1 h).

`--no-cache` bypasses every layer for that command run, refetching
fresh data from each upstream. Useful if you suspect stale data or
want to validate that a cache key is being populated correctly.

The cache is just a directory — `rm -rf .cache/` is a safe reset.
The next run repopulates everything on demand. Don't delete `.fantrax-cache/` (that's the auth session cookie, not
the data cache; deleting it triggers a chromedp browser login on the
next run). In GHA, `.fantrax-cache/` is persisted across all workflows
via the shared `fantrax-session-` cache key so only the first daily run
needs to do a full browser login.

## Automation

GitHub Actions workflows run on daily schedules:

| Workflow | Schedule | Command |
|---|---|---|
| `lineup.yml` | Every hour 8am-7pm PT | `optimize --matchup` |
| `gs-check.yml` | 8am ET daily | `gs-check` |
| `transactions.yml` | 10am ET daily | `transactions` |
| `prospects.yml` | 7am ET daily | `prospects` |
| `waivers.yml` | 9am ET daily | `waivers` (Statcast-driven free-agent picks) |
| `recap.yml` | 7am ET Mondays | `recap-site` (builds every completed week + index, deploys to GitHub Pages) |

All workflows support `workflow_dispatch` for manual triggering. Required repository secrets: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`.

The recap workflow uses `actions/deploy-pages` and needs `permissions: pages: write, id-token: write` (already in the file). No HTML is committed to the repo. To enable Pages: repo Settings → Pages → Source = **"GitHub Actions"**. The site root (`https://<owner>.github.io/<repo>/`) serves the latest matchup week, and the dropdown in the header switches between past weeks (`week-01.html`, `week-02.html`, …).

## Development

```bash
make test         # run all unit tests
make dry-run      # quick local test run (optimize --dry-run only)
make clean-cache  # rm -rf .cache/  (cold-pass baseline before make run-all)
make run-all      # exercise every CLI command in dry-run / read-only mode
```

Tests require no credentials — all network dependencies are mocked via interfaces or test servers.

`make run-all` iterates every command (scoring, optimize, prospects,
gs-check, transactions, waivers, backtest, recap, recap-site) with
`time` on each step, prints the final `.cache/` size, and continues
on errors so one broken step doesn't abort the sweep. It's the
single-command end-to-end smoke test and the easiest way to observe
cache behavior — stderr `cache hit:` / `cache miss:` lines tell you
what each command touched. Run cold-then-warm to see the speedup:

```bash
make clean-cache && make run-all 2>&1 | tee /tmp/cold.log
make run-all 2>&1 | tee /tmp/warm.log
```

**When adding a new CLI command, append a corresponding line to the
`run-all` recipe in the `Makefile`** so the smoke test stays
comprehensive. The convention is: dry-run mode if the command has
side effects, plain invocation otherwise; output written to
`/tmp/<name>` for anything that produces files.

## Architecture

```
cmd/              CLI commands (Cobra)
internal/
  config/         env var loading + validation
  fantrax/        Fantrax API client (public + authenticated)
  projections/    FanGraphs projections, blending, park/matchup adjustments
  optimizer/      pure-function lineup optimization (hitters + pitchers)
  schedule/       MLB Stats API (schedule, lineups, probable pitchers)
  prospects/      minor league prospect monitoring
  waivers/        Statcast-driven MLB free-agent picks (buy-low + hot streaks)
  gscheck/        league-wide GS violation checker
  roster/         roster hygiene alerts
  notify/         Pushover push notifications
  backtest/       grade past lineup moves + projection accuracy
```

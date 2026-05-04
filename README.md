# RosterBot

Fantasy baseball roster automation for Fantrax head-to-head points leagues. Optimizes daily lineups, monitors minor league prospects, and tracks league-wide game start violations.

## What It Does

- **Daily lineup optimization** — Backtracking optimizer finds the globally optimal hitter slot assignment. Pitcher optimizer accounts for probable starters and weekly GS budgets. Blends FanGraphs projections with recent rolling stats so hot/cold streaks factor into decisions.
- **Real-life lineup awareness** — Checks MLB starting lineups so players sitting out (rest days, etc.) get benched in favor of active hitters.
- **Prospect monitoring** — Scans MLB transactions, MiLB performance breakouts, and prospect rankings (MLB Pipeline / FanGraphs) to surface call-up alerts, hot streaks, and upgrade recommendations.
- **Trade monitoring** — Fetches recent league trades, values each side using HKB player rankings, and sends a Pushover notification with the trade report.
- **GS violation detection** — Tallies game starts across all league teams and sends Pushover notifications when a team exceeds the cap.
- **Roster hygiene** — Flags healthy players stuck in IL slots, called-up players still in Minors slots, and injured players occupying active slots.
- **Backtesting** — Grades past lineup moves against the hindsight-optimal lineup and measures projection accuracy against actual fantasy points.

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

# Check GS violations
rosterbot gs-check --dry-run --force

# Backtest last completed matchup week (lineup + projection accuracy)
rosterbot backtest

# Backtest a specific window
rosterbot backtest --dates 2026-04-13:2026-04-19

# Archive today's projections so a future backtest can grade them exactly
rosterbot optimize --dry-run --archive-projections

# Render Sleeper-style HTML recap of the most recently completed matchup week
rosterbot recap --out /tmp/recap.html

# Recap a specific window
rosterbot recap --dates 2026-04-20:2026-04-26 --out /tmp/recap.html

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
| `FANTRAX_COOKIES` | — | Raw `FX_RM` cookie value to skip browser login |
| `PROSPECT_ROLLING_DAYS` | 14 | Days of MiLB stats for breakout detection |
| `PROSPECT_MIN_GAMES` | 8 | Minimum games for prospect breakout eligibility |
| `PROSPECT_RANK_CACHE_HOURS` | 168 | Hours to cache prospect rankings |
| `PROSPECT_UPGRADE_RANK_THRESHOLD` | 20 | Prospect rank threshold for upgrade alerts |
| `PUSHOVER_USER_KEY` | — | Pushover user key for notifications (trades, lineup) |
| `PUSHOVER_GROUP_KEY` | — | Pushover group key for GS violation alerts |
| `PUSHOVER_API_TOKEN` | — | Pushover API token for notifications |
| `BACKTEST_ARCHIVE` | — | Set to `1` to archive every `optimize` run's projections to `.backtest/snapshots/` for later grading (same as `--archive-projections`) |

## Automation

GitHub Actions workflows run on daily schedules:

| Workflow | Schedule | Command |
|---|---|---|
| `lineup.yml` | Every hour 8am-7pm PT | `optimize --matchup` |
| `gs-check.yml` | 8am ET daily | `gs-check` |
| `transactions.yml` | 10am ET daily | `transactions` |
| `prospects.yml` | 7am ET daily | `prospects` |
| `recap.yml` | 11am ET daily | `recap` (publishes to GitHub Pages, only commits when a new matchup week ends) |

All workflows support `workflow_dispatch` for manual triggering. Required repository secrets: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`.

The recap workflow needs `permissions: contents: write` (already in the file) to push the rendered HTML back to `main`. To enable Pages: repo Settings → Pages → Source = "Deploy from a branch" → branch `main`, folder `/docs`. Recaps then live at `https://<owner>.github.io/<repo>/recaps/<season>/week-NN.html`.

## Development

```bash
make test      # run all unit tests
make dry-run   # quick local test run
```

Tests require no credentials — all network dependencies are mocked via interfaces or test servers.

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
  gscheck/        league-wide GS violation checker
  roster/         roster hygiene alerts
  notify/         Pushover push notifications
  backtest/       grade past lineup moves + projection accuracy
```

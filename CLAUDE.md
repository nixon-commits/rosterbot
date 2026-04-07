# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build              # build binary (or: go build -o rosterbot .)
make install            # install to $GOPATH/bin
make test               # run all unit tests (or: go test ./internal/...)
go test ./internal/optimizer/...  # run a specific package's tests
make dry-run            # run optimizer locally without applying changes
go run . optimize --dry-run --dates 2026-04-01  # test a specific date
go run . optimize --dry-run --dates 2026-03-26:2026-03-28  # test a date range
go run . optimize --dry-run --dates all  # test full season from today
go run . optimize --dry-run --matchup    # test remaining days in current matchup period
go run . prospects --dry-run  # run prospect report locally
go run . gs-check --dry-run --force  # check GS violations for most recent period
go run . gs-check --dry-run          # check GS violations (only if yesterday ended a period)
go run . transactions --dry-run      # check recent trades with HKB valuations
```

After making code changes, always run `go vet ./...` and `go mod tidy` to catch issues early. Note: `gofmt` and `go vet` run automatically via PostToolUse hooks on every Edit/Write.

Tests require no credentials — all network dependencies are mocked via interfaces or test servers.

For local dev, create a `.env` file (gitignored) with `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`. Loaded automatically by `godotenv`.

Optional env vars with defaults: `GS_MAX` (0 = no limit) — max game starts per matchup week, used by both the optimizer (weekly GS budget) and gs-check (league-wide violation detection). `GS_MIN` (0 = no minimum) — min game starts per matchup week, used by gs-check to flag teams below the floor. `PROSPECT_ROLLING_DAYS` (14), `PROSPECT_MIN_GAMES` (8), `PROSPECT_RANK_CACHE_HOURS` (168), `PROSPECT_UPGRADE_RANK_THRESHOLD` (20).

GS-check env vars (required only for `gs-check` command): `GS_MAX`, `PUSHOVER_USER_KEY`, `PUSHOVER_API_TOKEN`. Optional: `GS_MIN`.

## Architecture

The optimizer runs as a single binary (`main.go`) with Cobra subcommands (`cmd/`) that wire together four independent packages:

```
fantrax client  ──┐
mlb schedule    ──┼──► optimizer ──► apply lineup (or dry-run print)
fangraphs proj  ──┘
```

**`internal/cache`** — generic TTL file cache using Go generics (`FileCache[T]`). Stores JSON in `.cache/` with a `fetched_at` timestamp envelope. `Get(key, fetchFunc)` returns cached data if fresh, otherwise calls `fetchFunc`, saves, and returns. All I/O errors are non-fatal. TTL of 0 bypasses cache (`--no-cache` flag). Used by FanGraphs projections (12h TTL) and handedness (7d).

**`internal/config`** — loads env vars via `godotenv`, validates that all four required vars are set, and returns a `Config` struct used by the CLI commands to wire everything together.

**`internal/fantrax`** — wraps `github.com/pmurley/go-fantrax` (public read API) and `go-fantrax/auth_client` (authenticated API + lineup writes). Key details:
- `auth_client` uses chromedp (headless Chrome) to log in and obtain a session cookie. Cookie is cached in `.fantrax-cache/`. On first run or cache miss, a browser opens.
- Credentials read from env: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`.
- Alternatively, set `FANTRAX_COOKIES` to the raw `FX_RM` cookie value to skip browser login entirely.
- **Position IDs are numeric strings** (`"001"` = C, `"002"` = 1B, `"003"` = 2B, `"004"` = 3B, `"005"` = SS, `"008"` = INF, `"012"` = OF, `"014"` = UT). These come from the roster API and must be used as-is for slot assignment and eligibility checks.
- This league's active slot names: `C`, `1B`, `2B`, `3B`, `SS`, `INF`, `OF` (×4), `UT` (×3). Mapped in `posNameToID` in `client.go`.
- Scoring group code is `BASEBALL_HITTING` (not `HITTING`).
- **Scoring periods are daily** (period 1 = season opener). Period number = `1 + days since season start`. Matchup data from `GetAllMatchups()` has weekly matchup entries, not daily — don't use it for period lookup.
- **Future lineup apply** requires a two-step confirmation flow: first API call returns a confirmation prompt (`ShowConfirmWindow=true`), second call with the same payload applies the changes. Handled in `ApplyLineup`.

**`internal/projections`** — FanGraphs Steamer projections (primary) with rolling-stats fallback chained via `ChainedSource`. FanGraphs returns **JSON** (not CSV); player name field is `PlayerName`. The `Projection` struct includes derived stats (`Singles`, `XBH`, `TB`) that must be computed from raw fields before scoring. Separate `Source` (hitters) and `PitcherSource` (pitchers) interfaces.

**Blended scoring** — wraps Steamer with recent Fantrax stats (last 10 scoring periods). Falls back to 100% Steamer when no recent data. Recent stats are fetched in parallel via `errgroup` in `fantrax/recent_stats.go`.
- **Hitters** (`BlendedSource`): `0.60 * steamerPtsPerGame + 0.40 * recentFP/G`. `PtsPerGameSource` interface (type assertion) lets the optimizer use pre-computed values.
- **Pitchers** (`PitcherBlendedSource`): role-aware weights — SP: `0.85/0.15`, RP: `0.70/0.30` Steamer/recent. Requires minimum 4 GP before blending. `PitcherPtsPerGameSource` interface.

**`internal/prospects`** — monitors minor league prospects across MLB transactions, MiLB performance breakouts, and prospect ranking sources (MLB Pipeline primary, FanGraphs fallback). Produces a daily prospect report in the GHA job summary with call-up alerts, hot streak detection, free agent watch, and upgrade recommendations. Separate from roster alerts (which detect slot mismatches); this focuses on external data to find new players to pick up. Rankings are cached in `.cache/` (168h default TTL). Breakout detection uses level-adjusted thresholds (AAA/AA/A-ball). Transaction tracking uses a cursor to avoid duplicate alerts across runs.

**`internal/gscheck`** — league-wide GS violation checker. `RunGSCheck` fetches all scoring periods and teams via `getStandings`, iterates every team to tally active-slot pitcher GS for a completed period, detects violations (GS > max or GS < min), and sends a Pushover notification. The `gs-check` CLI command validates that `GS_MAX`, `PUSHOVER_USER_KEY`, and `PUSHOVER_API_TOKEN` are set before running.

**`internal/transactions`** — trade monitor. `CheckTrades` fetches recent Fantrax league trades (last 24 hours) via `GetRecentTrades`, groups them by `TradeGroupID`, values each side using HKB player rankings, and sends a Pushover notification with the trade report. Uses normalized name matching (lowercase, stripped suffixes) to join Fantrax player names to HKB data. Requires `PUSHOVER_USER_KEY` and `PUSHOVER_API_TOKEN` for notifications (skips if not set).

**`internal/notify`** — notification helpers. `SendPushover` sends push notifications via the Pushover API. Self-contained function taking explicit parameters (no config dependency).

**`internal/roster`** — `CheckRoster` scans the full roster for slot mismatches (healthy players in IL, called-up players in Minors, injured/minor-leaguers in active slots). Suppresses alerts when IL/Minors slots are full. Separate from prospect report — this is about current roster hygiene.

**`internal/schedule`** — hits `statsapi.mlb.com` for game schedule and probable pitchers. `TeamsPlayingOn` returns a `map[string]bool` of playing team abbreviations. `ProbableStarters` returns normalized pitcher name → team abbreviation. Both URLs are `var` (not `const`) to allow test overriding.

**`internal/optimizer`** — pure functions, no I/O. Two parallel optimizers:
- **Hitters** (`OptimizeLineup`): backtracking with pruning to find globally optimal slot assignment maximizing total expected points. Checks `PtsPerGameSource` (type assertion) before falling back to `expectedPts`. `EligibleForSlot` in `fantrax/client.go` handles UT (accepts any hitter) and INF (accepts 1B, 2B, 3B, SS — not C).
- **Pitchers** (`OptimizePitcherLineup`): sorts by hasGame → expectedPts → ID, then assigns to slots. Uses probable starter data to determine if SPs start; when no probable data is available (future dates), SPs default to "has game" if their team plays. Accepts an optional `*GSBudget` for weekly game-start limit awareness.

**Scoring model** — this league scores: `1B`, `2B`, `3B`, `HR`, `RBI`, `R`, `BB`, `SB`, `CS`, `HBP`, `SO`, `GIDP`, `XBH`, `TB`, `CYC`. The `expectedPts` function derives `1B = H - 2B - 3B - HR`, `XBH = 2B + 3B + HR`, `TB = 1B + 2×2B + 3×3B + 4×HR` before applying weights.

**GS budget** — weekly game-start limit awareness (`GS_MAX` env var, 0 = disabled). When enabled, the pitcher optimizer gates SP starts to avoid exhausting the weekly GS allocation on low-value starters while better aces pitch later in the matchup week.
- **Matchup week boundaries** derived from `GetAllMatchups()`: consecutive daily scoring periods where the team faces the same opponent form a matchup week. Computed in `fantrax/matchup_weeks.go` via `MatchupWeekBounds`.
- **Past GS counting**: for each past day in the current matchup week, the `ProbableStarters` API is checked to count how many rostered SPs started.
- **Future demand forecasting** uses a hybrid approach: days with confirmed probable starters use exact counts; days without probables estimate `roster SPs whose team plays / 5` (standard 5-man rotation).
- **Proportional gate** (`optimizer/gs_budget.go`): allocates remaining GS proportionally across today and future days (`allowToday = round(remaining * todayStarters / totalDemand)`). When budget is tight, the highest-value starters are kept and lowest-value starters have `IsStarter` flipped to false, applying the existing 0.10x non-starter discount. Uses `eps = 1e-9` for float comparison consistency.
- The gate only applies to today's optimization (the daily GHA run). Future dates in `--dates` ranges are optimized without the gate since each day gets its own run.
- The `--matchup` flag on the optimize command resolves to all remaining days in the current matchup period (from today through the matchup week end).

## Idempotency

The optimizer must produce identical output given the same inputs. Key invariants:
- **Stable sort**: player ranking uses player ID as tiebreaker (`scored[i].Player.ID < scored[j].Player.ID`) so equal-scoring players always appear in the same order.
- **Epsilon comparison**: the backtracking optimizer uses `eps = 1e-9` for floating-point comparison to avoid flip-flopping between equivalent assignments.
- **Minimal changes**: when two assignments produce the same total points (within epsilon), the optimizer prefers the one with fewer roster moves (`changes < bestChanges`).
- **Period-specific roster**: for future dates, the optimizer fetches the roster for that period (`GetHitterRosterForPeriod`) so it sees already-applied lineups. A second run with the same inputs produces "No changes needed".
- **Verification**: after any optimizer change, run the command twice with the same inputs and confirm the second run shows "No changes needed" for all dates.

## Claude Code Agents

Specialized agents are available for this project:

- **`fantasy-baseball-model-auditor`** — audit projection models, scoring systems, and data products for accuracy and validity before deployment.
- **`fantasy-baseball-strategist`** — review and improve the automation codebase: scoring models, lineup optimization, projection blending, scheduling logic, and GHA workflows.
- **`fantasy-baseball-edge-finder`** — strategic analysis, roster optimization insights, and identifying exploitable edges in H2H points leagues (statcast-driven player evaluation, scoring setting exploitation, streaming strategies).

Use the strategist agent after making changes to optimizer logic, blending weights, or scoring models. Use the model auditor when building or updating projection pipelines. Use the edge finder for in-season roster decisions and waiver wire analysis.

## README

When adding new commands, flags, env vars, or changing architecture, update `README.md` to keep it in sync. The README covers user-facing features (commands, flags, setup, configuration) while CLAUDE.md covers internal implementation details.

## GHA

`.github/workflows/lineup.yml` runs daily at 10am UTC (6am ET) and on `workflow_dispatch`. Requires six repository secrets: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`. Optional: `GS_MAX` (game-start max), `GS_MIN` (game-start min). Chrome is installed via `browser-actions/setup-chrome@v2` before the Go run step.

`.github/workflows/gs-check.yml` runs daily at 12pm UTC (8am ET) and on `workflow_dispatch` (with `force` and `dry_run` inputs). Checks league-wide GS violations at period end. Additional secrets: `GS_MAX`, `GS_MIN` (optional).

`.github/workflows/transactions.yml` runs daily at 2pm UTC (10am ET) and on `workflow_dispatch` (with `dry_run` input). Checks recent league trades and sends Pushover notifications with HKB valuations.

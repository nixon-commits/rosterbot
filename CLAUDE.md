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
go run . waivers --dry-run           # Statcast-driven free-agent picks (top 15 by default)
go run . waivers --dry-run --top 25 --positions OF,SP   # bigger list, position filter
go run . backtest                                    # backtest last completed matchup week
go run . backtest --dates 2026-04-13:2026-04-19      # backtest a specific window
go run . backtest --skip-projections                 # lineup-only backtest (faster)
go run . optimize --dry-run --snapshot               # write projection snapshots in dry-run (non-dry-run runs write by default)
go run . recap --out /tmp/recap.html                 # render weekly HTML recap (most recent completed week)
go run . recap --dates 2026-04-20:2026-04-26 --out /tmp/recap.html  # specific window
go run . recap --out /tmp/recap.html --open          # render and auto-open in default browser
go run . recap-site --out dist                       # render every completed week into a static site dir
go run . recap-site --out dist --open                # build site and auto-open dist/index.html
make clean-cache        # rm -rf .cache/  (cold-pass baseline before make run-all)
make run-all            # exercise every command in dry-run / read-only mode + print cache size
```

**`make run-all` is the canonical end-to-end smoke test** — it iterates every CLI command in dry-run / read-only mode with `time` on each step and prints the final `.cache/` size. Use it for two things: (1) a single-command sanity check before pushing changes, and (2) observing cache behavior — stderr `cache hit:` / `cache miss:` lines show what each command touched. **Whenever you add a new top-level CLI command (a new `cmd/<x>.go` registered on `rootCmd`), append a corresponding line to the `run-all` recipe in the `Makefile`** so the smoke target stays comprehensive. Pair with `make clean-cache && make run-all` for a cold pass, then `make run-all` again to see warm-cache behavior.

After making code changes, always run `go vet ./...` and `go mod tidy` to catch issues early. Note: `gofmt` and `go vet` run automatically via PostToolUse hooks on every Edit/Write.

Tests require no credentials — all network dependencies are mocked via interfaces or test servers.

For local dev, create a `.env` file (gitignored) with `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`. Loaded automatically by `godotenv`.

Optional env vars with defaults: `GS_MAX` (0 = no limit) — max game starts per matchup week, used by both the optimizer (weekly GS budget) and gs-check (league-wide violation detection). `GS_MIN` (0 = no minimum) — min game starts per matchup week, used by gs-check to flag teams below the floor. `PROSPECT_ROLLING_DAYS` (14), `PROSPECT_MIN_GAMES` (8), `PROSPECT_RANK_CACHE_HOURS` (168), `PROSPECT_UPGRADE_RANK_THRESHOLD` (20).

GS-check env vars (required only for `gs-check` command): `GS_MAX`, `PUSHOVER_GROUP_KEY`, `PUSHOVER_API_TOKEN`. Optional: `GS_MIN`.

## Architecture

The optimizer runs as a single binary (`main.go`) with Cobra subcommands (`cmd/`) that wire together four independent packages:

```
fantrax client  ──┐
mlb schedule    ──┼──► optimizer ──► apply lineup (or dry-run print)
fangraphs proj  ──┘
```

**`internal/cache`** — generic TTL file cache using Go generics (`FileCache[T]`). Stores JSON in `.cache/` (flat — no subdirs) with a `fetched_at` timestamp envelope. `Get(key, fetchFunc)` returns cached data if fresh, otherwise calls `fetchFunc`, saves, and returns. All I/O errors are non-fatal. TTL of 0 bypasses cache (`--no-cache` flag).

**Caching at a glance**: three TTL tiers map to three mutability classes.

| Tier | TTL | Use for | Examples |
|------|-----|---------|----------|
| `pastPeriodTTL` | 30 d | Past-period snapshots (immutable once the period closes) | `fantrax-roster-stats-<teamID>-<period>`, `fantrax-pitcher-gs-<teamID>-<period>`, `fantrax-recent-stats-{hitter,pitcher}-<teamID>-<period>`, `fantrax-{hitter,pitcher}-roster-<teamID>-<period>` (when period < current), `mlb-schedule-<YYYY-MM-DD>` (past dates), `mlb-player-id-<name>-<team>` |
| `stableTTL` | 7 d | Season-invariant config | `fantrax-{hitter,pitcher}-slots-<leagueID>`, `fantrax-{hitter,pitcher}-scoring-<leagueID>`, `fantrax-season-range-<leagueID>` |
| `todayTTL` | 15 m | "Today, but stable for a window" — drifts during the day, but fine to reuse for an hourly GHA loop or local-dev iteration | `fantrax-{hitter,pitcher}-roster-<teamID>`, `fantrax-current-period-<leagueID>-<YYYY-MM-DD>`, `fantrax-player-pool-<leagueID>`, `fantrax-minors-roster-<teamID>`, `fantrax-available-prospects-<leagueID>`, `fantrax-pending-trades-<leagueID>`, `fantrax-all-trades-<leagueID>` |

A handful of provider-specific TTLs sit outside this scheme: `fangraphs-{bat,pit}-*` (12 h), `mlb-handedness` (7 d), `savant-*` (12 h), `hkb-*` (8 h), `prospect-rankings-*` (168 h via `PROSPECT_RANK_CACHE_HOURS`), `mlb-game-logs-<playerID>-<group>-<season>` (1 h, prospects MiLB game log), `mlb-game-log-<playerID>-<group>-<season>` (1 h, MLB-only game log used by `BackfillDailyFPts`). These predate or sit between the three-tier scheme and use whatever TTL fits the upstream's update cadence.

**Cache key naming**: `<source>-<entity>[-scope...]` where `source` ∈ {`fantrax`, `mlb`, `fangraphs`, `savant`, `hkb`, `prospect`, `mlb-handedness`}, `entity` is the data shape (e.g. `roster-stats`, `pitcher-gs`, `matchups`, `schedule`, `bat`, `pit`), and `scope` is whatever disambiguators apply (teamID, period, leagueID, date). The repeated prefixes are named constants owned by the package that builds them — the whole `fantrax-*` / `mlb-game-log` family in `internal/fantrax/cachekeys.go`, plus `keySavant` (`waivers/savant.go`) and `keyFanGraphs` (`projections/fangraphs.go`) — so a prefix rename lands in one place; genuine single-use keys (`mlb-schedule`, `mlb-handedness`, `mlb-player-id(s)`, `mlb-game-logs`) stay inline literals. Old `backtest-snapshot-mlb-*` files (pre–roster-stats rename), `backtest-snapshot-*` files (StatsType=2 era), and `player-ids.json` (pre-FileCache prospects bulk file) are orphaned and safe to delete.

**`Client.SetCache(cacheDir)`** in `internal/fantrax/client.go` enables the file cache for every cached method on `*fantrax.Client`. `cmd/root.go`'s `initApp` calls it on every command's client unless `--no-cache` is set, so commands inherit caching automatically — no per-command opt-in. When `cacheDir` is empty, every cached helper short-circuits to its uncached `fetchXxx` sibling, which is also how tests stay hermetic. Per-period methods (`GetHitterRosterForPeriod`, `GetRecentStats`, `GetRecentPitcherStats`, etc.) call `c.ttlForPeriod(period)` — past periods get `pastPeriodTTL`, current/future fall back to `todayTTL`. The "current" bound comes from `PeriodForDate(seasonStart, time.Now().UTC())` against the cached `fantrax-season-range`, so there's no extra upstream call to determine the cutoff.

The matchups response is also memoized in-memory per `*fantrax.Client` (via `allMatchups()`) — five different helpers consume the same `GetAllMatchups` payload during a single `recap-site` build, and in-memory is the right scope since the in-progress week mutates intra-day.

**`internal/config`** — loads env vars via `godotenv`, validates that all four required vars are set, and returns a `Config` struct used by the CLI commands to wire everything together.

**`internal/scoring`** — the single home for the stat→fantasy-points algebra. `ApplyHitter`/`ApplyPitcher` take a neutral `HitterLine`/`PitcherLine` of raw counts and apply `Weights` (the `fantrax.ScoringWeights` alias). Pure, imports nothing else in the tree, so `fantrax` (game-log backfill) and `projections` both depend on it without a cycle. See the **Scoring model** section below for the adapter pattern.

**`internal/positions`** — the single source of truth for Fantrax position-ID semantics: ID constants (filling `"003"`/`"008"`, which `auth_client` omits), `SlotName`, `AcceptsINF`, `IsPitcherSlot`, and `HitterBucket` (C>INF>OF>UT reporting precedence). A leaf package (depends only on `auth_client`); `fantrax` and `backtest` import it instead of hardcoding position-ID switches.

**`internal/fantrax`** — wraps `github.com/pmurley/go-fantrax` (public read API) and `go-fantrax/auth_client` (authenticated API + lineup writes). Key details:
- `auth_client` uses chromedp (headless Chrome) to log in and obtain a session cookie. Cookie is cached in `.fantrax-cache/`. On first run or cache miss, a browser opens.
- Credentials read from env: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`.
- **Fantrax API version** — every `/fxpa/req` POST body must include `"v": "<current>"`. The library centralizes this in `fantraxAPIVersion` in `auth_client/fantrax_client.go`. When Fantrax deploys a new version and calls start returning `STALE_CLIENT` (empty `responses` array), probe the API with `curl` to find the new version string — update the constant in the library and also in `gs_check.go`, which builds a `/fxpa/req` payload directly. (`daily_fpts.go` now goes through `auth_client`'s request builder and carries no version of its own.)
- **Position IDs are numeric strings** (`"001"` = C, `"002"` = 1B, `"003"` = 2B, `"004"` = 3B, `"005"` = SS, `"008"` = INF, `"012"` = OF, `"014"` = UT). These come from the roster API and must be used as-is for slot assignment and eligibility checks. Their semantics live in **`internal/positions`** (the single source of truth): ID constants — filling `"003"`/`"008"`, which upstream `auth_client` omits — plus `SlotName`, `AcceptsINF`, `IsPitcherSlot`, and `HitterBucket` (the C>INF>OF>UT reporting precedence). `fantrax` and `backtest` both import it instead of hardcoding switches.
- This league's active slot names: `C`, `1B`, `2B`, `3B`, `SS`, `INF`, `OF` (×4), `UT` (×3). Mapped in `posNameToID` in `client.go` (which references `positions` constants).
- Scoring group code is `BASEBALL_HITTING` (not `HITTING`).
- **Scoring periods are daily** (period 1 = season opener). Period number = `1 + days since season start`. Matchup data from `GetAllMatchups()` has weekly matchup entries, not daily — don't use it for period lookup.
- **Future lineup apply** requires a two-step confirmation flow: first API call returns a confirmation prompt (`ShowConfirmWindow=true`), second call with the same payload applies the changes. Handled in `ApplyLineup`.

**`internal/projections`** — FanGraphs projections via the `--projections` flag. Supported systems: `steamer`, `depthcharts`, `thebatx`, plus their rest-of-season variants `steamer-ros`, `depthcharts-ros`, `thebatx-ros`. Default is `depthcharts`; the bot currently runs on `depthcharts-ros` in season. Wraps the configured system with a rolling-stats fallback chained via `ChainedSource`. FanGraphs returns **JSON** (not CSV); player name field is `PlayerName`. The `Projection` struct includes derived stats (`Singles`, `XBH`, `TB`) that must be computed from raw fields before scoring. Separate `Source` (hitters) and `PitcherSource` (pitchers) interfaces.

**Blended scoring** — wraps the configured FanGraphs source with recent Fantrax stats. "Recent" here is the player's **season-to-date** FP/G: the Fantrax roster API returns cumulative YTD `FantasyPointsPerGame` regardless of the period requested, so `GetRecentStats` reads the latest completed period's snapshot and uses that YTD average (it is *not* a trailing-N-day window). Falls back to 100% base projection when no recent data. Recent stats are fetched in parallel via `errgroup` in `fantrax/recent_stats.go`. Weights are **dynamic** (not fixed) and shift toward recent stats as a player accumulates a stable sample. A backtest-only strategy-replay harness (`backtest --recency-experiment`) compares this YTD signal against rolling-window variants (14d / 30d / half-life decay) by lineup Gap; see `docs/superpowers/specs/2026-06-06-windowed-recency-strategy-replay-design.md`.
- **Hitters** (`BlendedSource`): `seasonWeight = approxPA / (approxPA + 250)` where `approxPA = GP × 3.8`; base weight = `max(1 - seasonWeight, 0.30)`. Equal weight at ~66 GP (mid-June); base never drops below 30%. `PtsPerGameSource` interface (type assertion) lets the optimizer use pre-computed values.
- **Pitchers** (`PitcherBlendedSource`): role-aware stabilization — SP equal weight at 15 GP, RP at 25 GP; base floor 35%. Requires minimum GP before blending. `PitcherPtsPerGameSource` interface.

**`internal/prospects`** — monitors minor league prospects across MLB transactions, MiLB performance breakouts, and prospect ranking sources (MLB Pipeline primary, FanGraphs fallback). Produces a daily prospect report in the GHA job summary with call-up alerts, hot streak detection, free agent watch, and upgrade recommendations. Separate from roster alerts (which detect slot mismatches); this focuses on external data to find new players to pick up. Rankings are cached in `.cache/` (168h default TTL). Breakout detection uses level-adjusted thresholds (AAA/AA/A-ball). Transaction tracking uses a cursor to avoid duplicate alerts across runs.

**`internal/gscheck`** — league-wide GS violation checker. `RunGSCheck` fetches all scoring periods and teams via `getStandings`, iterates every team to tally active-slot pitcher GS for a completed period, detects violations (GS > max or GS < min), and sends a Pushover notification. The `gs-check` CLI command validates that `GS_MAX`, `PUSHOVER_GROUP_KEY`, and `PUSHOVER_API_TOKEN` are set before running.

**`internal/transactions`** — trade monitor. `CheckTrades` fetches recent Fantrax league trades (last 24 hours) via `GetRecentTrades`, groups them by `TradeGroupID`, values each side using HKB player rankings, and sends a Pushover notification with the trade report. Uses normalized name matching (lowercase, stripped suffixes) to join Fantrax player names to HKB data. Requires `PUSHOVER_USER_KEY` and `PUSHOVER_API_TOKEN` for notifications (skips if not set).

**`internal/waivers`** — Statcast-driven waiver wire audit. `Run(ft, today, opts)` fetches the full Fantrax player pool, filters to MLB free agents (FantasyStatus `"FA"` / `"W*"` / empty, excluding `MinorsEligible`), resolves names → MLBAM IDs via `playername.ResolveMLBAMIDs`, and joins each FA against (a) FanGraphs projections (configured system; default `depthcharts`) from `projections.LoadBattingProjections`/`LoadPitcherProjections` for the per-game point projection, and (b) a `SavantBundle` of five Baseball Savant CSVs for signal tagging. Two signals fire per player: **BUY-LOW** when xStats outpace surface stats (hitter: `xwOBA - wOBA ≥ 0.030` AND barrel ≥ 9 AND hard-hit ≥ 42; pitcher: `era - xera ≥ 1.00` AND `xwoba ≤ 0.310`) and **HOT** when recent production is backed by quality (hitter: 14d `wOBA ≥ .380` AND `xwOBA ≥ .360` AND season barrel ≥ 8; pitcher: 30d `era ≤ 3.20` AND `xera ≤ 3.50`). When both fire, the row is tagged `BOTH`. Sample guards: hitter season ≥80 PA, hitter 14d ≥20 PA, pitcher season ≥100 TBF, pitcher 30d ≥50 TBF. Defaults live in `DefaultThresholds()`; tests override. The design separates concerns deliberately — Statcast picks WHO surfaces, FanGraphs scores HOW MUCH — so the league's own `ScoringWeights` (via `ExpectedPtsFromProj`) drive the ranking and FAs without a FanGraphs projection are skipped (accepted v1 limitation). Output: stdout (always), GHA markdown summary (when `GITHUB_STEP_SUMMARY` set), Pushover (when `!DryRun` and creds set; `formatPushover` truncates to fit the 1024-char limit). CSV fetchers in `savant.go` have URLs as `var` for test override; column lookup is by lowercased name with aliases so Savant header drift doesn't break parsing. CSVs are cached at 12h TTL via `cache.FileCache[T]` keyed by year + window-end-date.

**`internal/backtest`** — grades past work against actual outcomes. Two analyses:
- **Lineup grading**: for each past day, computes an actual-points total (sum of FPts for active-slot players) and a hindsight-optimal total (the existing optimizer run against a `hindsightSource` that returns each player's actual FPts as pts-per-game via the `PtsPerGameSource` / `PitcherPtsPerGameSource` interfaces). `Gap = actual - optimal`; negative means points left on bench. SP-eligible pitchers who actually appeared are fed to the optimizer as "probable starters" so the 0.10x non-starter discount doesn't apply in hindsight.
- **Projection grading**: checks `.backtest/snapshots/<YYYY-MM-DD>.json` for archived per-player projection values written by `optimize`. Each snapshot row (`backtest.SnapshotPlayer`) records the projected pts/game plus look-back fields — `was_started`, `slot`, `locked`, `role`, and `eligibility` (position IDs) — so error can be sliced by position and lineup decision. When present, grading compares against actual FPts for an overall MAE/Bias/RMSE report, a per-position MAE table (buckets C/INF/OF/UT for hitters via eligibility precedence C>INF>OF>UT, and SP/RP for pitchers via role), and the top-10 signed-error misses (ordered most-over-projected first so ramp-up patterns surface). When a day's snapshot is absent, the day is marked `source="missing"`. When a snapshot exists but its `generated_at` falls on a different UTC calendar day than the date it projects — a `--matchup` pre-write that was never refreshed on the day itself (e.g. every hourly run that day failed) — the day is marked `source="stale"` and excluded from the rollup, since grading a multi-day-old forecast inflates apparent projection error (`sameUTCDate` guard in `backtest.go`; a zero `generated_at` predates the field and is graded as before). `FormatReport` prints an "Excluded from grading: N stale, M missing" line listing the dates, so a window thinned by stale/missing days is visible rather than silently shrinking the sample. The hourly `lineup.yml` workflow runs `optimize` (which writes snapshots by default on non-dry-run) and persists `.backtest/snapshots/` in the GHA cache (alongside `.cache`), so production accumulates a snapshot per day from the most recent hourly run. Local backtest runs that need the snapshots have to either (a) replay the workflow's cache or (b) run `optimize --archive-projections` themselves.

Per-day FPts come from `fantrax.DailyFantasyPoints` (in `internal/fantrax/daily_fpts.go`), which walks a period range and diffs consecutive YTD snapshots via `playerStatsFromTables`. This covers both `scGroup=10` (hitting) and `scGroup=20` (pitching). The Fantrax roster API is requested with `StatsType=1` (MLB stats — real per-player season totals) rather than `StatsType=2` (Fantasy Team — team-credited only) so reserve players' production is visible to hindsight callers; without this, the optimal lineup calc in the backtest/recap can never recommend a benched player and efficiency % stays artificially compressed (~95–99% across the league).

Players seen for the first time in the window have their `(deltaFP, deltaGP)` zeroed so the cumulative pre-window YTD doesn't leak in as same-day production (a waiver pickup with a 200-FPts season-to-date YTD would otherwise inject 200 phantom points on the day they appear). The zeroed row is flagged via `DayPlayerFP.NeedsBackfill`; `Client.BackfillDailyFPts` (in `internal/fantrax/mlb_backfill.go`) resolves the flag by fetching the player's MLB statsapi game log for that date and computing FPts from raw stats × the league's `ScoringWeights` — bounded to a handful of API calls per recap week, soft-fails to "leave zero" on any error. Two-way crossings (Ohtani moving between the hitters and pitchers tables) are handled the same way: `diffYTD` detects the `prevOther` fallback (role-specific YTDs can't be meaningfully subtracted), zeroes the diff, sets `NeedsBackfill`, and the backfill computes the role-correct same-day FPts. Both fixes verified against the Fantrax-authoritative `Matchup.HomeTeam.Points` for week 8 (recap matched all 10 team totals exactly). Per-period snapshots are cached at `.cache/fantrax-roster-stats-<teamID>-<period>.json` with a 30-day TTL since past periods are immutable. A parallel cache `.cache/fantrax-pitcher-gs-<teamID>-<period>.json` (same TTL) backs `GetTeamPitcherStarts`, used by the recap to enumerate every active-slot SP start; it's optional (pass `cacheDir=""` / `cacheTTL=0` to bypass, which the live `gs-check` path does because it always wants fresh data), and when the cache hits, the per-day 200ms upstream-throttle in `GetTeamPitcherStarts` is skipped.

Snapshot writing is default-on for real (non-dry-run) `optimize` runs so the cron accumulates backtest data automatically. Dry-run stays side-effect-free unless you opt in with `--snapshot` (the older `--archive-projections` flag and `BACKTEST_ARCHIVE=1` env var are kept as backward-compatible aliases). Snapshots are rewritten if the same date is optimized twice — last write wins, which is fine for the hourly lineup workflow: the final write of the day captures the most recent projection state for that date, and is the version `backtest` grades against. If that final same-day write never lands (the day's runs all failed), the snapshot is left as an earlier `--matchup` pre-write and the stale-snapshot guard above excludes it from grading.

**`internal/recap`** — Sleeper-style weekly recap. `recap.Run(ft, opts)` aggregates all 12 (or however many) teams in parallel via `errgroup`: for each team it pulls `DailyFantasyPoints` for the matchup week and runs `backtest.RunLineupAnalysis` to compute actual + hindsight-optimal totals, plus `GetTeamPitcherStarts` (a sibling to `GetTeamGS` in `internal/fantrax/pitcher_starts.go`) to enumerate every active-slot SP start with its FPts. H2H pairings come from `GetAllMatchupEntries` (a passthrough wrapper added on `*fantrax.Client`); team weekly scores are aggregated from daily FPts (deterministic, doesn't depend on parsing the upstream `MatchTeam.Total`). Award functions in `awards.go` are pure and unit-tested. **League Leaders** (`leaders.go`, `buildLeaders`) ranks every rostered player league-wide (from `GetFullPlayerPool`, filtered to `FantasyTeamID != ""`) by season-to-date wOBA (hitters) and FIP (pitchers): names → MLBAM IDs via `playername.ResolveMLBAMIDs`, wOBA joined from `waivers.LoadSavant`'s qualified-hitter expected-stats CSV, FIP computed from a batched MLB statsapi season-pitching fetch (`mlbSeasonPitchingURL`, `parseIP` handles `.1`/`.2` thirds) with the FIP constant solved from the rostered-pitcher pool so displayed values land near league average (ranking is constant-invariant). Pitchers need ≥`fipMinIP` (30) innings. The whole path soft-fails to nil — the section is `{{if}}`-guarded out when empty, so archived/credential-less renders stay clean. The single-day leaderboards and league-leaders board both badge each row with the owning team's logo (`PlayerLine.OwnerTeamID` / `LeaderLine.OwnerTeamID` → `teamLogo`) instead of a rank number (rank implied by order); default board size is top 5 (`--top`). The renderer (`render.go` + embedded `template.html`) emits a single self-contained HTML file via `Render` (no nav) or `RenderSite` (with a cross-week dropdown). `recap.RunSite(ft, sopts)` (`internal/recap/site.go`) drives the multi-week build: it enumerates every completed matchup week via `GetMatchupWeekByNumber`, calls `Run` for each, and writes `dist/week-NN.html` plus `dist/index.html` (mirror of the latest week). Each page carries a `<select>` dropdown of all weeks. The `recap-site` CLI command exposes this for the GitHub Pages workflow.

**Week-completion signal** — `completedMatchupWeeks` and the `recap` command's range resolver both treat a week as renderable when `weekEnd < today` OR `weekEnd == today AND ft.IsMatchupWeekFinal(n)`. The latter delegates to `MatchupWeekIsFinal` in `internal/fantrax/matchup_weeks.go`, which checks the upstream `Matchup.HomeTeam.Points` / `.AwayTeam.Points` fields: Fantrax flips the schedule row from the 4-cell "future/in-progress" format (only `Total` set) to the 8-cell "completed" format (Points/Adjustment/Total all populated) right after the last MLB game of the week ends. So `Points > 0` for either side is a reliable "Fantrax has officially closed the week" signal, available the same evening the last game finishes — no schedule-API round-trip needed.

**WP model** — `wp.go` exposes `ComputeWPCurve`, `LeadChangeCount`, `MinWinnerWP`. `ComputeWPCurve` is a Go port of the formula Fantrax runs client-side in its live-scoring Angular bundle (`models.CalculateWinProbability` in go-fantrax): deterministic, no Monte Carlo, no sigma. At each of 8 points (pre-week + 7 day-ends) it feeds cumulative actual FPts, a **live-adjusted** weekly projection (`actual_so_far + remaining_days × HomeMeanDaily`, mirroring how Fantrax recomputes `calculatedProjectedTotalsMap` intra-week — without the live adjustment the projection ratio stays flat all week and the chart degenerates to 50% then a snap to 100%), and a uniform `timeLeft = 7 - i` for both teams into the Fantrax formula. Pre-week WP[0] reflects projection ratio (a 60/40 projected favorite starts at ~84/16, not 50/50). Final WP[7] is always 100/0 or 0/100 — on an exact-tie final the Fantrax formula awards the win to "away" (faithful port; ties never happen in practice with float FPts).

**Game of the Week** — always featured at the top of every week (no skip for quiet weeks). Picked by `GameOfTheWeek(curves, matchups)` using `score = LeadChanges + (1 - minWinnerWP)` — the "weeeeeh factor": back-and-forth swings plus how deep the eventual winner sank mid-week. Ties broken by smallest final margin then home `TeamID` asc. Ties (no winner) score with the comeback term zeroed. The hero chart is rendered as a 380×140 SVG with mirrored 100/75/50/75/100% y-axis labels, half tints (green=home favored on top, red=away favored on bottom), team name labels in their respective halves, and dated x-axis ticks. The matchup-results list marks the chosen game with a ⭐ badge.

**Comeback award** — winner whose mid-week WP dropped below `comebackThreshold = 0.30` (eventual-winner's minimum during `Points[1..6]`). Feeds into `AggregateSeasonAwards`. Game of the Week is intentionally excluded from the season leaderboard — it shows at the top of each week's page, so cumulative counts would be redundant.

**`internal/notify`** — notification helpers. `SendPushover` sends push notifications via the Pushover API. Self-contained function taking explicit parameters (no config dependency).

**`internal/roster`** — `CheckRoster` scans the full roster for slot mismatches (healthy players in IL, called-up players in Minors, injured/minor-leaguers in active slots). Suppresses alerts when IL/Minors slots are full. Separate from prospect report — this is about current roster hygiene.

**`internal/schedule`** — hits `statsapi.mlb.com` for game schedule and probable pitchers. `TeamsPlayingOn` returns a `map[string]bool` of playing team abbreviations. `ProbableStarters` returns normalized pitcher name → team abbreviation. Both URLs are `var` (not `const`) to allow test overriding.

**`internal/optimizer`** — pure functions, no I/O. Two parallel optimizers:
- **Hitters** (`OptimizeLineup`): backtracking with pruning to find globally optimal slot assignment maximizing total expected points. Checks `PtsPerGameSource` (type assertion) before falling back to `expectedPts`. `EligibleForSlot` in `fantrax/client.go` handles UT (accepts any hitter) and INF (accepts 1B, 2B, 3B, SS — not C).
- **Pitchers** (`OptimizePitcherLineup`): sorts by hasGame → expectedPts → ID, then assigns to slots. Uses probable starter data to determine if SPs start; when no probable data is available (future dates), SPs default to "has game" if their team plays. Accepts an optional `*GSBudget` for weekly game-start limit awareness.

**Scoring model** — this league scores: `1B`, `2B`, `3B`, `HR`, `RBI`, `R`, `BB`, `SB`, `CS`, `HBP`, `SO`, `GIDP`, `XBH`, `TB`, `CYC`. The stat→points algebra lives in **`internal/scoring`** (the single source): `ApplyHitter`/`ApplyPitcher` take a neutral `HitterLine`/`PitcherLine` of raw counts, derive `1B = H - 2B - 3B - HR` (floored at 0), `XBH = 2B + 3B + HR`, `TB = 1B + 2×2B + 3×3B + 4×HR`, and apply the league weights. The package imports nothing else (so both `fantrax` and `projections` can depend on it without a cycle); `fantrax.ScoringWeights` is a type alias for `scoring.Weights`. Callers adapt their source to a stat line: `projections.ExpectedPtsFromProj`/`PitcherExpectedPtsFromProj` build a line from a projection and divide by games (scoring is linear, so per-game = total/G); the `mlb_backfill` game-log scorers build a line and don't divide (single game). `optimizer` and `waivers` consume these rather than re-implementing the math.

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

**Auth in GHA** — all six workflows install Chrome via `browser-actions/setup-chrome@v2` and restore/save `.fantrax-cache/` under the shared `fantrax-session-` cache key prefix. The first workflow that runs each day does a full chromedp browser login (15–20 s) and writes the session to the GHA cache; subsequent workflows restore the cached cookie and skip the browser. No `FANTRAX_COOKIES` secret is needed or used — the env var short-circuits the library's fallback chain and bypasses the browser login.

`.github/workflows/lineup.yml` runs hourly during the active window — `cron: '0 14-23 * * *'` (6am–3pm PT) and `'0 0-3 * * *'` (4pm–7pm PT) — plus `workflow_dispatch`. Requires six repository secrets: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`. Optional: `GS_MAX` (game-start max), `GS_MIN` (game-start min). Invokes `optimize --matchup --archive-projections` so each run also writes `.backtest/snapshots/<YYYY-MM-DD>.json`. Uses `actions/cache@v4` with key prefix `projections-` and a multi-path config (`.cache` + `.backtest/snapshots`), so snapshots persist across runs and accumulate one per day.

`.github/workflows/prospects.yml` runs daily at 11am UTC (7am ET) and on `workflow_dispatch`. Runs `prospects` to surface call-up alerts, hot streaks, and upgrade recommendations; pipes `[HIGH ...]` lines into a Pushover notification when present. Uses `actions/cache@v4` with key prefix `projections-`.

`.github/workflows/gs-check.yml` runs daily at 12pm UTC (8am ET) and on `workflow_dispatch` (with `force` and `dry_run` inputs). Checks league-wide GS violations at period end. Additional secrets: `GS_MAX`, `GS_MIN` (optional).

`.github/workflows/transactions.yml` runs daily at 2pm UTC (10am ET) and on `workflow_dispatch` (with `dry_run` input). Checks recent league trades and sends Pushover notifications with HKB valuations. Uses `actions/cache@v4` with key prefix `transactions-` (falls back to `projections-`) so HKB rankings warm-start from neighboring runs.

`.github/workflows/waivers.yml` runs daily at 1pm UTC (9am ET) and on `workflow_dispatch` (with `dry_run` and `top` inputs). Calls `waivers` to surface Statcast-driven free-agent pickups; sends Pushover when not in dry-run. Same secrets as `transactions.yml`. Uses `actions/cache@v4` with key prefix `waivers-` (falls back to `projections-`) so the FanGraphs JSON and Savant CSVs survive across runs.

`.github/workflows/recap.yml` runs Mondays at 11am UTC (7am ET) and on `workflow_dispatch`. Calls `recap-site --out dist` to build the full site (every completed week + index.html), uploads `dist/` via `actions/upload-pages-artifact@v3`, and deploys with `actions/deploy-pages@v4`. No HTML is committed back to the repo. Needs `permissions: pages: write, id-token: write` and the repo's Pages source set to "GitHub Actions" (Settings → Pages → Source). The Pushover notification uses `steps.deployment.outputs.page_url` so the link always points at the live site root.

`.github/workflows/backtest.yml` runs Mondays at 12pm UTC (after `recap.yml`) and on `workflow_dispatch` (with an optional `dates` input). Runs `backtest` (lineup + projection grading of the just-completed week) then `backtest --recency-experiment` (hitter recency-strategy comparison). Restores the shared `projections-` cache (`.cache` + `.backtest/snapshots`) so projection grading has snapshot data — **that data only exists in the GHA cache** (written by the hourly `lineup.yml`), so the backtest must run in CI to grade it. Read-only; results land in the job log, no Pushover.

## Agent skills

### Issue tracker

Issues and PRDs are tracked as local markdown files under `.scratch/<feature-slug>/`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default canonical vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`), recorded as a `Status:` line in each issue file. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.

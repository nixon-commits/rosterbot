# CLAUDE.md

## Commands

```bash
go build ./...          # build all packages
go test ./internal/...  # run all unit tests (no network required)
go test ./internal/optimizer/...  # run a specific package's tests
go run ./cmd --dry-run  # run locally without applying changes
go run ./cmd --dry-run --dates 2026-04-01  # test a specific date
go run ./cmd --dry-run --dates 2026-03-26:2026-03-28  # test a date range
go run ./cmd --dry-run --dates all  # test full season from today
```

Tests require no credentials — all network dependencies are mocked via interfaces or test servers.

For local dev, create a `.env` file (gitignored) with `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`. Loaded automatically by `godotenv`.

## Architecture

The optimizer runs as a single binary (`cmd/main.go`) that wires together four independent packages:

```
fantrax client  ──┐
mlb schedule    ──┼──► optimizer ──► apply lineup (or dry-run print)
fangraphs proj  ──┘
```

**`internal/config`** — loads env vars via `godotenv`, validates that all four required vars are set, and returns a `Config` struct used by `cmd/main.go` to wire everything together.

**`internal/fantrax`** — wraps `github.com/pmurley/go-fantrax` (public read API) and `go-fantrax/auth_client` (authenticated API + lineup writes). Key details:
- `auth_client` uses chromedp (headless Chrome) to log in and obtain a session cookie. Cookie is cached in `.fantrax-cache/`. On first run or cache miss, a browser opens.
- Credentials read from env: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`.
- Alternatively, set `FANTRAX_COOKIES` to the raw `FX_RM` cookie value to skip browser login entirely.
- **Position IDs are numeric strings** (`"001"` = C, `"002"` = 1B, `"003"` = 2B, `"004"` = 3B, `"005"` = SS, `"008"` = INF, `"012"` = OF, `"014"` = UT). These come from the roster API and must be used as-is for slot assignment and eligibility checks.
- This league's active slot names: `C`, `1B`, `2B`, `3B`, `SS`, `INF`, `OF` (×4), `UT` (×3). Mapped in `posNameToID` in `client.go`.
- Scoring group code is `BASEBALL_HITTING` (not `HITTING`).
- **Scoring periods are daily** (period 1 = season opener). Period number = `1 + days since season start`. Matchup data from `GetAllMatchups()` has weekly matchup entries, not daily — don't use it for period lookup.
- **Future lineup apply** requires a two-step confirmation flow: first API call returns a confirmation prompt (`ShowConfirmWindow=true`), second call with the same payload applies the changes. Handled in `ApplyLineup`.

**`internal/projections`** — FanGraphs Steamer projections (primary) with rolling-stats fallback chained via `ChainedSource`. FanGraphs returns **JSON** (not CSV); player name field is `PlayerName`. The `Projection` struct includes derived stats (`Singles`, `XBH`, `TB`) that must be computed from raw fields before scoring.

**Blended scoring** — `BlendedSource` in `projections/blended.go` wraps Steamer with recent Fantrax stats (last 10 scoring periods). Formula: `0.60 * steamerPtsPerGame + 0.40 * recentFP/G`. Falls back to 100% Steamer when no recent data. The `PtsPerGameSource` interface (type assertion, not on `Source`) lets the optimizer use pre-computed blended values. Recent stats are fetched in parallel via `errgroup` in `fantrax/recent_stats.go`.

**`internal/schedule`** — hits `statsapi.mlb.com` for today's game schedule. Returns a `map[string]bool` of playing MLB team abbreviations. The URL is a `var` (not `const`) to allow test overriding.

**`internal/optimizer`** — pure functions, no I/O. `OptimizeLineup` uses backtracking with pruning to find the globally optimal slot assignment that maximizes total expected points. Checks `PtsPerGameSource` (type assertion) before falling back to `expectedPts`. `EligibleForSlot` in `fantrax/client.go` handles UT (accepts any hitter) and INF (accepts any infield position ID).

**Scoring model** — this league scores: `1B`, `2B`, `3B`, `HR`, `RBI`, `R`, `BB`, `SB`, `CS`, `HBP`, `SO`, `GIDP`, `XBH`, `TB`, `CYC`. The `expectedPts` function derives `1B = H - 2B - 3B - HR`, `XBH = 2B + 3B + HR`, `TB = 1B + 2×2B + 3×3B + 4×HR` before applying weights.

## Idempotency

The optimizer must produce identical output given the same inputs. Key invariants:
- **Stable sort**: player ranking uses player ID as tiebreaker (`scored[i].Player.ID < scored[j].Player.ID`) so equal-scoring players always appear in the same order.
- **Epsilon comparison**: the backtracking optimizer uses `eps = 1e-9` for floating-point comparison to avoid flip-flopping between equivalent assignments.
- **Minimal changes**: when two assignments produce the same total points (within epsilon), the optimizer prefers the one with fewer roster moves (`changes < bestChanges`).
- **Period-specific roster**: for future dates, the optimizer fetches the roster for that period (`GetHitterRosterForPeriod`) so it sees already-applied lineups. A second run with the same inputs produces "No changes needed".
- **Verification**: after any optimizer change, run the command twice with the same inputs and confirm the second run shows "No changes needed" for all dates.

## GHA

`.github/workflows/lineup.yml` runs daily at 10am UTC (6am ET) and on `workflow_dispatch`. Requires six repository secrets: `FANTRAX_USERNAME`, `FANTRAX_PASSWORD`, `FANTRAX_LEAGUE_ID`, `FANTRAX_TEAM_ID`, `FANTRAX_IL_SLOTS`, `FANTRAX_MINORS_SLOTS`. Chrome is installed via `browser-actions/setup-chrome@v2` before the Go run step.

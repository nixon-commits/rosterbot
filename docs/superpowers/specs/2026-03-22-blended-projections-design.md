# Blended Projections Design

## Problem

The optimizer uses FanGraphs Steamer season-long projections to rank players. These projections don't account for recent performance — a player on a hot streak gets the same score as one in a slump. We want to blend Steamer with recent Fantrax scoring data to better reflect current form.

## Design

### Blend Formula

```
finalPtsPerGame = 0.60 * steamerPtsPerGame + 0.40 * recentFP/G
```

- **Steamer weight: 60%** — long-term talent baseline
- **Recent weight: 40%** — last 10 scoring periods (days) from Fantrax
- **Fallback**: 100% Steamer when a player has 0 games in the rolling window (new to roster, injured, early season)

### Data Source for Recent Stats

Fantrax scoring periods — each period = one day's entire game slate (187 periods in the 2026 season). We fetch the roster for each of the last 10 periods via `GetTeamRosterInfo(period, teamID)`. Each response includes `BattingStats.FantasyPointsPerGame` and `BattingStats.GamesPlayed` per player for that period.

For a single-day period where `GamesPlayed=1`, FP/G equals total FP for that day. When `GamesPlayed=0`, both fields will be `nil` (they are pointer types `*float64` and `*int`).

Recent FP/G = sum of non-nil FP values across 10 periods / sum of non-nil GP values across 10 periods.

**Verification needed**: Before implementing, make a test call to `GetTeamRosterInfo` with a specific historical period and confirm that `FantasyPointsPerGame` represents that single period's stats, not cumulative season-to-date. The column key `fptsPerGame` is a display-calculated field which should be per-period when scoped to a single period, but this must be verified.

### Architecture

#### New files

**`internal/fantrax/recent_stats.go`**
- `RecentStat` struct: `{TotalFP float64, GamesPlayed int}`
- `GetRecentStats(currentPeriod, numPeriods int) (map[string]RecentStat, error)` — method on `*Client`
- Uses `errgroup.Group` to fire `numPeriods` goroutines in parallel, each calling `c.auth.GetTeamRosterInfo(strconv.Itoa(period), c.teamID)` for one period
- Aggregates per-player (keyed by player ID): sum of FP and count of games played, with nil-safe pointer derefs on `FantasyPointsPerGame` and `GamesPlayed`
- Skips periods ≤ 0 (early season with fewer than 10 periods elapsed)
- On partial failure: if some period calls fail, use data from successful calls rather than failing entirely. Log warnings for failed periods.

**`internal/projections/blended.go`**
- `BlendedSource` struct with fields:
  - `inner Source` (Steamer)
  - `recentStats map[string]RecentStat` (keyed by player ID)
  - `scoring ScoringWeights`
  - `nameToID map[string]string` (normalized player name → Fantrax player ID)
- `GetProjection(name, mlbTeam)` — delegates to inner source
- Implements blended FP/G computation:
  - Look up Steamer projection via inner source, compute `expectedPts` for steamer FP/G
  - Look up player ID via `nameToID` using `normalizeName(name)`, then find recent stats
  - If recent games > 0: return `0.60 * steamer + 0.40 * (totalFP / gamesPlayed)`
  - If recent games == 0: return steamer FP/G only

#### Modified files

**`internal/optimizer/lineup.go`**
- `scoreRoster`: after getting projection, attempt type assertion on `projSrc` to a `PtsPerGameSource` interface (`GetPtsPerGame(name, mlbTeam string, scoring ScoringWeights) (float64, bool)`). If the assertion succeeds and returns `ok`, use that value directly as `ExpectedPts`. Otherwise fall back to `expectedPts(proj, scoring)` as today.
- This avoids polluting the core `Source` interface — only `BlendedSource` implements `PtsPerGameSource`.

**`cmd/main.go`**
- After fetching roster, call `c.auth.GetCurrentPeriod()` (existing method on `auth_client.Client`) to get current period number
- Call `ft.GetRecentStats(currentPeriod, 10)` to fetch last 10 periods in parallel
- Build normalized name→ID mapping from roster
- Construct `BlendedSource` wrapping `FanGraphsSource`, recent stats, scoring weights, and name mapping
- Pass `BlendedSource` to optimizer (replaces plain `FanGraphsSource` in the chain)

### Period Discovery

Use `auth_client.Client.GetCurrentPeriod()` which already exists and returns `(int, error)` via the public API. Count backwards: `currentPeriod-1` through `currentPeriod-10`.

Periods ≤ 0 are skipped (handles early season gracefully — if only 3 periods have elapsed, we use 3 periods of data).

### Interface Design

Rather than adding `GetPtsPerGame` to the `Source` interface (which would require all implementations to add a no-op method), define a separate interface:

```go
// PtsPerGameSource can provide a pre-computed points-per-game value.
type PtsPerGameSource interface {
    GetPtsPerGame(name, mlbTeam string, scoring fantrax.ScoringWeights) (float64, bool)
}
```

Only `BlendedSource` implements this. The optimizer does a type assertion:

```go
if pps, ok := projSrc.(projections.PtsPerGameSource); ok {
    if pts, ok := pps.GetPtsPerGame(p.Name, p.MLBTeam, scoring); ok {
        // use pts directly
    }
}
```

Existing sources (`FanGraphsSource`, `RollingSource`, `ChainedSource`) are unchanged.

### Edge Cases

- **Before season starts**: `GetCurrentPeriod()` returns 0 or 1 → no historical periods to fetch → 100% Steamer
- **First 10 days of season**: Fewer than 10 periods available → use however many exist
- **Player didn't play in any of the 10 periods**: 100% Steamer fallback
- **Player not in Steamer projections**: type assertion returns `false`, falls back to `expectedPts` which returns 0 (existing behavior)
- **FanGraphs unavailable**: `BlendedSource` still needs Steamer for the 60% weight — if FanGraphs is down, recent-only with 100% weight on recent FP/G, or fall back to 0 (match existing degradation path)
- **Nil pointer stats**: `FantasyPointsPerGame` and `GamesPlayed` are `*float64` and `*int` — skip entries where either is nil
- **Player traded mid-window**: They may appear on your roster for some periods but not others — only count periods where they appear in the response
- **Partial API failures**: Log warning, use data from successful period calls

### Performance

- 10 API calls fired in parallel via `errgroup.Group`
- Each call is ~1-2 seconds → total wall time ~2 seconds for all 10
- Historical period data never changes → future optimization: cache to disk

### RollingSource

The existing `RollingSource` and `ChainedSource` are not modified and remain available as a fallback chain for `GetProjection`. `BlendedSource` wraps the `ChainedSource` (which chains FanGraphs → RollingSource), so the existing fallback behavior is preserved for projection lookups. The blended FP/G is an additional layer on top.

# Live Fantrax GS Position Limits — Design

**Date:** 2026-07-06
**Status:** Approved (pending spec review)

## Problem

The GS budget gate (`internal/optimizer/gs_budget.go`, wired from `cmd/optimize.go`) and the
league-wide violation checker (`internal/gscheck`) both treat the "Games Started - Pitching (GS)"
position limit as a flat constant — the `GS_MAX`/`GS_MIN` env vars (currently 12/10) — applied
identically to every matchup/scoring period regardless of length.

Fantrax itself does **not** apply a flat limit. It scales the real configured min/max whenever a
scoring period is merged across more than one calendar week (season opener, All-Star break, etc.).
This was confirmed by directly querying Fantrax's `getTeamRosterInfo` endpoint with
`view=GAMES_PER_POS` (the same call the "Min/Max" tab of the Team Roster screen makes) for three
periods:

| Period | Span | Real Fantrax GS min/max | rosterbot's `GS_MIN`/`GS_MAX` |
|---|---|---|---|
| 1 (season opener) | Mar 25–Apr 5, 12 days | **17 / 21** | 10 / 12 |
| 15 (normal week) | Jul 6–12, 7 days | 10 / 12 | 10 / 12 ✓ |
| 16 (All-Star break) | Jul 13–26, 14 days | **15 / 19** | 10 / 12 |

Static `GS_MAX`/`GS_MIN` has therefore already been silently wrong once this season (period 1) and
is about to be wrong again for period 16 (Jul 13–26). Concretely:

- `applyGSGate` starts suppressing (benching) otherwise-good SP starts once `Used` hits the
  configured max — at 12 instead of the real 21 (period 1) or 19 (period 16), leaving real points
  on the bench.
- `gscheck.RunGSCheck` flags false "OVER MAX" violations (and would miss real "UNDER MIN"
  violations) for any team whose real GS count falls between the static and real thresholds.

**Goal:** fetch the real per-period min/max directly from Fantrax instead of guessing via a static
env var, so this class of bug can't recur for any future irregular-length period.

## Non-goals

- Not building general position-limit support (C/1B/2B/... limits) into rosterbot — this league
  only configures a limit on the GS category; the other position rows are all "No min"/"No max".
  The go-fantrax library addition exposes both tables (see below) for general reuse, but rosterbot
  only consumes the GS row.
- Not removing `GS_MAX`/`GS_MIN` — they become a fallback, not dead config (see Error handling).
- Not touching the large uncommitted `add-discovered-endpoints` branch in go-fantrax — this ships
  as its own small, independent PR off `main`.

## Decisions (from brainstorming)

| Decision | Choice |
|---|---|
| go-fantrax branch base | Fresh branch off `main` (small standalone PR, not the big WIP branch) |
| go-fantrax library scope | Expose both tables: per-position games-played *and* per-category (GS) min/max |
| rosterbot GS_MAX/GS_MIN fate | Kept as **fallback only**, used when the live fetch errors |

## Architecture — go-fantrax (new PR, branched off `main`)

New file `auth_client/get_team_roster_position_counts.go`, using `main`'s existing
`buildFullRequest`/`readBody` helpers (no new request-building machinery needed) and following the
`get_standings.go` convention of raw-response → `Process...` → clean typed result (no separate
`parser` package required):

```go
func (c *Client) GetTeamRosterPositionCounts(teamID, scoringPeriod string) (*GamesPerPosition, error)

type PositionCount struct {
    Name, ShortName string
    GP              int
    Min, Max        *int // nil = "No min" / "No max"
}
type CategoryLimit struct {
    Category string // e.g. "Games Started - Pitching (GS)"
    Total    int
    Min, Max *int
}
type GamesPerPosition struct {
    Positions      []PositionCount
    CategoryLimits []CategoryLimit
}
```

Request: `getTeamRosterInfo` with `data: {leagueId, teamId, scoringPeriod, view: "GAMES_PER_POS"}`.
Response has two relevant tables under `responses[0].data`:

- `gamePlayedPerPosData.tableData[]` → `{pos, posShort, gp, min, max}` (per fielding position)
- `scMinMaxData.tableData[]` → `{scoringCategory, total, min, max}` (per scoring category)

Both tables' `min`/`max` fields are JSON strings that are either a numeric string (`"10"`) or the
literal sentinel `"No min"` / `"No max"`. A shared `parseMinMax(s string) *int` helper
(`strconv.Atoi`, nil on failure) normalizes both.

`scoringPeriod` here is the **weekly** Scoring Period numbering from `getStandings?view=SCHEDULE`
(the same numbering `internal/fantrax.GetScoringPeriodsAndTeams` already parses as `ScoringPeriod.Number`,
and the same numbering as Fantrax matchup weeks — verified period 16 and matchup week 16 are both
exactly Jul 13–26). This is a *different* numbering scheme from the *daily* lineup-locking period
(`PeriodForDate`/`AnchorPeriodForDate`, subject to the known mid-season drift noted in
`period-drift-2026` memory) — no drift risk here.

## Architecture — rosterbot integration

`internal/fantrax` gains a cached wrapper:

```go
// GetGSLimits returns the real Fantrax-configured min/max for the
// "Games Started - Pitching (GS)" category for the given team+period.
func (c *Client) GetGSLimits(teamID string, period int) (min, max *int, err error)
```

Cached under `fantrax-gs-limits-<teamID>-<period>`, using the existing `ttlForPeriod`-style split
(past periods → `pastPeriodTTL`, current/future → `todayTTL`) — consistent with every other
per-period cache entry in the codebase.

**Call sites:**

- `cmd/optimize.go` already resolves `weekStart, weekEnd` via `GetMatchupWeekBounds`. It additionally
  calls the already-existing-but-currently-unused `GetMatchupWeekNumberForDate(today)` to get the
  period number, then calls `GetGSLimits(teamID, periodNum)` in place of reading `cfg.GSMax`
  directly when constructing `optimizer.GSBudget{Limit: ...}`.
- `internal/gscheck.RunGSCheck` already has `period.Number` from `GetScoringPeriodsAndTeams()` (same
  weekly numbering). It calls `GetGSLimits(teamID, period.Number)` per team instead of reading
  `cfg.GSMax`/`cfg.GSMin`.

## Error handling

`GetGSLimits` failures (network error, unexpected response shape, category row not found) are
**soft failures**: both call sites fall back to `cfg.GSMax`/`cfg.GSMin`, matching the existing
"WARNING: ... — GS limit disabled" soft-fail style already present in `cmd/optimize.go`. This keeps
`GS_MAX`/`GS_MIN` as a live safety net rather than removing them outright — a live-fetch outage
degrades to the previous (imperfect but known) behavior instead of silently disabling the GS gate
or `gscheck` entirely.

## Testing

- **go-fantrax:** a trimmed/synthetic `testdata/getTeamRosterInfoGamesPerPos.json` fixture covering
  both tables (not a verbatim dump of the real captured response, which carries real player names,
  injury notes, and league member names not appropriate for a public fixture), plus a
  `TestParseGamesPerPosition`-style test mirroring the existing `TestParseRosterViolations` pattern.
- **rosterbot:** a unit test for the GS-row-extraction/parsing logic in `internal/fantrax`, plus
  updates to the existing `cmd/optimize.go`/`gscheck` tests to cover the fallback-on-error path.

## Rollout

1. Land the go-fantrax PR (`GetTeamRosterPositionCounts`) against `pmurley/go-fantrax`.
2. Bump rosterbot's `go.mod` to depend on `github.com/pmurley/go-fantrax` directly (dropping the
   `github.com/nixon-commits/go-fantrax` fork replace/require) now that this and prior fixes have
   merged upstream.
3. Wire `internal/fantrax.GetGSLimits`, update `cmd/optimize.go` and `internal/gscheck`.
4. Verify against the live league for periods 15/16/17 (already spot-checked manually during
   design: 10/12, 15/19, 10/12 respectively) before the Jul 13 break window arrives.

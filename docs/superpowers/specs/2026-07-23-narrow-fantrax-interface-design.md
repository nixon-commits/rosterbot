# Narrow Fantrax interfaces for lineuprun, recap, gscheck

**Issue:** rosterbot-phb · **Date:** 2026-07-23 · **Blocks:** rosterbot-6rv (Lineup Run phase-split)

## Problem

`internal/fantrax.Client` exposes 34 methods and embeds two concrete upstream
types. Three consumer packages take that concrete `*fantrax.Client` as their
entry-point parameter, so there is no seam at which to inject a fake Fantrax —
which is exactly why the documented period-drift incidents (rosterbot-uv6,
z3b, 2ax, 48z) could not be caught by a package-level test.

Three sibling packages already declare the narrow interface they use
(`waivers.FantraxClient`, `claims.ClaimsClient`, `transactions.TradeClient`),
and `recap/site.go` already declares `matchupWeekProvider` / `dayCompletionChecker`
this way. This issue applies the same treatment to the remaining three entry
points — and lands the tests the seam enables.

## Principle

**Consumer-declared, minimal interfaces.** `internal/fantrax` does not change;
`*fantrax.Client` keeps satisfying every new interface implicitly. This is the
Go idiom (consumer owns the interface) and keeps `fantrax` free of consumer
concerns. Direction chosen deliberately over the `fantrax.WeekBounder`
(producer-declared) style.

## Method surface (verified against source)

| Package | Entry point | Methods |
|---|---|---|
| `gscheck` | `RunGSCheck` | `GetScoringPeriodsAndTeams`, `GetGSLimits`, `GetTeamGS` |
| `recap` | `Run` | `GetSeasonDateRange`, `GetScoringPeriodsAndTeams`, `GetActiveSlots`, `GetPitcherSlots`, `GetAllMatchupEntries`, `GetMatchupWeekNumberForDate`, `DailyFantasyPoints`, `BackfillDailyFPts`, `GetTeamPitcherStarts`, `GetFullPlayerPool` |
| `recap` | `RunSite` | `Run`'s set **+** `GetMatchupWeekByNumber` |
| `recap` | `buildLeaders` | `GetFullPlayerPool` |
| `lineuprun` | `Run` | rosters/slots/scoring (`GetHitterRoster`, `GetPitcherRoster`, `GetActiveSlots`, `GetPitcherSlots`, `GetScoringWeights`, `GetPitcherScoringWeights`, `GetFullHitterRoster`), periods (`GetCurrentPeriod`, `GetSeasonDateRange`, `GetMatchupWeekBounds`, `GetScoringPeriodsAndTeams`, `DailyPeriodFor`, `GetHitterRosterForPeriod`, `GetPitcherRosterForPeriod`), GS (`GetGSLimits`, `GetTeamGS`), stats (`GetRecentPitcherStats`, `DailyFantasyPoints`, `BackfillDailyFPts`), apply (`ApplyLineup`, `InvalidatePeriodRosterCache`) |

All referenced types (`ScoringPeriod`, `WeeklyPeriod`, `DailyPeriod`, `Slot`,
`Player`, `PlayerSlot`, `RecentStat`, `ScoringWeights`, `SlotCounts`,
`DayRoster`, `PitcherStart`, `DatedPitcherStart`, `MatchupEntry`,
`models.PoolPlayer`) are already exported from `fantrax`.

## Interfaces (layered by embedding)

### gscheck (`gscheck.go`)
```go
type GSCheckClient interface {
    GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
    GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
    GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
}
```
`RunGSCheck(ft GSCheckClient, cfg config.Config)`.

### lineuprun (`lineuprun.go`)
```go
type recentStatsClient interface { // unexported; windowedHitterRecent
    GetSeasonDateRange() (time.Time, time.Time, error)
    DailyFantasyPoints(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.DayRoster, error)
    BackfillDailyFPts(days []fantrax.DayRoster) error
}

type LineupClient interface { // exported; Run
    recentStatsClient
    // …the remaining ~18 methods listed above…
}
```
`Run(ft LineupClient, cfg *config.Config, opts Options)`. `Run` passes `ft` to
`windowedHitterRecent(ft, …)` — valid, `LineupClient` ⊇ `recentStatsClient`.

### recap (`recap.go`, `leaders.go`, `site.go`)
```go
type leadersClient interface { GetFullPlayerPool() ([]models.PoolPlayer, error) }   // leaders.go
type seasonMeanClient interface { DailyFantasyPoints(...) ([]fantrax.DayRoster, error) } // recap.go

type RecapClient interface { // recap.go, exported; Run
    leadersClient
    seasonMeanClient
    GetSeasonDateRange() (time.Time, time.Time, error)
    GetScoringPeriodsAndTeams() (...)
    GetActiveSlots() ([]fantrax.Slot, error)
    GetPitcherSlots() ([]fantrax.Slot, error)
    GetAllMatchupEntries() ([]fantrax.MatchupEntry, error)
    GetMatchupWeekNumberForDate(date time.Time) (int, error)
    BackfillDailyFPts(days []fantrax.DayRoster) error
    GetTeamPitcherStarts(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.DatedPitcherStart, error)
}

type SiteClient interface { // site.go, exported; RunSite
    RecapClient
    matchupWeekProvider // existing: GetMatchupWeekByNumber
}
```
`Run(ft RecapClient, opts Options)`, `RunSite(ft SiteClient, sopts SiteOptions)`,
`buildLeaders(ft leadersClient, …)`, `seasonToDateTeamMean`/`fetchSeasonMeans`
take `seasonMeanClient`. `RunSite` passes `ft` to `Run` (⊇ `RecapClient`) and
`completedMatchupWeeks` (⊇ `matchupWeekProvider`).

## Tests

| Package | Test | Covers |
|---|---|---|
| `gscheck` | `RunGSCheck` via a fake `GSCheckClient` — violation-detected, clean no-op, and a per-team tally that would false-fire "Under Min" | The false league-wide GS alert class (rosterbot-uv6/wd5), previously uncatchable |
| `recap` | `fetchSeasonMeans`/`seasonToDateTeamMean` via a `seasonMeanClient` fake; `buildLeaders` via a `leadersClient` fake | recap season-mean aggregation (criterion 4); moves recap.go + leaders.go off 0% |
| `lineuprun` | `windowedHitterRecent` via a `recentStatsClient` fake — trailing-30d window aggregation (rosterbot-2nd) | The seam isolated today |

Fakes live in each package's `*_test.go` (mirrors how waivers tests fake
`FantraxClient`). `RunGSCheck` tests run with `cfg.DryRun=true` so `notify`
is a no-op and no network is touched.

## Scope boundary

`lineuprun.Run` and `recap.Run` also depend on non-fantrax heavy collaborators
(`projections.LoadBattingProjections`, `schedule.NewClient()`), so the fantrax
seam alone does not isolate the full entry point. Full end-to-end `Run`
orchestration testing is **out of scope** here and is delivered by the
phase-split issue this unblocks (rosterbot-6rv). The `LineupClient` /
`RecapClient` interfaces are the load-bearing deliverable that makes those
future phase-level tests possible.

## Acceptance criteria

1. `lineuprun.Run`, `recap.Run`, `recap.RunSite`, `gscheck.RunGSCheck` take a
   package-declared interface. ✓
2. `*fantrax.Client` satisfies each with no changes to `internal/fantrax`. ✓
3. Each package gains ≥1 fake-driven test (not just leaf helpers). ✓
4. ≥1 previously-uncatchable failure mode covered (GS false-alert + season-mean). ✓
5. recap.go / site.go / leaders.go off 0%; gscheck coverage up. ✓
6. `go vet ./...` and `make test` pass. (verified at end)

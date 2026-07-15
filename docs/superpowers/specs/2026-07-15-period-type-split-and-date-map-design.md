# Period Type Split + Authoritative Date→Period Map — Design

**Issues:** rosterbot-1i3 (WeeklyPeriod/DailyPeriod type split) + rosterbot-ren (authoritative date→daily-period map). Follow-up deferred: rosterbot-2ax (unify Class-A resolution onto the map).

**Date:** 2026-07-15

## Problem

Fantrax exposes two unrelated period-numbering schemes, both represented as a bare `int`:

- **Weekly matchup axis** — `ScoringPeriod.Number` (~7 days/period, merged wider around breaks). Used for GS-limit lookups and standings.
- **Daily roster/apply axis** — one number per calendar day (e.g. 104…110 across a week). Used to key roster/apply/GS-snapshot endpoints.

Because both are `int`, a weekly value passed where a daily one is required type-checks silently. This exact class caused two production incidents (rosterbot-uv6, rosterbot-z3b) and a third is open (rosterbot-ren).

Separately, the daily axis is currently *computed* from season-start day math (`PeriodForDate(seasonStart, date) = 1 + daysSince`). Fantrax can insert extra daily periods mid-season (doubleheader/postponed makeups), so this computed value can drift from Fantrax's authoritative numbering. Class A (near-today) is patched via `AnchorPeriodForDate` (anchors on `GetCurrentPeriod()`); Class B (historical: `DailyFantasyPoints`, `GetTeamPitcherStarts`) still uses naive day math and can attribute a day's data to the wrong period across an insertion boundary (rosterbot-ren).

## Investigation finding (decisive)

A live read-only probe of `getTeamRosterInfo` showed the authoritative map is already available in **one call**:

- `TeamRosterResponseData.DisplayedLists["periodList"]` is a `[]interface{}` of strings: `"1 (Wed Mar 25)"` … `"187 (Sun Sep 27)"` — the daily-period dropdown, spanning the whole season (past **and** future).
- The label for period N is definitionally the date whose snapshot lives at period N (it is what the Fantrax UI clicks to render period N's roster).
- As of 2026-07-15: **zero drift** — all 187 entries match naive day-math, and `GetCurrentPeriod()=113 == naive(today)=113`.
- `Data.sDate` is only the request timestamp — not the period's date. Ignored.

Consequence: the map is a **strict, self-correcting upgrade** over `PeriodForDate` — identical output today, automatically correct if Fantrax inserts a period later. No go-fantrax struct change is needed (`DisplayedLists` is already `map[string]interface{}`); we parse the strings in rosterbot.

## Part A — WeeklyPeriod / DailyPeriod type split (rosterbot-1i3)

New file `internal/fantrax/period_types.go`:

```go
// WeeklyPeriod is the weekly matchup axis (ScoringPeriod.Number): ~7 days per
// period, merged wider around breaks. Source: getStandings SCHEDULE captions.
type WeeklyPeriod int

// DailyPeriod is the daily roster/apply axis: one number per calendar day.
// Source: GetCurrentPeriod() (today) and the periodList date map. Keys
// roster/apply/GS-snapshot endpoints.
type DailyPeriod int
```

**Weekly axis → `WeeklyPeriod`:**
- `ScoringPeriod.Number` field.
- `GetGSLimits(teamID string, period WeeklyPeriod)` (+ `fetchGSLimits`).
- `Find{Current,JustEnded,MostRecentPast}Period` return `*ScoringPeriod` (unchanged); consumers read `.Number` (now `WeeklyPeriod`) and pass it to `GetGSLimits`.

**Daily axis → `DailyPeriod`:**
- `GetCurrentPeriod() (DailyPeriod, error)` (convert the `auth_client` `int` at the seam; cache `New[DailyPeriod]` — JSON identical to `int`, old cache entries still deserialize).
- `PeriodForDate`, `AnchorPeriodForDate` (its `anchorPeriod` param + return), `DailyPeriodFor` (its `currentPeriod` param + return).
- `gsPeriodWalk(...) []DailyPeriod`.
- `ApplyLineup(period DailyPeriod, ...)`, `Get{Hitter,Pitcher}RosterForPeriod`, `fetch{Hitter,Pitcher}RosterForPeriod`, `getPlayerGSSnapshotForPeriod` (+ cached), `GetRecentPitcherStats(currentPeriod DailyPeriod, _ int)`, `fetchRecentPitcherStats`, `InvalidatePeriodRosterCache`, `ttlForPeriod`.
- `daily_fpts.go` internals: `snapCacheFor`, `periodIsVolatile`, `getPeriodSnapshotCached`, `fetchPeriodSnapshot`, `DayRoster.Period`.
- `pitcher_starts.go` internal `period` locals.
- `internal/backtest` `Period` field (fed from `DayRoster.Period`).

**Conversion boundaries the compiler will flag:**
- Cache-key formatting: `strconv.Itoa(int(period))`, `fmt.Sprintf("%d", int(period))`.
- `auth_client` seam: `c.auth.GetCurrentPeriod()` returns `int` → `DailyPeriod(...)`; `ApplyLineup`/roster-fetch pass `fmt.Sprintf("%d", int(period))`.
- `DayRoster.Period` JSON tag stays `json:"period"` — named int marshals as int, no format change.

**Out of scope (left `int`, with a one-line comment):** `MatchupEntry.ScoringPeriod` / `MatchupWeek.ScoringPeriod` — matchup-grouping numbers from the matchups API, never passed to roster/apply/GS endpoints, so not in the confusion hot path.

**Verification:** Go's lack of implicit conversion between named int types makes every axis mismatch a compile error. Existing tests unchanged and green proves no behavior change.

## Part B — Authoritative date→period map (rosterbot-ren)

New file `internal/fantrax/period_date_map.go`:

- Parse `DisplayedLists["periodList"]` entries `"<N> (<Weekday> <Mon> <D>)"` via regex into `map[string]DailyPeriod` keyed by `date.Format("2006-01-02")`. Year comes from `seasonStart` (list is chronological; roll to year+1 if a parsed month precedes the season-start month — guards a season spanning a year boundary, though MLB does not).
- `Client.periodDateMap(seasonStart time.Time) (map[string]DailyPeriod, error)`: fetch once via `c.auth.GetTeamRosterInfoRaw(fmt.Sprintf("%d", currentPeriod), c.teamID)` (currentPeriod from `GetCurrentPeriod()`), parse, cache in file cache (`fantrax-period-date-map-<leagueID>-<seasonYear>`, `stableTTL` = 7d) **and** memoize in-memory on `*Client` (mirror `allMatchups()` with a mutex), since a `recap-site` rebuild re-derives every week.
- `Client.dailyPeriodForDate(seasonStart, date time.Time) DailyPeriod`: look up `date` in the map → authoritative `DailyPeriod`; on miss or any fetch/parse error, **soft-fall-back** to `PeriodForDate(seasonStart, date)`. Soft-fail keeps hermetic tests (`cacheDir==""`, no auth) and credential-less renders working exactly as today.

**Integration:** replace the naive `PeriodForDate(seasonStart, d)` / `PeriodForDate(seasonStart, dayBefore)` calls in `DailyFantasyPoints` and `GetTeamPitcherStarts` (both `*Client` methods) with `c.dailyPeriodForDate(...)`. **No exported signature changes** — the map is fetched internally, so `internal/backtest`, `internal/recap`, `cmd/backtest.go`, `cmd/grade.go`, `internal/lineuprun` are untouched by Part B.

Because there is zero drift today, cache keys (`fantrax-roster-stats-<teamID>-<period>`) are byte-identical to current behavior; if Fantrax inserts a period later, the correct key is used automatically. No cache migration.

## Non-goals

- **Class-A unification (rosterbot-2ax):** rebasing `DailyPeriodFor`/`AnchorPeriodForDate` (lineup-apply, `GetTeamGS`) onto the map. Deferred — they are pure free functions (no `*Client`), produce identical results today, and unifying them means signature changes + touching the live apply hot path.
- No go-fantrax library changes.

## Testing strategy

- **Part A:** no new behavior tests. `go build ./...` + `go vet ./...` are the primary gate (they enforce the axes). `go test ./internal/...` must stay green unchanged.
- **Part B:**
  - Unit-test the `periodList` parser against a captured fixture slice (`"1 (Wed Mar 25)"`, `"104 (Mon Jul 6)"`, an out-of-range date, a malformed entry) → asserts the `map[date]DailyPeriod` and that a malformed entry is skipped, not fatal.
  - Unit-test `dailyPeriodForDate` fallback: with an empty/unavailable map it returns `PeriodForDate(seasonStart, date)` (hermetic, no network).
  - `make run-all` cold+warm as the end-to-end smoke (exercises `backtest`/`recap` which drive `DailyFantasyPoints`/`GetTeamPitcherStarts`); confirm one map fetch, not one-per-day, via `cache hit/miss` lines.

## Acceptance criteria

1. All period-carrying signatures in `internal/fantrax` use `WeeklyPeriod` or `DailyPeriod` instead of bare `int` (except the documented `MatchupEntry`/`MatchupWeek` matchup-grouping fields). `go build ./...` and `go vet ./...` pass.
2. `DailyFantasyPoints` and `GetTeamPitcherStarts` resolve dates via the authoritative `periodList` map, soft-falling back to naive day math; the map is fetched at most once per client/build (cached + memoized). rosterbot-ren closed.
3. Existing `go test ./internal/...` suite green, unchanged. New parser + fallback unit tests added.
4. CLAUDE.md updated: the rosterbot-ren "still-open" / "accepted gap" references become "resolved via the authoritative periodList date map"; Class-A remains as the documented rosterbot-2ax follow-up.

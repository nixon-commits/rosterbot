# Period Type Split + Authoritative Date→Period Map — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Fantrax's two period axes type-distinct (`WeeklyPeriod`/`DailyPeriod`) so they can't be silently swapped, and replace naive season-start day-math in the historical FP/GS walks with Fantrax's authoritative `periodList` date→daily-period map.

**Architecture:** Two named int types in `internal/fantrax` thread through ~20 signatures and ~40 call sites; Go's lack of implicit conversion between named int types turns every axis mismatch into a compile error. A new `periodDateMap` (parsed once from `getTeamRosterInfo`'s `DisplayedLists["periodList"]`, cached + memoized) feeds `DailyFantasyPoints`/`GetTeamPitcherStarts` via an internal `dailyPeriodForDate` resolver that soft-falls-back to the naive path.

**Tech Stack:** Go 1.2x, `github.com/nixon-commits/go-fantrax` (fork), `internal/cache` (generic `FileCache[T]`).

## Global Constraints

- **Verification gate** (run after every task; the compiler is the primary safety net): `go build ./... && go vet ./... && go test ./internal/...` — all must pass.
- `gofmt` + `go vet` also run automatically via PostToolUse hooks on every Edit/Write.
- Run `go mod tidy` once at the end (per CLAUDE.md).
- **`fmt` verbs work on named int types** — `fmt.Sprintf("%d", period)` / `prog.Logf("... %d", period)` need **no** conversion. Only these need an explicit `int(period)` cast: `strconv.Itoa`, and passing to a go-fantrax/library function whose parameter is `int` (e.g. `ConfirmOrExecuteTeamRosterChangesRaw`, `GetTeamRosterPositionCounts` takes a string via `strconv.Itoa`).
- **Untyped constants assign freely** to named int types (`GetHitterRosterForPeriod(105)`, `ScoringPeriod{Number: 2}`, `period == 0` all compile unchanged). Only period-typed **variables** and **struct/table fields** declared `int` break and must be retyped or wrapped.
- **Test updates are mechanical:** retype table fields / locals from `int` to the named type (or wrap a comparison with `int(...)`). **No asserted value changes** — green-after-retype proves no behavior change.
- Named int marshals to JSON identically to `int`; existing cache entries deserialize unchanged. No cache migration.
- Commit after each task with a message referencing the issue.

---

### Task 1: Define WeeklyPeriod / DailyPeriod types

**Files:**
- Create: `internal/fantrax/period_types.go`

**Interfaces:**
- Produces: `type WeeklyPeriod int`, `type DailyPeriod int` (package `fantrax`).

- [ ] **Step 1: Create the types file**

```go
package fantrax

// WeeklyPeriod is the weekly matchup axis: Fantrax's "Scoring Period N" from the
// getStandings SCHEDULE captions (~7 days per period, merged wider around breaks
// like the All-Star break). This is what GetGSLimits and standings-style lookups
// key on. See ScoringPeriod.Number, FindCurrentPeriod.
type WeeklyPeriod int

// DailyPeriod is the daily roster/apply axis: one number per calendar day
// (e.g. 104…110 across a week), which Fantrax never exposes as a list — only as
// "today" via GetCurrentPeriod() and as the full season dropdown parsed by the
// periodList date map. Roster/apply/GS-snapshot endpoints are keyed by this axis.
// It is a distinct type from WeeklyPeriod precisely so the two cannot be passed
// interchangeably (the rosterbot-uv6 / rosterbot-z3b bug class).
type DailyPeriod int
```

- [ ] **Step 2: Verify it compiles (nothing uses the types yet)**

Run: `go build ./internal/fantrax/`
Expected: PASS (no errors).

- [ ] **Step 3: Commit**

```bash
git add internal/fantrax/period_types.go
git commit -m "feat(fantrax): add WeeklyPeriod/DailyPeriod named types (rosterbot-1i3)"
```

---

### Task 2: Migrate the daily axis end-to-end

Change every daily-axis signature to `DailyPeriod` and fix the resulting compile cascade across `internal/fantrax`, `internal/lineuprun`, `internal/backtest`, and their tests. The build is red mid-task and green at the end.

**Files:**
- Modify: `internal/fantrax/recent_stats.go` — `GetCurrentPeriod`, `AnchorPeriodForDate`, `PeriodForDate`
- Modify: `internal/fantrax/gs_period_walk.go` — `DailyPeriodFor`, `gsPeriodWalk`
- Modify: `internal/fantrax/client.go` — `ttlForPeriod`, `InvalidatePeriodRosterCache`, `GetHitterRosterForPeriod`, `fetchHitterRosterForPeriod`, `ApplyLineup`
- Modify: `internal/fantrax/pitcher_roster.go` — `GetPitcherRosterForPeriod`, `fetchPitcherRosterForPeriod`
- Modify: `internal/fantrax/pitcher_recent_stats.go` — `GetRecentPitcherStats`, `fetchRecentPitcherStats`
- Modify: `internal/fantrax/gs_check.go` — `getPlayerGSSnapshotForPeriod`, `getPlayerGSSnapshotForPeriodCached` (daily params only; **not** `ScoringPeriod.Number`, that's Task 3)
- Modify: `internal/fantrax/daily_fpts.go` — `snapCacheFor`, `periodIsVolatile`, `getPeriodSnapshotCached`, `fetchPeriodSnapshot`, `DayRoster.Period`
- Modify: `internal/fantrax/pitcher_starts.go` — internal `period`/`basePeriod` locals
- Modify: `internal/lineuprun/snapshot.go` — `dateResult.period` field + add `fantrax` import
- Modify: `internal/backtest/backtest.go` — `LineupDayResult.Period` field
- Tests: `internal/fantrax/period_anchor_test.go`, `internal/fantrax/gs_period_walk_test.go`, `internal/fantrax/daily_fpts_test.go`, plus any the compiler flags in `internal/backtest`, `internal/recap`, `internal/lineuprun`.

**Interfaces:**
- Consumes: `DailyPeriod` (Task 1).
- Produces: `GetCurrentPeriod() (DailyPeriod, error)`, `PeriodForDate(seasonStart, date) DailyPeriod`, `AnchorPeriodForDate(anchorDate, anchorPeriod DailyPeriod, date) DailyPeriod`, `DailyPeriodFor(currentPeriod DailyPeriod, seasonStart, today, date) DailyPeriod`, `ApplyLineup(period DailyPeriod, …)`, `Get{Hitter,Pitcher}RosterForPeriod(period DailyPeriod)`, `GetRecentPitcherStats(currentPeriod DailyPeriod, _ int)`, `InvalidatePeriodRosterCache(period DailyPeriod)`, `DayRoster.Period DailyPeriod`.

- [ ] **Step 1: `recent_stats.go` — GetCurrentPeriod / AnchorPeriodForDate / PeriodForDate**

```go
func (c *Client) GetCurrentPeriod() (DailyPeriod, error) {
	if c.cacheDir == "" {
		p, err := c.auth.GetCurrentPeriod()
		return DailyPeriod(p), err
	}
	fc := cache.New[DailyPeriod](c.cacheDir, c.todayTTL)
	key := cache.Key(keyCurrentPeriod, c.leagueID, time.Now().UTC().Format("2006-01-02"))
	return fc.Get(key, func() (DailyPeriod, error) {
		p, err := c.auth.GetCurrentPeriod()
		return DailyPeriod(p), err
	})
}

func AnchorPeriodForDate(anchorDate time.Time, anchorPeriod DailyPeriod, date time.Time) DailyPeriod {
	days := int(date.Truncate(24*time.Hour).Sub(anchorDate.Truncate(24*time.Hour)).Hours() / 24)
	return anchorPeriod + DailyPeriod(days)
}

func PeriodForDate(seasonStart, date time.Time) DailyPeriod {
	return AnchorPeriodForDate(seasonStart, 1, date)
}
```

- [ ] **Step 2: `gs_period_walk.go` — DailyPeriodFor / gsPeriodWalk**

```go
func DailyPeriodFor(currentPeriod DailyPeriod, seasonStart, today, date time.Time) DailyPeriod {
	if currentPeriod > 0 {
		return AnchorPeriodForDate(today, currentPeriod, date)
	}
	return PeriodForDate(seasonStart, date)
}

func gsPeriodWalk(sp ScoringPeriod, currentPeriod DailyPeriod, seasonStart, today time.Time) []DailyPeriod {
	// … body unchanged except:
	var out []DailyPeriod
	// … out = append(out, DailyPeriodFor(currentPeriod, seasonStart, today, d))
}
```

- [ ] **Step 3: `client.go` — ttlForPeriod / InvalidatePeriodRosterCache / hitter roster / ApplyLineup**

```go
func (c *Client) ttlForPeriod(period DailyPeriod) time.Duration {
	seasonStart, _, err := c.fetchSeasonDateRange()
	if err != nil {
		return c.todayTTL
	}
	cur := PeriodForDate(seasonStart, time.Now().UTC())
	if period < cur {
		return pastPeriodTTL
	}
	return c.todayTTL
}

func (c *Client) InvalidatePeriodRosterCache(period DailyPeriod) {
	if c.cacheDir == "" {
		return
	}
	fc := cache.New[[]Player](c.cacheDir, 0)
	periodStr := strconv.Itoa(int(period))
	// … rest unchanged
}

func (c *Client) GetHitterRosterForPeriod(period DailyPeriod) ([]Player, error) {
	// … unchanged except:
	key := cache.Key(keyHitterRoster, c.teamID, strconv.Itoa(int(period)))
	// …
}
func (c *Client) fetchHitterRosterForPeriod(period DailyPeriod) ([]Player, error) { /* body unchanged: fmt.Sprintf("%d", period) works on DailyPeriod */ }

func (c *Client) ApplyLineup(period DailyPeriod, active []PlayerSlot, reserve []string) error {
	if period == 0 {
		p, err := c.auth.GetCurrentPeriod()
		if err != nil {
			return fmt.Errorf("auto-detect period: %w", err)
		}
		period = DailyPeriod(p)
	}
	rawRoster, err := c.auth.GetTeamRosterInfoRaw(fmt.Sprintf("%d", period), c.teamID)
	// …
	executor := func(fieldMap map[string]auth_client.RosterPosition) (*models.RosterChangeResponse, error) {
		return c.auth.ConfirmOrExecuteTeamRosterChangesRaw(int(period), c.teamID, fieldMap, false, true, false)
	}
	// …
}
```

- [ ] **Step 4: `pitcher_roster.go` / `pitcher_recent_stats.go`**

Change `GetPitcherRosterForPeriod(period DailyPeriod)`, `fetchPitcherRosterForPeriod(period DailyPeriod)` (cache key → `strconv.Itoa(int(period))`; `fmt.Sprintf("%d", period)` unchanged). Change `GetRecentPitcherStats(currentPeriod DailyPeriod, _ int)` and `fetchRecentPitcherStats(period DailyPeriod)` (any cache-key `strconv.Itoa` → `int(period)`).

- [ ] **Step 5: `gs_check.go` daily helpers**

`getPlayerGSSnapshotForPeriod(teamID string, period DailyPeriod)` → cache/roster key `strconv.Itoa(int(period))`. `getPlayerGSSnapshotForPeriodCached(..., period DailyPeriod)` → `cache.Key(keyPitcherGS, teamID, strconv.Itoa(int(period)))`. **Leave `ScoringPeriod.Number` and `GetGSLimits` alone — Task 3.**

- [ ] **Step 6: `daily_fpts.go` internals + DayRoster.Period**

```go
type DayRoster struct {
	// …
	Period DailyPeriod `json:"period"`
	// …
}
```
`snapCacheFor(period DailyPeriod)`, `periodIsVolatile(period, curPeriod DailyPeriod)`, `getPeriodSnapshotCached(..., period DailyPeriod)`, `fetchPeriodSnapshot(teamID string, period DailyPeriod)` → `GetTeamRosterInfoRaw(strconv.Itoa(int(period)), …)`. The `curPeriod`/`basePeriod`/`period` locals become `DailyPeriod` automatically (they're assigned from `PeriodForDate`). `basePeriod >= 1` compiles (untyped const).

- [ ] **Step 7: `pitcher_starts.go` locals** — no signature change; `basePeriod`/`period` locals become `DailyPeriod` from `PeriodForDate`; cache-key formatting inside the cached helper already handled in Step 5.

- [ ] **Step 8: `lineuprun/snapshot.go` — dateResult.period**

Add `"github.com/nixon-commits/rosterbot/internal/fantrax"` to the import block and:
```go
type dateResult struct {
	date             time.Time
	period           fantrax.DailyPeriod
	// … rest unchanged
}
```
(`lineuprun.go` already assigns `period: period` where `period` is `fantrax.DailyPeriodFor(...)`, now `DailyPeriod`; `dr.period == 0`, `dr.period` in `ApplyLineup`/`InvalidatePeriodRosterCache`, and `%d` logging all compile unchanged. `period > 0` at line ~618 compiles.)

- [ ] **Step 9: `backtest/backtest.go` — LineupDayResult.Period**

```go
type LineupDayResult struct {
	Date       time.Time          `json:"date"`
	Period     fantrax.DailyPeriod `json:"period"`
	// … rest unchanged
}
```
(`Period: day.Period` at line ~177 now matches.)

- [ ] **Step 10: Fix test call sites the compiler flags**

- `internal/fantrax/period_anchor_test.go`: the `cur` local passed to `AnchorPeriodForDate` → `DailyPeriod`; table `want` field compared to the `DailyPeriod` return → make `want DailyPeriod` (or compare `int(got)`). `PeriodForDate(...) != 1` / `!= 91` need no change (untyped consts).
- `internal/fantrax/gs_period_walk_test.go`: `currentPeriod` locals → `DailyPeriod`; `want []int{...}` → `want []DailyPeriod{...}`; line ~91 `want := []int{PeriodForDate(...)}` → `[]DailyPeriod{PeriodForDate(...)}`. `ScoringPeriod{Number: 15, …}` unchanged (Task 3 retypes the field but untyped literal still assigns).
- `internal/fantrax/daily_fpts_test.go`: table field `period int` → `period DailyPeriod` (the `const cur = 100` is untyped and needs no change).
- Any others the compiler flags in `internal/backtest`, `internal/recap`, `internal/lineuprun` (e.g. a `DayRoster{Period: N}` literal is fine; a stored `int` var isn't): retype the local/field to `fantrax.DailyPeriod` or wrap with `int(...)`. **Do not change any expected values.**

- [ ] **Step 11: Run the gate**

Run: `go build ./... && go vet ./... && go test ./internal/...`
Expected: PASS. If a period-typed mismatch remains, the compiler names the exact file:line — fix by retyping/wrapping per Global Constraints.

- [ ] **Step 12: Commit**

```bash
git add -A
git commit -m "refactor(fantrax): type the daily period axis as DailyPeriod (rosterbot-1i3)"
```

---

### Task 3: Migrate the weekly axis

**Files:**
- Modify: `internal/fantrax/gs_check.go` — `ScoringPeriod.Number`, parse site
- Modify: `internal/fantrax/gs_limits.go` — `GetGSLimits`, `fetchGSLimits`
- Tests: `internal/fantrax/gs_check_test.go` (table `want` fields)

**Interfaces:**
- Consumes: `WeeklyPeriod` (Task 1).
- Produces: `ScoringPeriod.Number WeeklyPeriod`, `GetGSLimits(teamID string, period WeeklyPeriod) (min, max *int, err error)`.

- [ ] **Step 1: `gs_check.go` — ScoringPeriod.Number + parse**

```go
type ScoringPeriod struct {
	Number    WeeklyPeriod
	Caption   string
	StartDate time.Time
	EndDate   time.Time
}
```
At the caption-parse site, `num, _ := strconv.Atoi(...)` then `ScoringPeriod{Number: WeeklyPeriod(num), …}`. `%d` logging of `.Number` needs no change.

- [ ] **Step 2: `gs_limits.go` — GetGSLimits / fetchGSLimits**

```go
func (c *Client) GetGSLimits(teamID string, period WeeklyPeriod) (min, max *int, err error) {
	// … unchanged except:
	key := cache.Key(keyGSLimits, teamID, strconv.Itoa(int(period)))
	// …
}
func (c *Client) fetchGSLimits(teamID string, period WeeklyPeriod) (gsLimits, error) {
	gpp, err := c.auth.GetTeamRosterPositionCounts(teamID, strconv.Itoa(int(period)))
	// … unchanged
}
```

- [ ] **Step 3: Confirm downstream `.Number` callers compile unchanged**

`internal/lineuprun/lineuprun.go` `ft.GetGSLimits(cfg.TeamID, sp.Number)` and `internal/gscheck/gscheck.go` `ft.GetGSLimits(cfg.TeamID, period.Number)` now pass `WeeklyPeriod` into a `WeeklyPeriod` param — no change needed. `%d` logging of `sp.Number` — no change.

- [ ] **Step 4: Fix `gs_check_test.go`**

Table structs that declare `want int` and compare to `p.Number` → `want WeeklyPeriod` (or compare `int(p.Number)`). `ScoringPeriod{Number: 2, …}` and `p.Number != 2` need no change (untyped consts).

- [ ] **Step 5: Run the gate**

Run: `go build ./... && go vet ./... && go test ./internal/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(fantrax): type the weekly matchup axis as WeeklyPeriod (rosterbot-1i3)"
```

---

### Task 4: periodList parser (TDD)

**Files:**
- Create: `internal/fantrax/period_date_map.go` (parser only this task)
- Create/Test: `internal/fantrax/period_date_map_test.go`

**Interfaces:**
- Produces: `parsePeriodList(entries []interface{}, seasonYear int, startMonth time.Month) map[string]DailyPeriod` — maps `"2006-01-02"` → `DailyPeriod`, skipping malformed/non-string entries.

- [ ] **Step 1: Write the failing test**

```go
package fantrax

import (
	"testing"
	"time"
)

func TestParsePeriodList(t *testing.T) {
	entries := []interface{}{
		"1 (Wed Mar 25)",
		"104 (Mon Jul 6)",
		"187 (Sun Sep 27)",
		"not a period entry",
		42, // non-string, must be skipped
	}
	m := parsePeriodList(entries, 2026, time.March)

	want := map[string]DailyPeriod{
		"2026-03-25": 1,
		"2026-07-06": 104,
		"2026-09-27": 187,
	}
	if len(m) != len(want) {
		t.Fatalf("len=%d, want %d (map=%v)", len(m), len(want), m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%q]=%d, want %d", k, m[k], v)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/fantrax/ -run TestParsePeriodList`
Expected: FAIL — `undefined: parsePeriodList`.

- [ ] **Step 3: Implement the parser**

```go
package fantrax

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// periodListEntryRe matches a Fantrax periodList dropdown entry like
// "104 (Mon Jul 6)" → group 1 = daily period number, group 2 = "Mon Jul 6".
var periodListEntryRe = regexp.MustCompile(`^(\d+)\s+\((\w+ \w+ \d+)\)$`)

// parsePeriodList turns Fantrax's periodList dropdown (DisplayedLists["periodList"])
// into a date→DailyPeriod map keyed by "2006-01-02". The label for period N is the
// calendar date whose roster snapshot lives at period N, so this is the
// authoritative daily-period numbering (self-correcting across Fantrax's mid-season
// period insertions). Year comes from seasonYear; an entry whose month precedes
// startMonth rolls to seasonYear+1 (defensive — MLB seasons don't cross a year
// boundary). Malformed/non-string entries are skipped, never fatal.
func parsePeriodList(entries []interface{}, seasonYear int, startMonth time.Month) map[string]DailyPeriod {
	out := make(map[string]DailyPeriod, len(entries))
	for _, e := range entries {
		s, ok := e.(string)
		if !ok {
			continue
		}
		m := periodListEntryRe.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		dt, err := time.Parse("Mon Jan 2 2006", fmt.Sprintf("%s %d", m[2], seasonYear))
		if err != nil {
			continue
		}
		if dt.Month() < startMonth {
			dt = dt.AddDate(1, 0, 0)
		}
		out[dt.Format("2006-01-02")] = DailyPeriod(num)
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/fantrax/ -run TestParsePeriodList -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fantrax/period_date_map.go internal/fantrax/period_date_map_test.go
git commit -m "feat(fantrax): parse authoritative periodList date map (rosterbot-ren)"
```

---

### Task 5: periodDateMap fetch/cache/memo + dailyPeriodForDate resolver

**Files:**
- Modify: `internal/fantrax/client.go` — add `periodMapMu sync.Mutex` + `periodMapMemo map[string]DailyPeriod` fields to `Client` (confirm `sync` is imported; `matchupsMu` already suggests it is)
- Modify: `internal/fantrax/cachekeys.go` — add `keyPeriodDateMap` const
- Modify: `internal/fantrax/period_date_map.go` — add `periodDateMap` + `dailyPeriodForDate` methods
- Test: `internal/fantrax/period_date_map_test.go` — add hermetic resolver test

**Interfaces:**
- Consumes: `parsePeriodList` (Task 4), `GetCurrentPeriod` (Task 2), `cache.FileCache`, `stableTTL`.
- Produces: `(*Client).periodDateMap(seasonStart time.Time) (map[string]DailyPeriod, error)`, `(*Client).dailyPeriodForDate(seasonStart, date time.Time) DailyPeriod`.

- [ ] **Step 1: Add the cache-key const** in `cachekeys.go` alongside the other `key*` consts:

```go
keyPeriodDateMap = "fantrax-period-date-map"
```

- [ ] **Step 2: Add Client fields** in `client.go`'s `Client` struct:

```go
periodMapMu   sync.Mutex
periodMapMemo map[string]DailyPeriod
```

- [ ] **Step 3: Write the hermetic resolver test** (pre-seed the memo so no auth/network is touched)

```go
func TestDailyPeriodForDate(t *testing.T) {
	seasonStart := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	c := &Client{periodMapMemo: map[string]DailyPeriod{
		"2026-07-06": 104,
	}}
	d := func(y int, m time.Month, day int) time.Time {
		return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	}
	// hit → authoritative period from the map
	if got := c.dailyPeriodForDate(seasonStart, d(2026, 7, 6)); got != 104 {
		t.Errorf("hit: got %d, want 104", got)
	}
	// miss → soft-fallback to naive PeriodForDate
	miss := d(2026, 4, 1)
	if got := c.dailyPeriodForDate(seasonStart, miss); got != PeriodForDate(seasonStart, miss) {
		t.Errorf("miss: got %d, want naive %d", got, PeriodForDate(seasonStart, miss))
	}
}
```

- [ ] **Step 4: Run it to verify it fails**

Run: `go test ./internal/fantrax/ -run TestDailyPeriodForDate`
Expected: FAIL — `c.dailyPeriodForDate undefined`.

- [ ] **Step 5: Implement `periodDateMap` + `dailyPeriodForDate`** in `period_date_map.go`

```go
// periodDateMap returns the authoritative date→DailyPeriod map for the season,
// fetched once (in-memory memoized like allMatchups, plus a season-stable file
// cache) from getTeamRosterInfo's DisplayedLists["periodList"]. cacheDir=="" (and
// a pre-seeded periodMapMemo, as in tests) skips the network entirely.
func (c *Client) periodDateMap(seasonStart time.Time) (map[string]DailyPeriod, error) {
	c.periodMapMu.Lock()
	defer c.periodMapMu.Unlock()
	if c.periodMapMemo != nil {
		return c.periodMapMemo, nil
	}
	build := func() (map[string]DailyPeriod, error) {
		cur, err := c.GetCurrentPeriod()
		if err != nil {
			return nil, err
		}
		raw, err := c.auth.GetTeamRosterInfoRaw(strconv.Itoa(int(cur)), c.teamID)
		if err != nil {
			return nil, err
		}
		if len(raw.Responses) == 0 {
			return nil, fmt.Errorf("period map: empty responses")
		}
		pl, _ := raw.Responses[0].Data.DisplayedLists["periodList"].([]interface{})
		if len(pl) == 0 {
			return nil, fmt.Errorf("period map: empty periodList")
		}
		return parsePeriodList(pl, seasonStart.Year(), seasonStart.Month()), nil
	}

	var (
		m   map[string]DailyPeriod
		err error
	)
	if c.cacheDir == "" {
		m, err = build()
	} else {
		fc := cache.New[map[string]DailyPeriod](c.cacheDir, stableTTL)
		key := cache.Key(keyPeriodDateMap, c.leagueID, strconv.Itoa(seasonStart.Year()))
		m, err = fc.Get(key, build)
	}
	if err != nil {
		return nil, err
	}
	c.periodMapMemo = m
	return m, nil
}

// dailyPeriodForDate resolves a calendar date to its authoritative DailyPeriod via
// the periodList map, soft-falling back to naive season-start day math on any
// miss/fetch error. This is the rosterbot-ren fix for the historical FP/GS walks;
// the fallback keeps hermetic tests and credential-less renders working as before.
func (c *Client) dailyPeriodForDate(seasonStart, date time.Time) DailyPeriod {
	if m, err := c.periodDateMap(seasonStart); err == nil {
		if p, ok := m[date.Format("2006-01-02")]; ok {
			return p
		}
	}
	return PeriodForDate(seasonStart, date)
}
```

Add `"github.com/pmurley/go-fantrax/auth_client"`? Not needed here (no auth_client types referenced). Ensure imports: `fmt`, `strconv`, `time`, and `internal/cache`. `sync` is imported by `client.go`, not this file.

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/fantrax/ -run TestDailyPeriodForDate -v`
Expected: PASS.

- [ ] **Step 7: Full gate + commit**

Run: `go build ./... && go vet ./... && go test ./internal/...`
Expected: PASS.
```bash
git add -A
git commit -m "feat(fantrax): authoritative periodDateMap + dailyPeriodForDate resolver (rosterbot-ren)"
```

---

### Task 6: Wire the map into the historical FP/GS walks

**Files:**
- Modify: `internal/fantrax/daily_fpts.go` — 3 `PeriodForDate` call sites
- Modify: `internal/fantrax/pitcher_starts.go` — 2 `PeriodForDate` call sites

**Interfaces:**
- Consumes: `(*Client).dailyPeriodForDate` (Task 5). No exported signatures change.

- [ ] **Step 1: `daily_fpts.go`** — replace the three naive lookups (`DailyFantasyPoints` is a `*Client` method, so `c` is in scope):
  - `curPeriod := PeriodForDate(seasonStart, time.Now().UTC())` → `curPeriod := c.dailyPeriodForDate(seasonStart, time.Now().UTC())`
  - `basePeriod := PeriodForDate(seasonStart, dayBefore)` → `basePeriod := c.dailyPeriodForDate(seasonStart, dayBefore)`
  - `period := PeriodForDate(seasonStart, d)` → `period := c.dailyPeriodForDate(seasonStart, d)`

- [ ] **Step 2: `pitcher_starts.go`** — replace the two naive lookups in `GetTeamPitcherStarts` (also a `*Client` method):
  - `basePeriod := PeriodForDate(seasonStart, dayBefore)` → `basePeriod := c.dailyPeriodForDate(seasonStart, dayBefore)`
  - `period := PeriodForDate(seasonStart, d)` → `period := c.dailyPeriodForDate(seasonStart, d)`

- [ ] **Step 3: Gate**

Run: `go build ./... && go vet ./... && go test ./internal/...`
Expected: PASS (types already align — `dailyPeriodForDate` returns `DailyPeriod`, matching the now-`DailyPeriod` locals/fields).

- [ ] **Step 4: End-to-end smoke** — confirm the map is fetched once, not per-day, and nothing regressed:

Run: `make clean-cache && make run-all` then `make run-all` again.
Expected: both complete; the second (warm) run shows `cache hit:` for `fantrax-period-date-map-*`; `backtest`/`recap` steps succeed. (If creds/live API are unavailable locally, `dailyPeriodForDate` soft-falls-back and the walks behave exactly as before — a clean degradation, not a failure.)

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "fix(fantrax): resolve historical FP/GS dates via authoritative periodList map (rosterbot-ren)"
```

---

### Task 7: Docs + close issues

**Files:**
- Modify: `CLAUDE.md` — update rosterbot-ren references

- [ ] **Step 1: Update CLAUDE.md**

In the `internal/backtest` and `internal/fantrax` sections, change the rosterbot-ren "still-open" / "accepted gap" / "Class B still uses season-start day math" language to note it's **resolved** via the authoritative `periodList` date→daily-period map (`period_date_map.go`, `dailyPeriodForDate`), fetched once and cached season-stable. Keep the Class-A description and add that unifying it onto the same map is the deferred **rosterbot-2ax** follow-up. Leave the `DailyPeriodFor`/`AnchorPeriodForDate` prose (Class A) intact.

- [ ] **Step 2: Final full gate**

Run: `go mod tidy && go build ./... && go vet ./... && go test ./internal/...`
Expected: PASS; `go mod tidy` produces no diff (no new deps).

- [ ] **Step 3: Commit + close issues**

```bash
git add -A
git commit -m "docs: rosterbot-ren resolved via authoritative periodList map; note rosterbot-2ax follow-up"
bd close rosterbot-1i3 rosterbot-ren --reason="WeeklyPeriod/DailyPeriod type split landed; historical FP/GS walks now resolve dates via the authoritative periodList map. Class-A unification deferred to rosterbot-2ax."
```

---

## Self-Review

- **Spec coverage:** Part A type split → Tasks 1–3 (daily + weekly axes, all listed signatures). Part B map → Tasks 4–6 (parser, resolver, wiring). CLAUDE.md + issue close → Task 7. `MatchupEntry/MatchupWeek.ScoringPeriod` left `int` per spec (untouched; no task needed — they already compile). rosterbot-2ax follow-up filed. ✔
- **Placeholder scan:** every code step shows real code; every run step shows a command + expected output. ✔
- **Type consistency:** `DailyPeriod` return of `PeriodForDate`/`AnchorPeriodForDate`/`DailyPeriodFor`/`GetCurrentPeriod`/`dailyPeriodForDate` matches the `DailyPeriod` params of `ApplyLineup`/roster/GS helpers and the `dateResult.period`/`DayRoster.Period`/`LineupDayResult.Period` fields. `WeeklyPeriod` return of `ScoringPeriod.Number` matches the `GetGSLimits` param. `parsePeriodList` returns `map[string]DailyPeriod`, consumed by `periodDateMap`/`dailyPeriodForDate`. ✔
- **Testing refinement vs spec:** spec said "tests unchanged"; corrected here — tests need mechanical retyping of `int` table fields/locals, no value changes (Task 2 Step 10, Task 3 Step 4).

# Narrow Fantrax Interfaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the concrete `*fantrax.Client` parameter on `gscheck.RunGSCheck`, `recap.Run`/`RunSite`, and `lineuprun.Run` with consumer-declared narrow interfaces, and land one fake-driven orchestration test per package.

**Architecture:** Consumer-declared, minimal interfaces (Go idiom). `internal/fantrax` is untouched; `*fantrax.Client` satisfies each interface implicitly, so no cmd caller changes. Layered by embedding where a helper needs only a sub-slice of the surface.

**Tech Stack:** Go, standard `testing`, table-driven tests, in-package fakes (mirrors `internal/waivers` test fakes).

## Global Constraints

- No changes to `internal/fantrax`. Verify with `git diff --stat internal/fantrax` = empty.
- Interface types reference `fantrax.*` exported types + `github.com/pmurley/go-fantrax/models`.
- Existing cmd callers (`cmd/optimize.go`, `cmd/shadow.go`, `cmd/recap.go`, `cmd/recap_site.go`, `cmd/gs_check.go`) pass a concrete `*fantrax.Client` and must keep compiling unchanged.
- `go vet ./...` and `make test` pass at the end.
- Fakes are offline: `RunGSCheck` tests set `cfg.DryRun=true` so `notify.SendPushover` is a no-op.

---

### Task 1: gscheck — GSCheckClient interface + RunGSCheck test

**Files:**
- Modify: `internal/gscheck/gscheck.go` (add interface, change `RunGSCheck` signature)
- Test: `internal/gscheck/run_test.go` (new)

**Interfaces:**
- Produces: `GSCheckClient` interface (3 methods); `RunGSCheck(ft GSCheckClient, cfg config.Config) error`.

- [ ] **Step 1: Add the interface** to `internal/gscheck/gscheck.go` (after imports, before `RunGSCheck`):

```go
// GSCheckClient is the narrow subset of *fantrax.Client that RunGSCheck needs,
// isolated for testability (mirrors waivers.FantraxClient). *fantrax.Client
// satisfies it implicitly — internal/fantrax is not modified.
type GSCheckClient interface {
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
	GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
}
```

- [ ] **Step 2: Change the signature** — `func RunGSCheck(ft *fantrax.Client, cfg config.Config) error` → `func RunGSCheck(ft GSCheckClient, cfg config.Config) error`. Body unchanged.

- [ ] **Step 3: Build to confirm callers still compile**

Run: `go build ./...`
Expected: success (cmd/gs_check.go passes a `*fantrax.Client`, which satisfies `GSCheckClient`).

- [ ] **Step 4: Write the fake + failing test** in `internal/gscheck/run_test.go`:

```go
package gscheck

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeGSClient is an in-test GSCheckClient. Per-team GS is looked up by teamID.
type fakeGSClient struct {
	periods []fantrax.ScoringPeriod
	teams   map[string]string
	min     *int
	max     *int
	gsByTeam map[string]int
}

func (f *fakeGSClient) GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error) {
	return f.periods, f.teams, map[string]string{}, nil
}
func (f *fakeGSClient) GetGSLimits(string, fantrax.WeeklyPeriod) (*int, *int, error) {
	return f.min, f.max, nil
}
func (f *fakeGSClient) GetTeamGS(teamID, _ string, _ fantrax.ScoringPeriod, _, _ time.Time, _ int, _ bool) (int, []fantrax.PitcherStart, error) {
	return f.gsByTeam[teamID], nil, nil
}

func ptr(i int) *int { return &i }

// captureStdout runs fn with os.Stdout redirected and returns everything printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig
	buf := make([]byte, 1<<16)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func TestRunGSCheck_ViolationsAndCleanTallies(t *testing.T) {
	today := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	// One just-ended period (EndDate == yesterday → FindJustEndedPeriod picks it),
	// complete (EndDate < today → min violations active).
	periods := []fantrax.ScoringPeriod{{
		Number:    5,
		Caption:   "Scoring Period 5",
		StartDate: today.AddDate(0, 0, -7),
		EndDate:   yesterday,
	}}
	cfg := config.Config{TeamID: "t1", DryRun: true}

	f := &fakeGSClient{
		periods: periods,
		teams:   map[string]string{"over": "OverTeam", "under": "UnderTeam", "ok": "OkTeam"},
		min:     ptr(7), max: ptr(12),
		gsByTeam: map[string]int{"over": 14, "under": 5, "ok": 9},
	}

	out := captureStdout(t, func() {
		if err := RunGSCheck(f, cfg); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})

	if !strings.Contains(out, "OverTeam") || !strings.Contains(out, "OVER MAX") {
		t.Errorf("expected OverTeam over-max flag; got:\n%s", out)
	}
	if !strings.Contains(out, "UnderTeam") || !strings.Contains(out, "UNDER MIN") {
		t.Errorf("expected UnderTeam under-min flag; got:\n%s", out)
	}
	if strings.Contains(out, "OkTeam: 9 GS ***") {
		t.Errorf("OkTeam (9, within 7..12) must not be flagged; got:\n%s", out)
	}
}

// A correct per-team GS tally at/above min must NOT false-fire "UNDER MIN".
// This is the previously-uncatchable regression class (rosterbot-uv6/wd5): the
// GetTeamGS daily walk once undercounted every team to ~one day's GS and fired
// a whole-league under-min alert. With the seam, a correct tally is testable.
func TestRunGSCheck_CorrectTallyNoFalseUnderMin(t *testing.T) {
	today := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	periods := []fantrax.ScoringPeriod{{
		Number: 5, Caption: "Scoring Period 5",
		StartDate: today.AddDate(0, 0, -7), EndDate: today.AddDate(0, 0, -1),
	}}
	cfg := config.Config{TeamID: "t1", DryRun: true}
	f := &fakeGSClient{
		periods: periods,
		teams:   map[string]string{"a": "Alpha", "b": "Beta"},
		min:     ptr(7), max: ptr(12),
		gsByTeam: map[string]int{"a": 8, "b": 10}, // both ≥ min, ≤ max
	}
	out := captureStdout(t, func() {
		if err := RunGSCheck(f, cfg); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})
	if !strings.Contains(out, "No violations found.") {
		t.Errorf("expected no violations; got:\n%s", out)
	}
	if strings.Contains(out, "UNDER MIN") {
		t.Errorf("false UNDER MIN on a correct tally; got:\n%s", out)
	}
}

func TestRunGSCheck_NotEndOfPeriod(t *testing.T) {
	today := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	// EndDate is 3 days out → no just-ended period → clean no-op.
	periods := []fantrax.ScoringPeriod{{
		Number: 5, Caption: "Scoring Period 5",
		StartDate: today.AddDate(0, 0, -4), EndDate: today.AddDate(0, 0, 3),
	}}
	f := &fakeGSClient{periods: periods, teams: map[string]string{"a": "Alpha"}, min: ptr(7), max: ptr(12)}
	out := captureStdout(t, func() {
		if err := RunGSCheck(f, config.Config{TeamID: "t1", DryRun: true}); err != nil {
			t.Fatalf("RunGSCheck: %v", err)
		}
	})
	if !strings.Contains(out, "Nothing to check") {
		t.Errorf("expected nothing-to-check no-op; got:\n%s", out)
	}
}
```

Note: `RunGSCheck` uses `today := time.Now().UTC()` internally, so tests must build `periods` relative to `time.Now()`, not a fixed date. **In the actual test, compute `today` as `time.Now().UTC().Truncate(24*time.Hour)` and derive period dates from it** (the fixed 2026-07-20 above is illustrative; swap it for `time.Now()`-relative dates so `FindJustEndedPeriod` matches). The 3-team test sleeps 500ms/team (~1.5s) — acceptable.

- [ ] **Step 5: Run the test to verify it drives the real code**

Run: `go test ./internal/gscheck/ -run TestRunGSCheck -v`
Expected: PASS (all three subtests).

- [ ] **Step 6: Confirm fantrax untouched + commit**

```bash
git diff --stat internal/fantrax   # must be empty
git add internal/gscheck/gscheck.go internal/gscheck/run_test.go
git commit -m "refactor(gscheck): RunGSCheck takes narrow GSCheckClient + orchestration test

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: recap — layered interfaces + season-mean and leaders tests

**Files:**
- Modify: `internal/recap/leaders.go` (add `leadersClient`, change `buildLeaders` signature)
- Modify: `internal/recap/recap.go` (add `seasonMeanClient`, `RecapClient`; change `Run`, `collectTeam`, `seasonToDateTeamMean`, `fetchSeasonMeans` signatures)
- Modify: `internal/recap/site.go` (add `SiteClient`, change `RunSite` signature)
- Test: `internal/recap/run_iface_test.go` (new)

**Interfaces:**
- Consumes: existing `matchupWeekProvider` (site.go), `models.PoolPlayer`.
- Produces: `leadersClient`, `seasonMeanClient`, `RecapClient`, `SiteClient`.

- [ ] **Step 1: leaders.go** — add before `buildLeaders`:

```go
// leadersClient is the fantrax subset buildLeaders needs (the rest of the
// leaderboard data comes from statcast + statsapi, not fantrax).
type leadersClient interface {
	GetFullPlayerPool() ([]models.PoolPlayer, error)
}
```
Change `func buildLeaders(ft *fantrax.Client, ...)` → `func buildLeaders(ft leadersClient, ...)`.

- [ ] **Step 2: recap.go** — add near the top (after imports):

```go
// seasonMeanClient is the fantrax subset the season-to-date team-mean helpers use.
type seasonMeanClient interface {
	DailyFantasyPoints(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.DayRoster, error)
}

// RecapClient is the narrow subset of *fantrax.Client that Run needs. Embeds
// the two helper interfaces so Run can hand its ft straight to buildLeaders,
// fetchSeasonMeans and collectTeam.
type RecapClient interface {
	leadersClient
	seasonMeanClient
	GetSeasonDateRange() (time.Time, time.Time, error)
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	GetActiveSlots() ([]fantrax.Slot, error)
	GetPitcherSlots() ([]fantrax.Slot, error)
	GetAllMatchupEntries() ([]fantrax.MatchupEntry, error)
	GetMatchupWeekNumberForDate(date time.Time) (int, error)
	BackfillDailyFPts(days []fantrax.DayRoster) error
	GetTeamPitcherStarts(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.DatedPitcherStart, error)
}
```
Change signatures (bodies unchanged):
- `func Run(ft *fantrax.Client, opts Options) (*Recap, error)` → `func Run(ft RecapClient, opts Options) (*Recap, error)`
- `func collectTeam(ft *fantrax.Client, ...)` → `func collectTeam(ft RecapClient, ...)`
- `func seasonToDateTeamMean(ft *fantrax.Client, ...)` → `func seasonToDateTeamMean(ft seasonMeanClient, ...)`
- `func fetchSeasonMeans(ft *fantrax.Client, ...)` → `func fetchSeasonMeans(ft seasonMeanClient, ...)`

- [ ] **Step 3: site.go** — add after `matchupWeekProvider`:

```go
// SiteClient is the fantrax subset RunSite needs: everything Run needs, plus
// the per-number matchup-week lookup used to enumerate completed weeks.
type SiteClient interface {
	RecapClient
	matchupWeekProvider
}
```
Change `func RunSite(ft *fantrax.Client, sopts SiteOptions) error` → `func RunSite(ft SiteClient, sopts SiteOptions) error`. Body unchanged (`Run(ft, ...)` and `completedMatchupWeeks(ft, ...)` both compile: `SiteClient` ⊇ `RecapClient` and ⊇ `matchupWeekProvider`).

- [ ] **Step 4: Build to confirm callers still compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Write fakes + failing tests** in `internal/recap/run_iface_test.go`:

```go
package recap

import (
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/models"
)

// fakeSeasonMeanClient returns a fixed day series per team for DailyFantasyPoints.
type fakeSeasonMeanClient struct {
	daysByTeam map[string][]fantrax.DayRoster
}

func (f *fakeSeasonMeanClient) DailyFantasyPoints(teamID string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	return f.daysByTeam[teamID], nil
}

func day(d time.Time, activeFPts ...float64) fantrax.DayRoster {
	var ps []fantrax.DayPlayerFP
	for _, fp := range activeFPts {
		ps = append(ps, fantrax.DayPlayerFP{Active: true, HadGame: true, FPts: fp})
	}
	return fantrax.DayRoster{Date: d, Players: ps}
}

// seasonToDateTeamMean sums active FPts per day and divides by played days.
func TestSeasonToDateTeamMean_MeanOfActiveDailyFPts(t *testing.T) {
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	asOf := seasonStart.AddDate(0, 0, 2)
	f := &fakeSeasonMeanClient{daysByTeam: map[string][]fantrax.DayRoster{
		"t1": {
			day(seasonStart, 10, 20),          // day total 30
			day(seasonStart.AddDate(0, 0, 1)), // no activity → skipped
			day(seasonStart.AddDate(0, 0, 2), 40), // day total 40
		},
	}}
	mean, played, err := seasonToDateTeamMean(f, "t1", seasonStart, asOf, "", 0)
	if err != nil {
		t.Fatalf("seasonToDateTeamMean: %v", err)
	}
	if played != 2 {
		t.Errorf("played = %d, want 2 (empty day skipped)", played)
	}
	if math.Abs(mean-35.0) > 1e-9 { // (30 + 40) / 2
		t.Errorf("mean = %v, want 35", mean)
	}
}

// fetchSeasonMeans returns nil (no HTTP) before season start, and a per-team
// map otherwise.
func TestFetchSeasonMeans_PreSeasonNil(t *testing.T) {
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeSeasonMeanClient{}
	got := fetchSeasonMeans(f, map[string]string{"t1": "Alpha"}, seasonStart, seasonStart.AddDate(0, 0, -1), "", 0, 1)
	if got != nil {
		t.Errorf("pre-season fetchSeasonMeans = %v, want nil", got)
	}
}

func TestFetchSeasonMeans_PerTeamMap(t *testing.T) {
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	asOf := seasonStart.AddDate(0, 0, 1)
	f := &fakeSeasonMeanClient{daysByTeam: map[string][]fantrax.DayRoster{
		"t1": {day(seasonStart, 50)},
		"t2": {day(seasonStart, 10), day(seasonStart.AddDate(0, 0, 1), 30)},
	}}
	got := fetchSeasonMeans(f, map[string]string{"t1": "Alpha", "t2": "Beta"}, seasonStart, asOf, "", 0, 2)
	if math.Abs(got["t1"]-50.0) > 1e-9 {
		t.Errorf("t1 mean = %v, want 50", got["t1"])
	}
	if math.Abs(got["t2"]-20.0) > 1e-9 { // (10 + 30)/2
		t.Errorf("t2 mean = %v, want 20", got["t2"])
	}
}

// fakeLeadersClient drives buildLeaders' fantrax seam.
type fakeLeadersClient struct {
	pool []models.PoolPlayer
	err  error
}

func (f *fakeLeadersClient) GetFullPlayerPool() ([]models.PoolPlayer, error) {
	return f.pool, f.err
}

// buildLeaders soft-fails to nil when no players are rostered (early return,
// before any statcast/statsapi network call).
func TestBuildLeaders_EmptyRosteredNil(t *testing.T) {
	f := &fakeLeadersClient{pool: []models.PoolPlayer{{FantasyTeamID: ""}}} // unrostered
	woba, fip := buildLeaders(f, 2026, time.Now().UTC(), "", 0, 5)
	if woba != nil || fip != nil {
		t.Errorf("want nil leaders for empty rostered pool, got woba=%v fip=%v", woba, fip)
	}
}

func TestBuildLeaders_PoolErrorNil(t *testing.T) {
	f := &fakeLeadersClient{err: errPoolFake}
	woba, fip := buildLeaders(f, 2026, time.Now().UTC(), "", 0, 5)
	if woba != nil || fip != nil {
		t.Errorf("want nil leaders on pool error, got woba=%v fip=%v", woba, fip)
	}
}

var errPoolFake = fmtErrorf("boom")

// tiny local error helper to avoid an errors import churn in the test file
func fmtErrorf(s string) error { return &strErr{s} }

type strErr struct{ s string }

func (e *strErr) Error() string { return e.s }
```

Note: if `models.PoolPlayer`'s rostered field is not `FantasyTeamID`, match `rosteredPlayers` in `leaders.go` (it filters on `p.FantasyTeamID != ""`). Confirm the field name compiles.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/recap/ -run 'TestSeasonToDateTeamMean|TestFetchSeasonMeans|TestBuildLeaders' -v`
Expected: PASS.

- [ ] **Step 7: Full package test + coverage delta**

Run: `go test ./internal/recap/ -cover`
Expected: PASS, coverage > 50.4% (baseline). recap.go/leaders.go no longer 0%.

- [ ] **Step 8: Confirm fantrax untouched + commit**

```bash
git diff --stat internal/fantrax   # empty
git add internal/recap/
git commit -m "refactor(recap): Run/RunSite/buildLeaders take narrow interfaces + season-mean & leaders tests

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: lineuprun — LineupClient interface + windowedHitterRecent test

**Files:**
- Modify: `internal/lineuprun/recent.go` (change `windowedHitterRecent` signature)
- Modify: `internal/lineuprun/lineuprun.go` (add `recentStatsClient`, `LineupClient`; change `Run` signature)
- Test: `internal/lineuprun/recent_test.go` (new)

**Interfaces:**
- Produces: `recentStatsClient` (3 methods), `LineupClient` (21 methods, embeds `recentStatsClient`); `Run(ft LineupClient, cfg *config.Config, opts Options)`.

- [ ] **Step 1: lineuprun.go** — add after the `Options`/`Result` types:

```go
// recentStatsClient is the fantrax subset windowedHitterRecent uses.
type recentStatsClient interface {
	GetSeasonDateRange() (time.Time, time.Time, error)
	DailyFantasyPoints(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.DayRoster, error)
	BackfillDailyFPts(days []fantrax.DayRoster) error
}

// LineupClient is the narrow subset of *fantrax.Client that Run needs. Embeds
// recentStatsClient so Run can hand its ft to windowedHitterRecent. Landing
// this seam is what lets rosterbot-6rv's phase-level tests inject a fake.
type LineupClient interface {
	recentStatsClient
	GetHitterRoster() ([]fantrax.Player, error)
	GetPitcherRoster() ([]fantrax.Player, error)
	GetFullHitterRoster() ([]fantrax.Player, fantrax.SlotCounts, error)
	GetActiveSlots() ([]fantrax.Slot, error)
	GetPitcherSlots() ([]fantrax.Slot, error)
	GetScoringWeights() (fantrax.ScoringWeights, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
	GetCurrentPeriod() (fantrax.DailyPeriod, error)
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	DailyPeriodFor(currentPeriod fantrax.DailyPeriod, seasonStart, today, date time.Time) fantrax.DailyPeriod
	GetHitterRosterForPeriod(period fantrax.DailyPeriod) ([]fantrax.Player, error)
	GetPitcherRosterForPeriod(period fantrax.DailyPeriod) ([]fantrax.Player, error)
	GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
	GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
	GetRecentPitcherStats(currentPeriod fantrax.DailyPeriod, n int) (map[string]fantrax.RecentStat, error)
	ApplyLineup(period fantrax.DailyPeriod, active []fantrax.PlayerSlot, reserve []string) error
	InvalidatePeriodRosterCache(period fantrax.DailyPeriod)
}
```
Change `func Run(ft *fantrax.Client, cfg *config.Config, opts Options) (Result, error)` → `func Run(ft LineupClient, cfg *config.Config, opts Options) (Result, error)`. Body unchanged.

- [ ] **Step 2: recent.go** — change `func windowedHitterRecent(ft *fantrax.Client, ...)` → `func windowedHitterRecent(ft recentStatsClient, ...)`. Body unchanged.

- [ ] **Step 3: Build to confirm callers still compile**

Run: `go build ./...`
Expected: success (`cmd/optimize.go`, `cmd/shadow.go` pass `*fantrax.Client`).

- [ ] **Step 4: Write the fake + failing test** in `internal/lineuprun/recent_test.go`:

```go
package lineuprun

import (
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// fakeRecentClient serves a fixed daily series; seasonStart is passed non-zero
// so GetSeasonDateRange is never called (returns an error if it is, to catch
// accidental use).
type fakeRecentClient struct {
	days []fantrax.DayRoster
}

func (f *fakeRecentClient) GetSeasonDateRange() (time.Time, time.Time, error) {
	return time.Time{}, time.Time{}, errUnexpectedSeasonRange
}
func (f *fakeRecentClient) DailyFantasyPoints(_ string, _, _, _ time.Time, _ string, _ time.Duration) ([]fantrax.DayRoster, error) {
	return f.days, nil
}
func (f *fakeRecentClient) BackfillDailyFPts([]fantrax.DayRoster) error { return nil }

var errUnexpectedSeasonRange = &recentErr{"GetSeasonDateRange should not be called when seasonStart is set"}

type recentErr struct{ s string }

func (e *recentErr) Error() string { return e.s }

func hitterDay(d time.Time, playerID string, fp float64) fantrax.DayRoster {
	return fantrax.DayRoster{Date: d, Players: []fantrax.DayPlayerFP{
		{PlayerID: playerID, IsPitcher: false, HadGame: true, FPts: fp},
	}}
}

// windowedHitterRecent collapses the daily series into per-player FP/game +
// games-in-window (trailing 30d, WindowWeight uniform 1 within the window,
// leakage guard excludes the as-of day).
func TestWindowedHitterRecent_WindowMean(t *testing.T) {
	today := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	days := []fantrax.DayRoster{
		hitterDay(today.AddDate(0, 0, -2), "p1", 10), // in window, played
		hitterDay(today.AddDate(0, 0, -1), "p1", 20), // in window, played
		hitterDay(today, "p1", 999),                  // as-of day → excluded by leakage guard
	}
	f := &fakeRecentClient{days: days}
	got, err := windowedHitterRecent(f, "t1", today, seasonStart, false)
	if err != nil {
		t.Fatalf("windowedHitterRecent: %v", err)
	}
	rs, ok := got["p1"]
	if !ok {
		t.Fatalf("p1 missing from recency map")
	}
	if rs.GamesPlayed != 2 {
		t.Errorf("GamesPlayed = %d, want 2 (as-of day excluded)", rs.GamesPlayed)
	}
	if math.Abs(rs.FPtsPerGame-15.0) > 1e-9 { // (10 + 20)/2
		t.Errorf("FPtsPerGame = %v, want 15", rs.FPtsPerGame)
	}
}
```

- [ ] **Step 5: Run the test**

Run: `go test ./internal/lineuprun/ -run TestWindowedHitterRecent -v`
Expected: PASS.

- [ ] **Step 6: Confirm fantrax untouched + commit**

```bash
git diff --stat internal/fantrax   # empty
git add internal/lineuprun/lineuprun.go internal/lineuprun/recent.go internal/lineuprun/recent_test.go
git commit -m "refactor(lineuprun): Run takes narrow LineupClient + windowedHitterRecent test

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Final verification

- [ ] **Step 1: Vet + full test suite**

Run: `go vet ./... && make test`
Expected: no vet findings; all packages pass.

- [ ] **Step 2: Confirm the whole change left fantrax untouched**

Run: `git diff --stat main -- internal/fantrax`
Expected: empty.

- [ ] **Step 3: Coverage confirmation for acceptance criterion 5**

Run: `go test ./internal/recap/ ./internal/gscheck/ ./internal/lineuprun/ -cover`
Expected: recap > 50.4%, gscheck > 27.7%, lineuprun > 2.7% (all baselines).

- [ ] **Step 4: Close the issue**

```bash
bd close rosterbot-phb --reason="Consumer-declared narrow Fantrax interfaces landed for gscheck/recap/lineuprun with fake-driven orchestration tests; fantrax untouched."
```

## Self-Review

- **Spec coverage:** every interface in the spec maps to a task (Task 1 gscheck, Task 2 recap ×4 interfaces, Task 3 lineuprun ×2). Tests map to criteria 3–5. Scope boundary (no full-Run test) honored.
- **Placeholder scan:** the fixed-date note in Task 1 Step 4 flags the one thing the implementer must adapt (`time.Now()`-relative periods) — called out explicitly, not left implicit.
- **Type consistency:** interface method names/signatures copied verbatim from `internal/fantrax` source greps; `RecapClient` embeds `leadersClient`+`seasonMeanClient`; `SiteClient` embeds `RecapClient`+`matchupWeekProvider`; `LineupClient` embeds `recentStatsClient`.

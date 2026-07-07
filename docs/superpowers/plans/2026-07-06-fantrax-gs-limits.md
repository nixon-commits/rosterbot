# Live Fantrax GS Position Limits Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fetch the real Fantrax-configured Games-Started min/max per scoring period (instead of
guessing via the static `GS_MAX`/`GS_MIN` env vars), fixing a confirmed-live bug where the static
values are wrong for any period that spans more than one calendar week (season opener, All-Star
break).

**Architecture:** A new `go-fantrax` library endpoint (`GetTeamRosterPositionCounts`, hitting
`getTeamRosterInfo?view=GAMES_PER_POS`) is added upstream via PR. Rosterbot's `internal/fantrax`
gets a cached wrapper (`GetGSLimits`) that calls it and extracts the pitcher games-started row.
`cmd/optimize.go` and `internal/gscheck` swap their static `cfg.GSMax`/`cfg.GSMin` reads for
`GetGSLimits` calls, falling back to the env vars if the live fetch errors.

**Tech Stack:** Go 1.26, `github.com/pmurley/go-fantrax` (forked as `github.com/nixon-commits/go-fantrax`),
`gh` CLI for the PR.

## Global Constraints

- Design doc: `docs/superpowers/specs/2026-07-06-fantrax-gs-limits-design.md` — this plan implements
  it exactly; do not deviate from the decisions table there.
- `GS_MAX`/`GS_MIN` env vars are kept as fallback-only, not removed (per that design doc).
- go-fantrax work happens in `/Users/jnixon/go-fantrax` (a separate local clone with `origin` =
  `pmurley/go-fantrax`, `fork` = `nixon-commits/go-fantrax`), not inside the rosterbot repo.
- This project uses `bd` (beads) for task tracking, not TodoWrite — Task 1 creates the tracking
  issue.

---

### Task 1: go-fantrax — add the GAMES_PER_POS endpoint

**Files:**
- Create: `/Users/jnixon/go-fantrax/auth_client/get_team_roster_position_counts.go`
- Create: `/Users/jnixon/go-fantrax/auth_client/get_team_roster_position_counts_test.go`

**Interfaces:**
- Produces (consumed by Task 4): `func (c *Client) GetTeamRosterPositionCounts(teamID, scoringPeriod string) (*GamesPerPosition, error)`, `type GamesPerPosition struct { Positions []PositionCount; CategoryLimits []CategoryLimit }`, `type CategoryLimit struct { Category string; Total int; Min, Max *int }`, `type PositionCount struct { Name, ShortName string; GP int; Min, Max *int }`, all in package `auth_client`.

- [ ] **Step 1: Create a bd tracking issue in rosterbot**

Run (from `/Users/jnixon/rosterbot`):
```bash
bd create --title="Fetch live Fantrax GS position limits instead of static GS_MAX/GS_MIN" \
  --description="Static GS_MAX/GS_MIN env vars diverge from Fantrax's real per-period limits whenever a scoring period spans more than one calendar week (confirmed: period 1 real 17/21, period 16 real 15/19, both vs configured 10/12). Add go-fantrax GetTeamRosterPositionCounts endpoint, wire internal/fantrax.GetGSLimits, update cmd/optimize.go and internal/gscheck to use it with env-var fallback. See docs/superpowers/specs/2026-07-06-fantrax-gs-limits-design.md." \
  --type=bug --priority=1
```
Note the returned issue ID (e.g. `rosterbot-XXX`) — use it in every subsequent commit message in
this plan as `(rosterbot-XXX)`.

- [ ] **Step 2: Claim the issue and branch off updated main in go-fantrax**

```bash
cd /Users/jnixon/rosterbot && bd update rosterbot-XXX --claim
cd /Users/jnixon/go-fantrax
git checkout main
git pull origin main --ff-only
git checkout -b add-games-per-position-endpoint
```

- [ ] **Step 3: Write the endpoint file**

Create `/Users/jnixon/go-fantrax/auth_client/get_team_roster_position_counts.go`:

```go
package auth_client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// GamesPerPositionResponse is the raw top-level Fantrax response for
// getTeamRosterInfo?view=GAMES_PER_POS (the "Min/Max" tab of the Team
// Roster screen).
type GamesPerPositionResponse struct {
	Responses []struct {
		Data GamesPerPositionResponseData `json:"data"`
	} `json:"responses"`
}

// GamesPerPositionResponseData holds the two tables the GAMES_PER_POS view
// returns: per-fielding-position games played, and per-scoring-category
// totals (e.g. "Games Started - Pitching (GS)").
type GamesPerPositionResponseData struct {
	GamePlayedPerPosData struct {
		TableData []PositionCountRow `json:"tableData"`
	} `json:"gamePlayedPerPosData"`
	ScMinMaxData struct {
		TableData []CategoryLimitRow `json:"tableData"`
	} `json:"scMinMaxData"`
}

// PositionCountRow is one row of the per-position games-played table.
type PositionCountRow struct {
	Pos      string `json:"pos"`
	PosShort string `json:"posShort"`
	GP       int    `json:"gp"`
	Min      string `json:"min"` // numeric string, or "No min"
	Max      string `json:"max"` // numeric string, or "No max"
}

// CategoryLimitRow is one row of the per-scoring-category totals table.
type CategoryLimitRow struct {
	ScoringCategory string `json:"scoringCategory"`
	Total           string `json:"total"`
	Min             string `json:"min"` // numeric string, or "No min"
	Max             string `json:"max"` // numeric string, or "No max"
}

// PositionCount is a single fielding position's games-played count for a
// scoring period, with its configured min/max (nil = no limit configured).
type PositionCount struct {
	Name      string
	ShortName string
	GP        int
	Min       *int
	Max       *int
}

// CategoryLimit is a single scoring category's (e.g. "Games Started -
// Pitching (GS)") total and configured min/max for a scoring period (nil =
// no limit configured).
type CategoryLimit struct {
	Category string
	Total    int
	Min      *int
	Max      *int
}

// GamesPerPosition is the parsed "Min/Max" tab of the Team Roster screen:
// per-position games-played counts plus per-scoring-category totals, each
// with their configured min/max.
type GamesPerPosition struct {
	Positions      []PositionCount
	CategoryLimits []CategoryLimit
}

// parseMinMax converts a Fantrax min/max cell to *int. Fantrax returns
// either a numeric string ("10") or the literal sentinel "No min"/"No max"
// when the position/category has no configured limit.
func parseMinMax(s string) *int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

// ProcessGamesPerPosition converts the raw API response into the typed
// GamesPerPosition result.
func ProcessGamesPerPosition(raw *GamesPerPositionResponse) (*GamesPerPosition, error) {
	if len(raw.Responses) == 0 {
		return nil, fmt.Errorf("no response data found")
	}
	data := raw.Responses[0].Data
	result := &GamesPerPosition{}
	for _, row := range data.GamePlayedPerPosData.TableData {
		result.Positions = append(result.Positions, PositionCount{
			Name:      row.Pos,
			ShortName: row.PosShort,
			GP:        row.GP,
			Min:       parseMinMax(row.Min),
			Max:       parseMinMax(row.Max),
		})
	}
	for _, row := range data.ScMinMaxData.TableData {
		total, _ := strconv.Atoi(row.Total)
		result.CategoryLimits = append(result.CategoryLimits, CategoryLimit{
			Category: row.ScoringCategory,
			Total:    total,
			Min:      parseMinMax(row.Min),
			Max:      parseMinMax(row.Max),
		})
	}
	return result, nil
}

// GetTeamRosterPositionCounts fetches and parses the "Min/Max" tab
// (getTeamRosterInfo?view=GAMES_PER_POS) for the given team and scoring
// period. scoringPeriod is the weekly Scoring Period number — the same
// numbering GetStandings(WithStandingsView(StandingsViewSchedule)) returns
// — pass "" for the current period.
func (c *Client) GetTeamRosterPositionCounts(teamID, scoringPeriod string) (*GamesPerPosition, error) {
	data := map[string]string{
		"leagueId": c.LeagueID,
		"teamId":   teamID,
		"view":     "GAMES_PER_POS",
	}
	if scoringPeriod != "" {
		data["scoringPeriod"] = scoringPeriod
	}

	fullRequest := buildFullRequest(
		[]FantraxMessage{{Method: "getTeamRosterInfo", Data: data}},
		fmt.Sprintf("https://www.fantrax.com/fantasy/league/%s/team/roster", c.LeagueID),
	)

	jsonStr, err := json.Marshal(fullRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request payload: %w", err)
	}

	req, err := http.NewRequest("POST", "https://www.fantrax.com/fxpa/req?leagueId="+c.LeagueID, bytes.NewBuffer(jsonStr))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned non-200 status code: %d", resp.StatusCode)
	}

	body, err := readBody(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var raw GamesPerPositionResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return ProcessGamesPerPosition(&raw)
}
```

- [ ] **Step 4: Write the failing/passing test**

Create `/Users/jnixon/go-fantrax/auth_client/get_team_roster_position_counts_test.go`:

```go
package auth_client

import (
	"encoding/json"
	"testing"
)

const testGamesPerPosJSON = `{
  "responses": [
    {
      "data": {
        "gamePlayedPerPosData": {
          "tableData": [
            {"pos": "Catcher (C)", "posShort": "C", "gp": 12, "min": "No min", "max": "No max"},
            {"pos": "Pitcher (P)", "posShort": "P", "gp": 15, "min": "5", "max": "20"}
          ]
        },
        "scMinMaxData": {
          "tableData": [
            {"scoringCategory": "Games Started - Pitching (GS)", "total": "15", "min": "15", "max": "19"},
            {"scoringCategory": "Innings Pitched (IP)", "total": "42", "min": "No min", "max": "No max"}
          ]
        }
      }
    }
  ]
}`

func TestProcessGamesPerPosition(t *testing.T) {
	var raw GamesPerPositionResponse
	if err := json.Unmarshal([]byte(testGamesPerPosJSON), &raw); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	result, err := ProcessGamesPerPosition(&raw)
	if err != nil {
		t.Fatalf("ProcessGamesPerPosition: %v", err)
	}

	if len(result.Positions) != 2 {
		t.Fatalf("expected 2 positions, got %d", len(result.Positions))
	}
	catcher := result.Positions[0]
	if catcher.Min != nil || catcher.Max != nil {
		t.Errorf("expected Catcher min/max nil (No min/No max), got min=%v max=%v", catcher.Min, catcher.Max)
	}
	pitcher := result.Positions[1]
	if pitcher.Min == nil || *pitcher.Min != 5 {
		t.Errorf("expected Pitcher min=5, got %v", pitcher.Min)
	}
	if pitcher.Max == nil || *pitcher.Max != 20 {
		t.Errorf("expected Pitcher max=20, got %v", pitcher.Max)
	}

	if len(result.CategoryLimits) != 2 {
		t.Fatalf("expected 2 category limits, got %d", len(result.CategoryLimits))
	}
	gs := result.CategoryLimits[0]
	if gs.Category != "Games Started - Pitching (GS)" {
		t.Errorf("expected GS category, got %q", gs.Category)
	}
	if gs.Total != 15 {
		t.Errorf("expected total 15, got %d", gs.Total)
	}
	if gs.Min == nil || *gs.Min != 15 {
		t.Errorf("expected min=15, got %v", gs.Min)
	}
	if gs.Max == nil || *gs.Max != 19 {
		t.Errorf("expected max=19, got %v", gs.Max)
	}

	ip := result.CategoryLimits[1]
	if ip.Min != nil || ip.Max != nil {
		t.Errorf("expected IP min/max nil, got min=%v max=%v", ip.Min, ip.Max)
	}
}

func TestProcessGamesPerPosition_NoResponses(t *testing.T) {
	raw := GamesPerPositionResponse{}
	if _, err := ProcessGamesPerPosition(&raw); err == nil {
		t.Fatal("expected error for empty responses")
	}
}
```

- [ ] **Step 5: Run the tests**

```bash
cd /Users/jnixon/go-fantrax && go test ./auth_client/... -run TestProcessGamesPerPosition -v
```
Expected: both `TestProcessGamesPerPosition` and `TestProcessGamesPerPosition_NoResponses` PASS.

- [ ] **Step 6: Build the whole module and vet**

```bash
cd /Users/jnixon/go-fantrax && go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
cd /Users/jnixon/go-fantrax
git add auth_client/get_team_roster_position_counts.go auth_client/get_team_roster_position_counts_test.go
git commit -m "Add GetTeamRosterPositionCounts (getTeamRosterInfo?view=GAMES_PER_POS)

Exposes the 'Min/Max' tab of the Team Roster screen: per-position
games-played counts and per-scoring-category (e.g. Games Started)
totals, each with their configured min/max. Fantrax scales these
per scoring period rather than applying a flat limit, which callers
tracking a position/category limit need to read directly rather
than assume."
```

---

### Task 2: go-fantrax — push and open the PR

**Files:** none (git/gh operations only).

**Interfaces:**
- Consumes: the `add-games-per-position-endpoint` branch committed in Task 1.
- Produces: a pushed branch on the `fork` remote and an open PR against `pmurley/go-fantrax`, whose
  branch name/commit Task 3 points rosterbot's `go.mod` replace directive at.

- [ ] **Step 1: Push the branch to the fork remote**

```bash
cd /Users/jnixon/go-fantrax && git push fork add-games-per-position-endpoint
```

- [ ] **Step 2: Open the PR**

```bash
cd /Users/jnixon/go-fantrax
gh pr create --repo pmurley/go-fantrax \
  --head nixon-commits:add-games-per-position-endpoint \
  --title "Add GetTeamRosterPositionCounts (getTeamRosterInfo?view=GAMES_PER_POS)" \
  --body "$(cat <<'EOF'
Exposes the "Min/Max" tab of the Team Roster screen
(`getTeamRosterInfo?view=GAMES_PER_POS`): per-position games-played counts
and per-scoring-category totals (e.g. "Games Started - Pitching (GS)"),
each with their configured min/max.

Useful for any league with a position or category games-played limit —
Fantrax scales these per scoring period (e.g. a period spanning more than
one calendar week gets a proportionally larger min/max), so a consumer
tracking the limit needs to read it directly rather than assume a flat
per-week constant.

## Test plan
- [x] `go test ./auth_client/... -run TestProcessGamesPerPosition -v`
- [x] `go build ./...` / `go vet ./...`
EOF
)"
```

- [ ] **Step 3: Record the PR URL**

Run `gh pr view --repo pmurley/go-fantrax --head nixon-commits:add-games-per-position-endpoint --json url -q .url`
and keep the URL for the bd issue notes (Task 7 references it).

---

### Task 3: rosterbot — point go.mod at the new branch

**Files:**
- Modify: `/Users/jnixon/rosterbot/go.mod`
- Modify: `/Users/jnixon/rosterbot/go.sum` (via `go mod tidy`)

**Interfaces:**
- Consumes: `auth_client.GetTeamRosterPositionCounts`, `auth_client.GamesPerPosition`,
  `auth_client.CategoryLimit`, `auth_client.PositionCount` from Task 1/2's branch.
- Produces: rosterbot builds against a `go-fantrax` version containing the new endpoint, so Task 4
  can call it.

- [ ] **Step 1: Point the replace directive at the new branch**

```bash
cd /Users/jnixon/rosterbot
go mod edit -replace github.com/pmurley/go-fantrax=github.com/nixon-commits/go-fantrax@add-games-per-position-endpoint
go mod tidy
```

- [ ] **Step 2: Verify it resolves and builds**

```bash
cd /Users/jnixon/rosterbot && go build ./... 2>&1 | tail -20
```
Expected: no errors. (`go.mod`'s replace line now shows a resolved pseudo-version, e.g.
`github.com/nixon-commits/go-fantrax v0.1.14-0.20260706HHMMSS-<hash>`.)

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "Point go-fantrax replace at add-games-per-position-endpoint branch (rosterbot-XXX)

Needed to consume GetTeamRosterPositionCounts before the upstream PR merges."
```

---

### Task 4: rosterbot — `internal/fantrax.GetGSLimits`

**Files:**
- Modify: `internal/fantrax/cachekeys.go`
- Create: `internal/fantrax/gs_limits.go`
- Create: `internal/fantrax/gs_limits_test.go`

**Interfaces:**
- Consumes: `c.auth.GetTeamRosterPositionCounts(teamID, scoringPeriod string) (*auth_client.GamesPerPosition, error)` (Task 1/3), `c.cacheDir string`, `pastPeriodTTL` constant, `cache.New[T]`/`cache.Key` (existing in `internal/cache`).
- Produces (consumed by Tasks 5 & 6): `func (c *Client) GetGSLimits(teamID string, period int) (min, max *int, err error)`.

- [ ] **Step 1: Add the cache key constant**

In `internal/fantrax/cachekeys.go`, add `keyGSLimits` to the const block (alphabetical position
doesn't matter here — the file isn't sorted, just grouped by topic — append at the end before the
closing paren):

```go
	keyGSLimits           = "fantrax-gs-limits"
	keyMLBGameLog         = "mlb-game-log"
)
```

(i.e. insert the new line directly above the existing `keyMLBGameLog` line.)

- [ ] **Step 2: Write the failing test**

Create `internal/fantrax/gs_limits_test.go`:

```go
package fantrax

import (
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

func gsIntPtr(n int) *int { return &n }

func TestExtractGSLimit_Found(t *testing.T) {
	categories := []auth_client.CategoryLimit{
		{Category: "Innings Pitched (IP)", Total: 42, Min: nil, Max: nil},
		{Category: "Games Started - Pitching (GS)", Total: 15, Min: gsIntPtr(15), Max: gsIntPtr(19)},
	}
	limits := extractGSLimit(categories)
	if limits.Min == nil || *limits.Min != 15 {
		t.Errorf("expected min=15, got %v", limits.Min)
	}
	if limits.Max == nil || *limits.Max != 19 {
		t.Errorf("expected max=19, got %v", limits.Max)
	}
}

func TestExtractGSLimit_NotFound(t *testing.T) {
	categories := []auth_client.CategoryLimit{
		{Category: "Innings Pitched (IP)", Total: 42, Min: nil, Max: nil},
	}
	limits := extractGSLimit(categories)
	if limits.Min != nil || limits.Max != nil {
		t.Errorf("expected nil/nil when GS category absent, got min=%v max=%v", limits.Min, limits.Max)
	}
}

func TestExtractGSLimit_NoLimitConfigured(t *testing.T) {
	categories := []auth_client.CategoryLimit{
		{Category: "Games Started - Pitching (GS)", Total: 15, Min: nil, Max: nil},
	}
	limits := extractGSLimit(categories)
	if limits.Min != nil || limits.Max != nil {
		t.Errorf("expected nil/nil when no min/max configured, got min=%v max=%v", limits.Min, limits.Max)
	}
}
```

- [ ] **Step 3: Run it to verify it fails to compile (extractGSLimit doesn't exist yet)**

```bash
cd /Users/jnixon/rosterbot && go test ./internal/fantrax/... -run TestExtractGSLimit -v
```
Expected: FAIL — `undefined: extractGSLimit`.

- [ ] **Step 4: Implement `gs_limits.go`**

Create `internal/fantrax/gs_limits.go`:

```go
package fantrax

import (
	"strconv"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/pmurley/go-fantrax/auth_client"
)

// gsCategoryName matches the pitcher games-started row in a GAMES_PER_POS
// category-limits list ("Games Started - Pitching (GS)" in this league).
const gsCategoryName = "Games Started"

// gsLimits is the cached shape for GetGSLimits — a small struct so the two
// *int results can share one FileCache entry.
type gsLimits struct {
	Min *int `json:"min"`
	Max *int `json:"max"`
}

// GetGSLimits returns the real Fantrax-configured min/max for the pitcher
// games-started category for the given team+period, straight from the
// league's own Min/Max position-limit settings (getTeamRosterInfo?view=
// GAMES_PER_POS) rather than a guessed constant. Fantrax scales this per
// period — a period spanning more than one calendar week (season opener,
// All-Star break) gets a proportionally larger min/max than a normal 7-day
// week, which a flat env var can't express. Either return value is nil if
// that limit isn't configured for the period.
//
// Cached under fantrax-gs-limits-<teamID>-<period> at pastPeriodTTL
// unconditionally — not via ttlForPeriod, which compares against the
// unrelated daily-period numbering (see period-drift-2026 memory). Once a
// period's min/max is set at league setup time it doesn't change again,
// past or current.
func (c *Client) GetGSLimits(teamID string, period int) (min, max *int, err error) {
	if c.cacheDir == "" {
		limits, ferr := c.fetchGSLimits(teamID, period)
		return limits.Min, limits.Max, ferr
	}
	fc := cache.New[gsLimits](c.cacheDir, pastPeriodTTL)
	key := cache.Key(keyGSLimits, teamID, strconv.Itoa(period))
	limits, ferr := fc.Get(key, func() (gsLimits, error) {
		return c.fetchGSLimits(teamID, period)
	})
	return limits.Min, limits.Max, ferr
}

func (c *Client) fetchGSLimits(teamID string, period int) (gsLimits, error) {
	gpp, err := c.auth.GetTeamRosterPositionCounts(teamID, strconv.Itoa(period))
	if err != nil {
		return gsLimits{}, err
	}
	return extractGSLimit(gpp.CategoryLimits), nil
}

// extractGSLimit finds the pitcher games-started row in a GAMES_PER_POS
// category-limits list. Returns a zero gsLimits (both nil) if the category
// isn't present.
func extractGSLimit(categories []auth_client.CategoryLimit) gsLimits {
	for _, cat := range categories {
		if strings.Contains(cat.Category, gsCategoryName) {
			return gsLimits{Min: cat.Min, Max: cat.Max}
		}
	}
	return gsLimits{}
}
```

- [ ] **Step 5: Run the tests and verify they pass**

```bash
cd /Users/jnixon/rosterbot && go test ./internal/fantrax/... -run TestExtractGSLimit -v
```
Expected: all three tests PASS.

- [ ] **Step 6: Build and vet the whole module**

```bash
cd /Users/jnixon/rosterbot && go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/fantrax/cachekeys.go internal/fantrax/gs_limits.go internal/fantrax/gs_limits_test.go
git commit -m "Add internal/fantrax.GetGSLimits (rosterbot-XXX)

Wraps the new go-fantrax GetTeamRosterPositionCounts to fetch the
real Fantrax-configured GS min/max per scoring period, cached like
other per-period data. Not yet consumed by any caller."
```

---

### Task 5: rosterbot — wire `cmd/optimize.go`

**Files:**
- Modify: `cmd/optimize.go:454-563`

**Interfaces:**
- Consumes: `ft.GetMatchupWeekNumberForDate(date time.Time) (int, error)` (already exists in
  `internal/fantrax/matchup_weeks.go`), `ft.GetGSLimits(teamID string, period int) (min, max *int, err error)` (Task 4).
- Produces: `gsBudget.Limit` now sourced from the live fetch when available.

- [ ] **Step 1: Replace the GS-budget block**

Replace `cmd/optimize.go` lines 454–563 (the `if cfg.GSMax > 0 && !seasonStart.IsZero() { ... }`
block through the closing `if cfg.GSMax > 0 { ... prog.Done/prog.Warn ... }` block) with:

```go
	if cfg.GSMax > 0 && !seasonStart.IsZero() {
		weekStart, weekEnd, err := ft.GetMatchupWeekBounds(today, seasonStart)
		if err != nil {
			prog.Logf("WARNING: could not determine matchup week (%v) — GS limit disabled", err)
		} else if weekStart.IsZero() {
			prog.Logf("WARNING: no matchup week found for today — GS limit disabled")
		} else if pastGS, _, gsErr := ft.GetTeamGS(cfg.TeamID, "", fantrax.ScoringPeriod{StartDate: weekStart, EndDate: today.AddDate(0, 0, -1)}, seasonStart, today, 0, false); gsErr != nil {
			// Past GS uses the gs_check active-slot delta walk. The probables
			// list is unreliable as a GS proxy: it counts current-roster SPs
			// who were probable while sitting on bench (overcount) and misses
			// SPs dropped after starting in an active slot (undercount). The
			// walk fetches per-day roster snapshots and counts only active-slot
			// YTD GS deltas — the same source of truth gs-check uses for
			// league-wide violation detection.
			prog.Logf("WARNING: per-day GS walk failed (%v) — GS limit disabled", gsErr)
		} else {
			// Prefer the real Fantrax-configured limit for this scoring period
			// over the static GS_MAX env var — Fantrax scales the real min/max
			// whenever a period spans more than one calendar week (season
			// opener, All-Star break), which a flat env var can't express.
			// Falls back to GS_MAX if the live fetch fails for any reason.
			gsLimit := cfg.GSMax
			if periodNum, perr := ft.GetMatchupWeekNumberForDate(today); perr != nil || periodNum <= 0 {
				prog.Logf("WARNING: could not resolve scoring period number (%v) — using configured GS_MAX=%d", perr, cfg.GSMax)
			} else if _, liveMax, gerr := ft.GetGSLimits(cfg.TeamID, periodNum); gerr != nil {
				prog.Logf("WARNING: live GS limit fetch failed (%v) — using configured GS_MAX=%d", gerr, cfg.GSMax)
			} else if liveMax != nil {
				gsLimit = *liveMax
			}

			prog.Logf("GS limit: %d per week (%s to %s)",
				gsLimit,
				weekStart.Format("2006-01-02"),
				weekEnd.Format("2006-01-02"))

			spNames := rosterSPNames(pitcherRoster)
			usedGS := pastGS

			// Build forecast for remaining days (today+1 through weekEnd).
			// For confirmed probables, collect each pitcher's projected pts so
			// the gate can rank across the week by value, not just count. Cap
			// at active P slots since bench SPs don't consume GS.
			numPSlots := len(pitcherSlots)
			var forecast []optimizer.DayForecast
			for d := today.AddDate(0, 0, 1); !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
				playing, _ := schedClient.TeamsPlayingOn(d)
				probs, _ := schedClient.ProbableStarters(d)

				df := optimizer.DayForecast{Date: d}
				if len(probs) > 0 {
					for normName, team := range probs {
						p, ours := spNames[normName]
						if !ours || p.MLBTeam != team {
							continue
						}
						df.ConfirmedStarters = append(df.ConfirmedStarters, pitcherProjectedPts(p, pitcherProjSrc, pitcherScoring))
					}
					// Cap at active P slots, keeping the highest-value probables.
					if len(df.ConfirmedStarters) > numPSlots {
						sort.Slice(df.ConfirmedStarters, func(i, j int) bool {
							return df.ConfirmedStarters[i] > df.ConfirmedStarters[j]
						})
						df.ConfirmedStarters = df.ConfirmedStarters[:numPSlots]
					}
				} else {
					// No probables — estimate: roster SPs whose team plays / 5 (standard rotation),
					// capped at active P slots since only active-slot SPs consume GS.
					var spPlaying float64
					for _, p := range spNames {
						if playing[p.MLBTeam] {
							spPlaying++
						}
					}
					if spPlaying > float64(numPSlots) {
						spPlaying = float64(numPSlots)
					}
					df.Estimated = spPlaying / 5.0
				}
				forecast = append(forecast, df)
			}

			// Count today's locked active SP starters toward used GS. Only count
			// pitchers who are MLB's probable starter for their team today —
			// otherwise an active-slot SP-eligible reliever or a non-starting
			// SP whose team plays gets miscounted as a GS just because the team
			// game is locked. Probables for completed games stay in the API for
			// the day, so this captures both in-progress and final starts.
			lockedTeams, lockErr := schedClient.LockedTeams(today)
			todayProbs, probsErr := schedClient.ProbableStarters(today)
			if lockErr == nil && probsErr == nil {
				for _, p := range pitcherRoster {
					if p.Status != "Active" || p.InMinors || p.IsInjured {
						continue
					}
					if !lockedTeams[p.MLBTeam] {
						continue
					}
					if !strings.Contains(p.PosShortNames, "SP") {
						continue
					}
					if team, ok := todayProbs[projections.NormalizeName(p.Name)]; ok && team == p.MLBTeam {
						usedGS++
					}
				}
			}

			gsBudget = &optimizer.GSBudget{
				Limit:    gsLimit,
				Used:     usedGS,
				Today:    today,
				WeekEnd:  weekEnd,
				Forecast: forecast,
			}
			prog.Logf("GS budget: %d/%d used, %.1f projected future starts",
				usedGS, gsLimit, gsBudget.FutureDemand())
		}
	}
	if cfg.GSMax > 0 {
		if gsBudget != nil {
			prog.Done("GS budget", fmt.Sprintf("%d/%d used · %.1f projected", gsBudget.Used, gsBudget.Limit, gsBudget.FutureDemand()))
		} else {
			prog.Warn("GS budget", "unavailable — limit disabled")
		}
	}
```

- [ ] **Step 2: Build**

```bash
cd /Users/jnixon/rosterbot && go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 3: Run the existing optimize-related tests**

```bash
cd /Users/jnixon/rosterbot && go test ./cmd/... -v 2>&1 | tail -60
```
Expected: all PASS (this block isn't unit-tested directly today — `cmd/optimize_period_test.go`
and `cmd/optimize_snapshot_test.go` test other helpers — so this is a regression check, not new
coverage).

- [ ] **Step 4: Dry-run against a normal week to sanity-check the live path**

```bash
cd /Users/jnixon/rosterbot && go run . optimize --dry-run --dates 2026-07-06 2>&1 | grep -i "GS "
```
Expected: a `GS limit: 12 per week (...)` line (period 15's real max is 12, matching `GS_MAX`) — no
`WARNING: live GS limit fetch failed` line.

- [ ] **Step 5: Commit**

```bash
git add cmd/optimize.go
git commit -m "optimize: use live Fantrax GS limit instead of static GS_MAX (rosterbot-XXX)

Falls back to GS_MAX if the live getTeamRosterInfo?view=GAMES_PER_POS
fetch fails. Fixes the optimizer needlessly benching SP starts during
any period Fantrax merges across more than one calendar week (season
opener, All-Star break Jul 13-26)."
```

---

### Task 6: rosterbot — wire `internal/gscheck`

**Files:**
- Modify: `internal/gscheck/gscheck.go`

**Interfaces:**
- Consumes: `ft.GetGSLimits(teamID string, period int) (min, max *int, err error)` (Task 4),
  `period.Number` (already available from `fantrax.FindJustEndedPeriod`).
- Produces: `RunGSCheck` now checks violations against the live per-period min/max.

- [ ] **Step 1: Replace `RunGSCheck`**

Replace the entire body of `func RunGSCheck(ft *fantrax.Client, cfg config.Config) error { ... }`
in `internal/gscheck/gscheck.go` with:

```go
// RunGSCheck checks all teams for GS violations in the most recent scoring period.
func RunGSCheck(ft *fantrax.Client, cfg config.Config) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	fmt.Printf("Running GS check for date: %s\n", today.Format("2006-01-02"))

	fmt.Println("Fetching scoring periods and teams...")
	periods, teamMap, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return fmt.Errorf("fetch scoring periods: %w", err)
	}
	if len(periods) == 0 {
		return fmt.Errorf("no scoring periods found")
	}

	period := fantrax.FindJustEndedPeriod(periods, today)
	if period == nil {
		fmt.Println("Yesterday was not the end of a scoring period. Nothing to check.")
		return nil
	}

	// Prefer the real Fantrax-configured min/max for this period over the
	// static GS_MAX/GS_MIN env vars — Fantrax scales the real min/max
	// whenever a period spans more than one calendar week (season opener,
	// All-Star break), which a flat env var can't express. Falls back to
	// the configured values if the live fetch fails for any reason.
	gsMax, gsMin := cfg.GSMax, cfg.GSMin
	if liveMin, liveMax, gerr := ft.GetGSLimits(cfg.TeamID, period.Number); gerr != nil {
		fmt.Printf("WARNING: live GS limit fetch failed (%v) — using configured GS_MAX=%d/GS_MIN=%d\n", gerr, cfg.GSMax, cfg.GSMin)
	} else {
		if liveMax != nil {
			gsMax = *liveMax
		}
		if liveMin != nil {
			gsMin = *liveMin
		}
	}

	periodLabel := fmt.Sprintf("%s (%s – %s)", period.Caption, period.StartDate.Format("2006-01-02"), period.EndDate.Format("2006-01-02"))
	fmt.Printf("Checking: %s\n", periodLabel)
	fmt.Printf("GS max: %d\n", gsMax)
	if gsMin > 0 {
		fmt.Printf("GS min: %d\n", gsMin)
	}

	if len(teamMap) == 0 {
		return fmt.Errorf("no teams found")
	}

	// Derive season start from the earliest scoring period (period 1 = season opener).
	seasonStart := periods[0].StartDate
	for _, p := range periods {
		if p.StartDate.Before(seasonStart) {
			seasonStart = p.StartDate
		}
	}
	fmt.Printf("Found %d teams. Tallying GS for Period %d (days %s to %s)...\n",
		len(teamMap), period.Number, period.StartDate.Format("2006-01-02"), today.Format("2006-01-02"))

	var results []teamGS
	for teamID, teamName := range teamMap {
		if cfg.DryRun {
			fmt.Printf("  --- %s (per-day GS deltas) ---\n", teamName)
		}
		gs, starts, err := ft.GetTeamGS(teamID, teamName, *period, seasonStart, today, gsMax, cfg.DryRun)
		if err != nil {
			fmt.Printf("WARNING: failed to get GS for %s: %v\n", teamName, err)
			continue
		}
		fmt.Printf("  %s: %d GS\n", teamName, gs)
		results = append(results, teamGS{id: teamID, name: teamName, gs: gs, starts: starts})
		time.Sleep(500 * time.Millisecond)
	}

	// Min violations are only meaningful once the period is complete; suppress
	// them mid-week so an in-progress period doesn't generate false alerts.
	periodComplete := period.EndDate.Before(today)

	// Find violations.
	var violations []Violation
	for _, r := range results {
		if r.gs > gsMax {
			v := Violation{TeamName: r.name, GSUsed: r.gs, Kind: ViolationMax}
			// Deduct the N highest-scoring starts where N = overage.
			overage := r.gs - gsMax
			if len(r.starts) > 0 {
				sorted := make([]fantrax.PitcherStart, len(r.starts))
				copy(sorted, r.starts)
				sort.Slice(sorted, func(i, j int) bool { return sorted[i].FPts > sorted[j].FPts })
				if overage > len(sorted) {
					overage = len(sorted)
				}
				v.Deductions = sorted[:overage]
			}
			violations = append(violations, v)
		}
		if periodComplete && gsMin > 0 && r.gs < gsMin {
			violations = append(violations, Violation{TeamName: r.name, GSUsed: r.gs, Kind: ViolationMin})
		}
	}

	lineupapi.RecordOutput("gs-check", toWireResult(violations, periodLabel, gsMax, gsMin))

	// Print report.
	sort.Slice(results, func(i, j int) bool { return results[i].gs > results[j].gs })
	fmt.Printf("\n--- GS Report: %s (max=%d", periodLabel, gsMax)
	if gsMin > 0 {
		fmt.Printf(", min=%d", gsMin)
	}
	fmt.Println(") ---")
	for _, r := range results {
		flag := ""
		if r.gs > gsMax {
			flag = " *** OVER MAX ***"
		} else if periodComplete && gsMin > 0 && r.gs < gsMin {
			flag = " *** UNDER MIN ***"
		}
		fmt.Printf("  %s: %d GS%s\n", r.name, r.gs, flag)
	}

	if len(violations) == 0 {
		fmt.Println("\nNo violations found.")
		return nil
	}

	fmt.Printf("\n%d violation(s) found.\n", len(violations))
	_, shortSummary := BuildReport(violations, periodLabel, gsMax, gsMin)

	if cfg.DryRun {
		fmt.Println("\n[DRY RUN] Would send Pushover notification:")
		fmt.Printf("  %s\n", shortSummary)
		return nil
	}

	// Send Pushover notification.
	if err := notify.SendPushover(cfg.PushoverGroupKey, cfg.PushoverAPIToken, "Fantrax GS Alert", shortSummary); err != nil {
		return fmt.Errorf("send pushover: %w", err)
	}
	fmt.Println("Pushover notification sent.")

	return nil
}
```

- [ ] **Step 2: Build**

```bash
cd /Users/jnixon/rosterbot && go build ./... && go vet ./...
```
Expected: no errors.

- [ ] **Step 3: Run gscheck's existing tests**

```bash
cd /Users/jnixon/rosterbot && go test ./internal/gscheck/... -v
```
Expected: all `TestBuildReport_*` tests PASS unchanged (their signature wasn't touched).

- [ ] **Step 4: Dry-run gs-check against the most recently completed period**

```bash
cd /Users/jnixon/rosterbot && go run . gs-check --dry-run 2>&1 | head -20
```
Expected: a `GS max: N` line reflecting the live-fetched value for whatever period just ended (12
for a normal week, per the design doc's confirmed values), no crash.

- [ ] **Step 5: Commit**

```bash
git add internal/gscheck/gscheck.go
git commit -m "gscheck: use live Fantrax GS limits instead of static GS_MAX/GS_MIN (rosterbot-XXX)

Falls back to GS_MAX/GS_MIN if the live fetch fails. Fixes false
OVER MAX / missed UNDER MIN violations for any period Fantrax merges
across more than one calendar week."
```

---

### Task 7: Verify end-to-end and close out

**Files:** none (verification + bd/docs only).

**Interfaces:** none — this task only runs and observes existing code from Tasks 1–6.

- [ ] **Step 1: Full test suite**

```bash
cd /Users/jnixon/rosterbot && go test ./... 2>&1 | tail -40
```
Expected: all packages PASS.

- [ ] **Step 2: `go mod tidy` sanity check**

```bash
cd /Users/jnixon/rosterbot && go mod tidy && git diff --stat go.mod go.sum
```
Expected: no unexpected diff beyond what Task 3 already committed.

- [ ] **Step 3: Verify against all three already-confirmed periods**

```bash
cd /Users/jnixon/rosterbot
go run . optimize --dry-run --dates 2026-07-06 2>&1 | grep -i "GS limit"
go run . optimize --dry-run --dates 2026-07-13 2>&1 | grep -i "GS limit"
go run . optimize --dry-run --dates 2026-07-27 2>&1 | grep -i "GS limit"
```
Expected: `GS limit: 12 ...` (Jul 6, period 15), `GS limit: 19 ...` (Jul 13, period 16 — this is the
line that matters: confirms the All-Star break period is no longer capped at the stale 12), `GS
limit: 12 ...` (Jul 27, period 17).

- [ ] **Step 4: File a follow-up bd issue for post-merge go.mod cleanup**

```bash
bd create --title="Repoint go-fantrax replace at pmurley/go-fantrax once GAMES_PER_POS PR merges" \
  --description="rosterbot-XXX temporarily points the go-fantrax replace directive at the nixon-commits/go-fantrax add-games-per-position-endpoint branch (see go.mod). Once <PR URL from Task 2> merges to pmurley/go-fantrax main, run: go mod edit -replace github.com/pmurley/go-fantrax=github.com/nixon-commits/go-fantrax@main (or drop the replace and bump the require version if depending on pmurley/go-fantrax directly is preferred), then go mod tidy, go build ./..., go test ./..., commit." \
  --type=task --priority=3
bd dep add <new-issue-id> rosterbot-XXX
```

- [ ] **Step 5: Close the tracking issue**

```bash
bd close rosterbot-XXX --reason="Live Fantrax GS limits wired into optimize + gscheck with env-var fallback; verified against periods 15/16/17. Follow-up <new-issue-id> tracks post-merge go.mod cleanup."
```

- [ ] **Step 6: Push**

```bash
cd /Users/jnixon/rosterbot
git pull --rebase
git push
git status
```
Expected: `git status` shows "up to date with origin" and clean.

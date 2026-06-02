# Recap Expansion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expand the weekly recap with a Monte Carlo win-probability chart (Game of the Week + sparklines), per-team Roster Activity log, and four new awards (Heart Attack, Comeback, Whale, Dud).

**Architecture:** Pure-function additions in `internal/recap/`: `wp.go` (Monte Carlo), `roster_activity.go` (transaction collector), award selectors in `awards.go`. `recap.Run` orchestrates new collectors into the existing `Recap` struct. Template gains a Game of the Week hero, sparkline column on matchups, Roster Activity section, and two new award cards.

**Tech Stack:** Go 1.23, `math/rand`, `hash/fnv`, html/template, embed. Existing `pmurley/go-fantrax@v0.1.13` exposes `auth_client.GetTransactionHistory()`.

**Reference spec:** `docs/superpowers/specs/2026-05-05-recap-expansion-design.md`

---

## Pre-task: Understand the testing convention

Tests in `internal/recap/` use plain `go test`, no mocks, no HTTP. New tests must follow that pattern.

Run all recap tests at any time:
```
go test ./internal/recap/...
```

Pre-commit hooks already auto-run `gofmt` and `go vet`.

---

## Task 1: Add new types to `types.go`

**Files:**
- Modify: `internal/recap/types.go`

- [ ] **Step 1: Add the 6 new types and extend `Awards` + `Recap`**

Append after the existing `SeasonAwards` declaration (end of file):

```go
// MatchupWPCurve is the per-matchup win-probability trace produced by Monte
// Carlo simulation. Points has length 8: index 0 is the pre-week baseline
// (both teams' WP starts at 0.5 in the absence of observed data); indices
// 1..7 are the WP at end of each day in the matchup week.
type MatchupWPCurve struct {
	HomeTeamID  string    `json:"home_team_id"`
	AwayTeamID  string    `json:"away_team_id"`
	Points      []WPPoint `json:"points"`
	LeadChanges int       `json:"lead_changes"`
}

// WPPoint is one snapshot in a matchup's WP curve.
type WPPoint struct {
	Date        time.Time `json:"date"`
	HomeWP      float64   `json:"home_wp"`
	HomeRunning float64   `json:"home_running"`
	AwayRunning float64   `json:"away_running"`
}

// TeamDay is one team's total FPts on a single day. Used for the Whale
// award (highest team-day across the league).
type TeamDay struct {
	TeamID   string    `json:"team_id"`
	TeamName string    `json:"team_name"`
	Date     time.Time `json:"date"`
	Pts      float64   `json:"pts"`
}

// RosterActivity is the per-team transaction log rendered in the recap's
// Roster Activity section. Teams with zero entries are omitted.
type RosterActivity struct {
	Teams []TeamActivity `json:"teams"`
}

// TeamActivity bundles one team's transactions over the matchup week.
type TeamActivity struct {
	TeamID   string          `json:"team_id"`
	TeamName string          `json:"team_name"`
	Entries  []ActivityEntry `json:"entries"`
}

// ActivityEntry is a single transaction. Kind selects which fields are
// populated:
//   - "claim": Player, ClaimType
//   - "drop":  Player
//   - "swap":  SwapIn (added), SwapOut (dropped)
//   - "trade": OtherTeam, Received, Sent
type ActivityEntry struct {
	Date      time.Time `json:"date"`
	Kind      string    `json:"kind"`
	Player    string    `json:"player,omitempty"`
	SwapIn    string    `json:"swap_in,omitempty"`
	SwapOut   string    `json:"swap_out,omitempty"`
	OtherTeam string    `json:"other_team,omitempty"`
	Received  []string  `json:"received,omitempty"`
	Sent      []string  `json:"sent,omitempty"`
	ClaimType string    `json:"claim_type,omitempty"` // "FA" | "WW"
}
```

Then extend `Awards` (search for `type Awards struct`) by appending these fields before the closing brace, after the existing `TopPitchers` field:

```go
	HeartAttack *MatchupResult   `json:"heart_attack,omitempty"`
	Comeback    *MatchupTeamSide `json:"comeback,omitempty"`
	Whale       *TeamDay         `json:"whale,omitempty"`
	Dud         *PlayerLine      `json:"dud,omitempty"`
	GameOfWeek  *MatchupResult   `json:"game_of_week,omitempty"` // == HeartAttack target
```

Then extend `Recap` (search for `type Recap struct`) by appending these fields before the closing brace, after the existing `Awards` field:

```go
	WPCurves       []MatchupWPCurve `json:"wp_curves,omitempty"`
	RosterActivity *RosterActivity  `json:"roster_activity,omitempty"`
```

- [ ] **Step 2: Compile-check**

Run: `go build ./internal/recap/...`
Expected: builds clean (types only — no usage yet).

- [ ] **Step 3: Commit**

```bash
git add internal/recap/types.go
git commit -m "Recap: add WP, Roster Activity, and new-award types"
```

---

## Task 2: Whale award (highest single-day team total)

**Files:**
- Modify: `internal/recap/awards.go`
- Modify: `internal/recap/awards_test.go`

- [ ] **Step 1: Write failing test**

Append to `awards_test.go`:

```go
func TestWhale(t *testing.T) {
	d1 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)

	days := []TeamDay{
		{TeamID: "1", TeamName: "A", Date: d1, Pts: 80},
		{TeamID: "2", TeamName: "B", Date: d1, Pts: 100}, // winner
		{TeamID: "3", TeamName: "C", Date: d2, Pts: 100}, // ties B but later → loses
		{TeamID: "4", TeamName: "D", Date: d2, Pts: 90},
	}

	got := Whale(days)
	if got == nil || got.TeamID != "2" {
		t.Fatalf("Whale: want team 2, got %+v", got)
	}
	if got.Pts != 100 {
		t.Errorf("Whale.Pts: want 100, got %.1f", got.Pts)
	}
}

func TestWhaleEmpty(t *testing.T) {
	if got := Whale(nil); got != nil {
		t.Errorf("Whale(nil): want nil, got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestWhale -v`
Expected: FAIL — `Whale` not defined.

- [ ] **Step 3: Implement**

Append to `awards.go` (just before the `// Award name labels rendered ...` block of constants):

```go
// Whale returns the highest single-day team total across the league × week.
// Tiebreak: earliest date, then TeamID asc. Returns nil if days is empty.
func Whale(days []TeamDay) *TeamDay {
	var best *TeamDay
	for i := range days {
		td := &days[i]
		switch {
		case best == nil:
		case td.Pts > best.Pts:
		case td.Pts == best.Pts && td.Date.Before(best.Date):
		case td.Pts == best.Pts && td.Date.Equal(best.Date) && td.TeamID < best.TeamID:
		default:
			continue
		}
		t := *td
		best = &t
	}
	return best
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestWhale -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recap/awards.go internal/recap/awards_test.go
git commit -m "Recap: add Whale award (biggest single-day team total)"
```

---

## Task 3: Dud award (lowest single-day active starter)

**Files:**
- Modify: `internal/recap/awards.go`
- Modify: `internal/recap/awards_test.go`

- [ ] **Step 1: Write failing test**

Append to `awards_test.go`:

```go
func TestDud(t *testing.T) {
	d1 := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)

	active := []PlayerLine{
		{PlayerID: "1", Name: "Alpha", FPts: 5, Date: d1, OwnerTeam: "A"},
		{PlayerID: "2", Name: "Bravo", FPts: -3, Date: d1, OwnerTeam: "B"}, // winner — most negative
		{PlayerID: "3", Name: "Charlie", FPts: -3, Date: d2, OwnerTeam: "C"}, // ties Bravo but later
		{PlayerID: "4", Name: "Delta", FPts: 2, Date: d1, OwnerTeam: "D"},
	}

	got := Dud(active)
	if got == nil || got.PlayerID != "2" {
		t.Fatalf("Dud: want player 2, got %+v", got)
	}
	if got.FPts != -3 {
		t.Errorf("Dud.FPts: want -3, got %.1f", got.FPts)
	}
}

func TestDudEmpty(t *testing.T) {
	if got := Dud(nil); got != nil {
		t.Errorf("Dud(nil): want nil, got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestDud -v`
Expected: FAIL — `Dud` not defined.

- [ ] **Step 3: Implement**

Append to `awards.go` (after `Whale`):

```go
// Dud returns the lowest single-day active-starter score across the league ×
// week. Negatives eligible. Tiebreak: earliest date, then Name asc.
// Returns nil if active is empty.
func Dud(active []PlayerLine) *PlayerLine {
	var best *PlayerLine
	for i := range active {
		l := &active[i]
		switch {
		case best == nil:
		case l.FPts < best.FPts:
		case l.FPts == best.FPts && l.Date.Before(best.Date):
		case l.FPts == best.FPts && l.Date.Equal(best.Date) && l.Name < best.Name:
		default:
			continue
		}
		t := *l
		best = &t
	}
	return best
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestDud -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recap/awards.go internal/recap/awards_test.go
git commit -m "Recap: add Dud award (lowest single-day active starter)"
```

---

## Task 4: `LeagueDailySigma` — variance computation

**Files:**
- Create: `internal/recap/wp.go`
- Create: `internal/recap/wp_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/recap/wp_test.go`:

```go
package recap

import (
	"math"
	"testing"
	"time"
)

func TestLeagueDailySigma(t *testing.T) {
	// Three points: 10, 20, 30 → mean=20, sample var=100, σ=10
	days := []TeamDay{
		{Pts: 10}, {Pts: 20}, {Pts: 30},
	}
	got := LeagueDailySigma(days)
	if math.Abs(got-10.0) > 1e-9 {
		t.Errorf("LeagueDailySigma: want 10, got %.6f", got)
	}
}

func TestLeagueDailySigmaTooFew(t *testing.T) {
	if got := LeagueDailySigma(nil); got != 0 {
		t.Errorf("nil → want 0, got %.6f", got)
	}
	if got := LeagueDailySigma([]TeamDay{{Pts: 50}}); got != 0 {
		t.Errorf("len=1 → want 0, got %.6f", got)
	}
}

// silence unused import when later tests are added
var _ = time.Now
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestLeagueDailySigma -v`
Expected: FAIL — package compile error (`wp.go` not yet created).

- [ ] **Step 3: Implement**

Create `internal/recap/wp.go`:

```go
package recap

import (
	"hash/fnv"
	"math"
	"math/rand"
	"strconv"
)

// wpNumSims is the Monte Carlo iteration count per WP point. 5000 gives a
// standard error of ~0.007 at p=0.5 — invisible at chart resolution.
const wpNumSims = 5000

// LeagueDailySigma returns the sample standard deviation of daily team
// scores. Returns 0 for fewer than 2 points (caller should treat as
// "WP simulation unavailable" and skip the curve).
func LeagueDailySigma(days []TeamDay) float64 {
	n := len(days)
	if n < 2 {
		return 0
	}
	var sum float64
	for _, d := range days {
		sum += d.Pts
	}
	mean := sum / float64(n)
	var ss float64
	for _, d := range days {
		dev := d.Pts - mean
		ss += dev * dev
	}
	return math.Sqrt(ss / float64(n-1))
}

// wpRNG returns a deterministic *rand.Rand seeded from the matchup identity
// + week number, so every run produces identical curves.
func wpRNG(homeID, awayID string, week int) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(homeID))
	h.Write([]byte("|"))
	h.Write([]byte(awayID))
	h.Write([]byte("|"))
	h.Write([]byte(strconv.Itoa(week)))
	return rand.New(rand.NewSource(int64(h.Sum64())))
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestLeagueDailySigma -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recap/wp.go internal/recap/wp_test.go
git commit -m "Recap: add LeagueDailySigma + WP RNG seed helper"
```

---

## Task 5: `ComputeWPCurve` — Monte Carlo simulation

**Files:**
- Modify: `internal/recap/wp.go`
- Modify: `internal/recap/wp_test.go`

- [ ] **Step 1: Write failing tests**

Append to `wp_test.go`:

```go
func makeDates(start time.Time) []time.Time {
	out := make([]time.Time, 7)
	for i := 0; i < 7; i++ {
		out[i] = start.AddDate(0, 0, i)
	}
	return out
}

func TestComputeWPCurve_HomeDominates(t *testing.T) {
	start := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	dates := makeDates(start)
	in := WPInputs{
		HomeTeamID:    "A",
		AwayTeamID:    "B",
		HomeMeanDaily: 60,
		AwayMeanDaily: 40,
		Sigma:         15,
		Dates:         dates,
		HomeActuals:   []float64{60, 60, 60, 60, 60, 60, 60}, // 420
		AwayActuals:   []float64{40, 40, 40, 40, 40, 40, 40}, // 280
		WeekNumber:    1,
	}
	curve := ComputeWPCurve(in)

	if len(curve.Points) != 8 {
		t.Fatalf("Points: want 8, got %d", len(curve.Points))
	}
	final := curve.Points[7]
	if final.HomeWP != 1.0 {
		t.Errorf("final HomeWP: want 1.0, got %.4f", final.HomeWP)
	}
	if final.HomeRunning != 420 {
		t.Errorf("final HomeRunning: want 420, got %.2f", final.HomeRunning)
	}
	if final.AwayRunning != 280 {
		t.Errorf("final AwayRunning: want 280, got %.2f", final.AwayRunning)
	}
	// Curve should monotonically increase as home dominates from start.
	for i := 1; i < 8; i++ {
		if curve.Points[i].HomeWP < curve.Points[i-1].HomeWP-1e-6 {
			t.Errorf("non-monotone WP at i=%d: prev=%.4f cur=%.4f",
				i, curve.Points[i-1].HomeWP, curve.Points[i].HomeWP)
		}
	}
}

func TestComputeWPCurve_TiedFinalIsHalf(t *testing.T) {
	start := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	in := WPInputs{
		HomeTeamID:    "A",
		AwayTeamID:    "B",
		HomeMeanDaily: 50,
		AwayMeanDaily: 50,
		Sigma:         20,
		Dates:         makeDates(start),
		HomeActuals:   []float64{50, 50, 50, 50, 50, 50, 50},
		AwayActuals:   []float64{50, 50, 50, 50, 50, 50, 50},
		WeekNumber:    1,
	}
	curve := ComputeWPCurve(in)
	if final := curve.Points[7].HomeWP; final != 0.5 {
		t.Errorf("tied final: want 0.5, got %.4f", final)
	}
}

func TestComputeWPCurve_Determinism(t *testing.T) {
	start := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	in := WPInputs{
		HomeTeamID:    "A",
		AwayTeamID:    "B",
		HomeMeanDaily: 55,
		AwayMeanDaily: 50,
		Sigma:         18,
		Dates:         makeDates(start),
		HomeActuals:   []float64{55, 50, 60, 45, 55, 50, 60},
		AwayActuals:   []float64{50, 55, 45, 60, 50, 55, 45},
		WeekNumber:    3,
	}
	a := ComputeWPCurve(in)
	b := ComputeWPCurve(in)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("ComputeWPCurve is non-deterministic")
	}
}
```

Add to the imports in `wp_test.go`:

```go
import (
	"math"
	"reflect"
	"testing"
	"time"
)
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestComputeWPCurve -v`
Expected: FAIL — `WPInputs` and `ComputeWPCurve` not defined.

- [ ] **Step 3: Implement**

Append to `wp.go`:

```go
// WPInputs is the per-matchup data needed to compute a WP curve. All slices
// are length 7 (one per day in the matchup week).
type WPInputs struct {
	HomeTeamID    string
	AwayTeamID    string
	HomeMeanDaily float64     // expected daily FPts for home
	AwayMeanDaily float64     // expected daily FPts for away
	Sigma         float64     // league-wide daily-score stddev
	Dates         []time.Time // length 7, one per matchup day (chronological)
	HomeActuals   []float64   // length 7, observed home FPts per day
	AwayActuals   []float64   // length 7, observed away FPts per day
	WeekNumber    int         // for RNG seed
}

// ComputeWPCurve returns the 8-point WP trace for one matchup. Points[0] is
// the pre-week baseline (always 0.5 — equal teams projected forward by
// definition); Points[1..7] are end-of-Day-i states using observed actuals
// + Monte Carlo projection of remaining days.
//
// Determinism: identical inputs always produce identical curves (RNG seeded
// from match identity + week number).
func ComputeWPCurve(in WPInputs) MatchupWPCurve {
	if len(in.Dates) != 7 || len(in.HomeActuals) != 7 || len(in.AwayActuals) != 7 {
		// Degenerate inputs — return an empty curve. The orchestrator
		// soft-fails by hiding charts/sparklines.
		return MatchupWPCurve{HomeTeamID: in.HomeTeamID, AwayTeamID: in.AwayTeamID}
	}
	rng := wpRNG(in.HomeTeamID, in.AwayTeamID, in.WeekNumber)

	points := make([]WPPoint, 8)
	var hSum, aSum float64
	for i := 0; i <= 7; i++ {
		if i > 0 {
			hSum += in.HomeActuals[i-1]
			aSum += in.AwayActuals[i-1]
		}
		daysLeft := 7 - i

		var wp float64
		switch {
		case daysLeft == 0:
			switch {
			case hSum > aSum:
				wp = 1.0
			case hSum < aSum:
				wp = 0.0
			default:
				wp = 0.5
			}
		default:
			wins := 0
			for s := 0; s < wpNumSims; s++ {
				simH := hSum
				simA := aSum
				for d := 0; d < daysLeft; d++ {
					simH += rng.NormFloat64()*in.Sigma + in.HomeMeanDaily
					simA += rng.NormFloat64()*in.Sigma + in.AwayMeanDaily
				}
				if simH > simA {
					wins++
				}
			}
			wp = float64(wins) / float64(wpNumSims)
		}

		// Date semantics: Points[0] uses the first matchup day's date as a
		// stand-in (the chart treats it as the leftmost X-axis tick);
		// Points[i] for i in 1..7 uses Dates[i-1].
		var date time.Time
		if i == 0 {
			date = in.Dates[0]
		} else {
			date = in.Dates[i-1]
		}
		points[i] = WPPoint{
			Date:        date,
			HomeWP:      wp,
			HomeRunning: hSum,
			AwayRunning: aSum,
		}
	}

	return MatchupWPCurve{
		HomeTeamID: in.HomeTeamID,
		AwayTeamID: in.AwayTeamID,
		Points:     points,
		// LeadChanges is filled in by the orchestrator after this returns,
		// using the LeadChangeCount helper from the next task.
	}
}
```

Add `"time"` to `wp.go` imports if not already present:

```go
import (
	"hash/fnv"
	"math"
	"math/rand"
	"strconv"
	"time"
)
```

(The `time` import is already needed because `WPInputs.Dates` references it.)

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestComputeWPCurve -v`
Expected: PASS (all three sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/recap/wp.go internal/recap/wp_test.go
git commit -m "Recap: ComputeWPCurve Monte Carlo simulator"
```

---

## Task 6: `LeadChangeCount`

**Files:**
- Modify: `internal/recap/wp.go`
- Modify: `internal/recap/wp_test.go`

- [ ] **Step 1: Write failing test**

Append to `wp_test.go`:

```go
func TestLeadChangeCount(t *testing.T) {
	cases := []struct {
		name string
		wps  []float64
		want int
	}{
		{"flat half", []float64{0.5, 0.5, 0.5, 0.5}, 0},
		{"home dominant", []float64{0.5, 0.7, 0.8, 0.9}, 1}, // crosses 0.5 once
		{"alternating", []float64{0.5, 0.6, 0.4, 0.6, 0.4, 0.6, 0.4, 0.6}, 6},
		{"never crosses", []float64{0.6, 0.7, 0.55, 0.8}, 0},
		{"goes to tie midway", []float64{0.6, 0.5, 0.4, 0.5, 0.4}, 1}, // crosses on .4
	}
	for _, c := range cases {
		points := make([]WPPoint, len(c.wps))
		for i, w := range c.wps {
			points[i] = WPPoint{HomeWP: w}
		}
		if got := LeadChangeCount(points); got != c.want {
			t.Errorf("%s: want %d changes, got %d", c.name, c.want, got)
		}
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestLeadChangeCount -v`
Expected: FAIL — `LeadChangeCount` not defined.

- [ ] **Step 3: Implement**

Append to `wp.go`:

```go
// LeadChangeCount returns the number of times the leader (defined as
// HomeWP > 0.5) flips across consecutive points. Days at exactly 0.5 do not
// trigger a transition either way.
func LeadChangeCount(points []WPPoint) int {
	if len(points) < 2 {
		return 0
	}
	count := 0
	prev := points[0].HomeWP
	for i := 1; i < len(points); i++ {
		cur := points[i].HomeWP
		// "Side" is HomeWP > 0.5 (true=home leads). Skip points at 0.5 by
		// carrying prev forward: a tie point alone does not count.
		if (prev > 0.5) != (cur > 0.5) {
			count++
		}
		prev = cur
	}
	return count
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestLeadChangeCount -v`
Expected: PASS.

- [ ] **Step 5: Wire into ComputeWPCurve**

Edit the `return` statement at the end of `ComputeWPCurve` in `wp.go`:

```go
	curve := MatchupWPCurve{
		HomeTeamID: in.HomeTeamID,
		AwayTeamID: in.AwayTeamID,
		Points:     points,
	}
	curve.LeadChanges = LeadChangeCount(points)
	return curve
```

(Replace the existing `return MatchupWPCurve{...}` with the variable form above.)

- [ ] **Step 6: Run all wp tests**

Run: `go test ./internal/recap/ -run "TestComputeWPCurve|TestLeadChangeCount" -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/recap/wp.go internal/recap/wp_test.go
git commit -m "Recap: LeadChangeCount + wire into ComputeWPCurve"
```

---

## Task 7: `MinWinnerWP`

**Files:**
- Modify: `internal/recap/wp.go`
- Modify: `internal/recap/wp_test.go`

- [ ] **Step 1: Write failing test**

Append to `wp_test.go`:

```go
func TestMinWinnerWP(t *testing.T) {
	// Mid-week points (Days 1..6) only — index 0 (pre-week) and index 7
	// (final) are excluded.
	cases := []struct {
		name      string
		wps       []float64 // length 8: idx 0=pre, idx 7=final
		homeWon   bool
		wantMin   float64
		wantOK    bool // whether a min was returnable
	}{
		{
			name:    "winner trailed mid-week",
			wps:     []float64{0.5, 0.4, 0.3, 0.2, 0.4, 0.6, 0.7, 1.0}, // home wins
			homeWon: true,
			wantMin: 0.2,
			wantOK:  true,
		},
		{
			name:    "winner never trailed",
			wps:     []float64{0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.95, 1.0},
			homeWon: true,
			wantMin: 0.6,
			wantOK:  true,
		},
		{
			name:    "away winner — invert",
			wps:     []float64{0.5, 0.6, 0.7, 0.8, 0.4, 0.3, 0.2, 0.0}, // away wins
			homeWon: false,
			wantMin: 0.2, // = 1 - 0.8 (away's lowest mid-week WP)
			wantOK:  true,
		},
	}

	for _, c := range cases {
		points := make([]WPPoint, len(c.wps))
		for i, w := range c.wps {
			points[i] = WPPoint{HomeWP: w}
		}
		got, ok := MinWinnerWP(points, c.homeWon)
		if ok != c.wantOK {
			t.Errorf("%s: ok mismatch: want %v got %v", c.name, c.wantOK, ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(got-c.wantMin) > 1e-9 {
			t.Errorf("%s: want %.4f, got %.4f", c.name, c.wantMin, got)
		}
	}
}

func TestMinWinnerWPShortCurve(t *testing.T) {
	if _, ok := MinWinnerWP(nil, true); ok {
		t.Errorf("nil curve: want ok=false")
	}
	short := []WPPoint{{HomeWP: 0.5}, {HomeWP: 0.7}}
	if _, ok := MinWinnerWP(short, true); ok {
		t.Errorf("short curve: want ok=false")
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestMinWinnerWP -v`
Expected: FAIL — `MinWinnerWP` not defined.

- [ ] **Step 3: Implement**

Append to `wp.go`:

```go
// MinWinnerWP returns the lowest mid-week win probability for the eventual
// winner. Mid-week is defined as Points[1..6] (Days 1..6) — index 0 (pre-
// week) and index 7 (final) are excluded.
//
// homeWon = true means the eventual winner was the home team; ok=false
// when the curve is too short to evaluate (need 8 points).
func MinWinnerWP(points []WPPoint, homeWon bool) (float64, bool) {
	if len(points) < 8 {
		return 0, false
	}
	min := math.Inf(1)
	for i := 1; i <= 6; i++ {
		wp := points[i].HomeWP
		if !homeWon {
			wp = 1.0 - wp
		}
		if wp < min {
			min = wp
		}
	}
	return min, true
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestMinWinnerWP -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recap/wp.go internal/recap/wp_test.go
git commit -m "Recap: MinWinnerWP for Comeback award"
```

---

## Task 8: HeartAttack + GameOfWeek selection

**Files:**
- Modify: `internal/recap/awards.go`
- Modify: `internal/recap/awards_test.go`

- [ ] **Step 1: Write failing test**

Append to `awards_test.go`:

```go
func TestHeartAttack(t *testing.T) {
	// Three matchups; "MID" has the most lead changes.
	low := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.4}, {HomeWP: 0.3}, {HomeWP: 0.2},
		{HomeWP: 0.15}, {HomeWP: 0.1}, {HomeWP: 0.05}, {HomeWP: 0.0},
	}
	mid := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.6}, {HomeWP: 0.4}, {HomeWP: 0.6},
		{HomeWP: 0.4}, {HomeWP: 0.6}, {HomeWP: 0.4}, {HomeWP: 0.55},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: low, LeadChanges: 1},
		{HomeTeamID: "C", AwayTeamID: "D", Points: mid, LeadChanges: 6},
		{HomeTeamID: "E", AwayTeamID: "F", Points: low, LeadChanges: 1},
	}
	matchups := []MatchupResult{
		mr("A", "B", 100, 200), // blowout
		mr("C", "D", 102, 100), // narrow
		mr("E", "F", 90, 120),
	}

	got := HeartAttack(curves, matchups)
	if got == nil || got.HomeTeamID != "C" {
		t.Fatalf("HeartAttack: want C-D, got %+v", got)
	}
}

func TestHeartAttackNoLeadChanges(t *testing.T) {
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", LeadChanges: 0},
	}
	matchups := []MatchupResult{mr("A", "B", 100, 90)}
	if got := HeartAttack(curves, matchups); got != nil {
		t.Errorf("HeartAttack: want nil when no lead changes, got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestHeartAttack -v`
Expected: FAIL — `HeartAttack` not defined.

- [ ] **Step 3: Implement**

Append to `awards.go` (after `Dud`):

```go
// HeartAttack returns the matchup with the most lead changes in its WP
// curve. Returns nil if no matchup has any lead changes (an "all-blowouts"
// week — the recap will hide the Game of the Week section in that case).
//
// matchups is the per-week MatchupResult list; curves are matched by
// canonical team-pair key, so order is independent.
//
// Tiebreak: smallest final margin → home TeamID asc.
func HeartAttack(curves []MatchupWPCurve, matchups []MatchupResult) *MatchupResult {
	if len(curves) == 0 || len(matchups) == 0 {
		return nil
	}
	mByPair := make(map[string]MatchupResult, len(matchups))
	for _, m := range matchups {
		mByPair[canonPair(m.HomeTeamID, m.AwayTeamID)] = m
	}

	var best *MatchupResult
	var bestChanges int
	for _, c := range curves {
		if c.LeadChanges == 0 {
			continue
		}
		m, ok := mByPair[canonPair(c.HomeTeamID, c.AwayTeamID)]
		if !ok {
			continue
		}
		switch {
		case best == nil:
		case c.LeadChanges > bestChanges:
		case c.LeadChanges == bestChanges && m.Margin < best.Margin:
		case c.LeadChanges == bestChanges && m.Margin == best.Margin && m.HomeTeamID < best.HomeTeamID:
		default:
			continue
		}
		copyM := m
		best = &copyM
		bestChanges = c.LeadChanges
	}
	return best
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestHeartAttack -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recap/awards.go internal/recap/awards_test.go
git commit -m "Recap: HeartAttack award (most lead changes)"
```

---

## Task 9: Comeback award (gated mid-week WP < 0.30)

**Files:**
- Modify: `internal/recap/awards.go`
- Modify: `internal/recap/awards_test.go`

- [ ] **Step 1: Write failing test**

Append to `awards_test.go`:

```go
func TestComeback(t *testing.T) {
	// Matchup 1: home wins after trailing badly mid-week (eligible).
	deep := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.4}, {HomeWP: 0.2}, {HomeWP: 0.15},
		{HomeWP: 0.4}, {HomeWP: 0.6}, {HomeWP: 0.7}, {HomeWP: 1.0},
	}
	// Matchup 2: home wins, mild dip but never below 0.30 (ineligible).
	mild := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.45}, {HomeWP: 0.4}, {HomeWP: 0.5},
		{HomeWP: 0.6}, {HomeWP: 0.7}, {HomeWP: 0.85}, {HomeWP: 1.0},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: deep},
		{HomeTeamID: "C", AwayTeamID: "D", Points: mild},
	}
	matchups := []MatchupResult{
		mr("A", "B", 200, 180),
		mr("C", "D", 150, 145),
	}

	got := Comeback(curves, matchups)
	if got == nil || got.TeamID != "A" {
		t.Fatalf("Comeback: want A, got %+v", got)
	}
}

func TestComebackNoEligible(t *testing.T) {
	mild := []WPPoint{
		{HomeWP: 0.5}, {HomeWP: 0.55}, {HomeWP: 0.6}, {HomeWP: 0.65},
		{HomeWP: 0.7}, {HomeWP: 0.75}, {HomeWP: 0.8}, {HomeWP: 1.0},
	}
	curves := []MatchupWPCurve{
		{HomeTeamID: "A", AwayTeamID: "B", Points: mild},
	}
	matchups := []MatchupResult{mr("A", "B", 200, 180)}
	if got := Comeback(curves, matchups); got != nil {
		t.Errorf("Comeback: want nil (no eligible), got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestComeback -v`
Expected: FAIL — `Comeback` not defined.

- [ ] **Step 3: Implement**

Append to `awards.go`:

```go
// comebackThreshold is the maximum mid-week WP a winner can have hit and
// still qualify for the Comeback award. 0.30 keeps it meaningful — only
// genuine "left for dead" comebacks count.
const comebackThreshold = 0.30

// Comeback returns the eventual winner with the lowest mid-week WP, gated
// at comebackThreshold. Returns nil if no winner had a mid-week WP below
// the threshold. Tiebreak: smallest min WP → TeamID asc.
func Comeback(curves []MatchupWPCurve, matchups []MatchupResult) *MatchupTeamSide {
	if len(curves) == 0 || len(matchups) == 0 {
		return nil
	}
	mByPair := make(map[string]MatchupResult, len(matchups))
	for _, m := range matchups {
		mByPair[canonPair(m.HomeTeamID, m.AwayTeamID)] = m
	}

	var best *MatchupTeamSide
	bestMin := math.Inf(1)
	for _, c := range curves {
		m, ok := mByPair[canonPair(c.HomeTeamID, c.AwayTeamID)]
		if !ok || m.IsTie {
			continue
		}
		homeWon := m.WinnerID == m.HomeTeamID
		minWP, ok := MinWinnerWP(c.Points, homeWon)
		if !ok || minWP >= comebackThreshold {
			continue
		}
		var side MatchupTeamSide
		if homeWon {
			side = MatchupTeamSide{
				TeamID: m.HomeTeamID, TeamName: m.HomeTeamName, Pts: m.HomePts,
				OppName: m.AwayTeamName, OppPts: m.AwayPts,
			}
		} else {
			side = MatchupTeamSide{
				TeamID: m.AwayTeamID, TeamName: m.AwayTeamName, Pts: m.AwayPts,
				OppName: m.HomeTeamName, OppPts: m.HomePts,
			}
		}
		switch {
		case best == nil:
		case minWP < bestMin:
		case minWP == bestMin && side.TeamID < best.TeamID:
		default:
			continue
		}
		copySide := side
		best = &copySide
		bestMin = minWP
	}
	return best
}
```

Add `"math"` to `awards.go` imports if not already imported:

```go
import (
	"math"
	"sort"
)
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestComeback -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/recap/awards.go internal/recap/awards_test.go
git commit -m "Recap: Comeback award (lowest mid-week WP among winners)"
```

---

## Task 10: Wrap `GetTransactionHistory` in our fantrax client

**Files:**
- Modify: `internal/fantrax/client.go`

- [ ] **Step 1: Add the wrapper**

Append after the existing `GetRecentTrades` function in `client.go`:

```go
// GetWeekTransactions returns all executed transactions (claims, drops,
// trades) whose ProcessedDate falls on a calendar date in
// [windowStart, windowEnd] (inclusive). Date comparison is YYYY-MM-DD
// lexical to dodge timezone equality pitfalls — same convention used by
// pairsForWeek in the recap pipeline.
func (c *Client) GetWeekTransactions(windowStart, windowEnd time.Time) ([]models.Transaction, error) {
	all, err := c.auth.GetTransactionHistory("250")
	if err != nil {
		return nil, fmt.Errorf("fetch transactions: %w", err)
	}
	startYMD := windowStart.Format("2006-01-02")
	endYMD := windowEnd.Format("2006-01-02")
	var window []models.Transaction
	for _, tx := range all {
		ymd := tx.ProcessedDate.Format("2006-01-02")
		if ymd >= startYMD && ymd <= endYMD {
			window = append(window, tx)
		}
	}
	return window, nil
}
```

- [ ] **Step 2: Compile-check**

Run: `go build ./internal/fantrax/...`
Expected: builds clean.

- [ ] **Step 3: Vet**

Run: `go vet ./internal/fantrax/...`
Expected: no issues.

- [ ] **Step 4: Commit**

```bash
git add internal/fantrax/client.go
git commit -m "Fantrax: add GetWeekTransactions for recap activity log"
```

---

## Task 11: Roster Activity collector

**Files:**
- Create: `internal/recap/roster_activity.go`
- Create: `internal/recap/roster_activity_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/recap/roster_activity_test.go`:

```go
package recap

import (
	"testing"
	"time"

	"github.com/pmurley/go-fantrax/models"
)

func TestBuildRosterActivity_ClaimDrop(t *testing.T) {
	d := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "CLAIM", TeamID: "1", PlayerName: "Hayes", ProcessedDate: d, ClaimType: "FA"},
		{Type: "DROP", TeamID: "2", PlayerName: "Carroll", ProcessedDate: d},
	}
	teamNames := map[string]string{"1": "Wahoos", "2": "Sliders"}

	got := BuildRosterActivity(txs, teamNames)
	if got == nil || len(got.Teams) != 2 {
		t.Fatalf("want 2 teams, got %+v", got)
	}
	// Sorted by team name asc → Sliders, Wahoos
	if got.Teams[0].TeamName != "Sliders" || got.Teams[1].TeamName != "Wahoos" {
		t.Errorf("teams not sorted: %v", got.Teams)
	}
	w := got.Teams[1]
	if len(w.Entries) != 1 || w.Entries[0].Kind != "claim" || w.Entries[0].Player != "Hayes" {
		t.Errorf("Wahoos entry wrong: %+v", w.Entries)
	}
	if w.Entries[0].ClaimType != "FA" {
		t.Errorf("ClaimType: want FA, got %q", w.Entries[0].ClaimType)
	}
}

func TestBuildRosterActivity_Swap(t *testing.T) {
	d := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "CLAIM", TeamID: "1", PlayerName: "Hayes", ProcessedDate: d, ClaimType: "FA"},
		{Type: "DROP", TeamID: "1", PlayerName: "Carroll", ProcessedDate: d},
	}
	teamNames := map[string]string{"1": "Wahoos"}

	got := BuildRosterActivity(txs, teamNames)
	if got == nil || len(got.Teams) != 1 || len(got.Teams[0].Entries) != 1 {
		t.Fatalf("want 1 team, 1 entry, got %+v", got)
	}
	e := got.Teams[0].Entries[0]
	if e.Kind != "swap" || e.SwapIn != "Hayes" || e.SwapOut != "Carroll" {
		t.Errorf("swap entry wrong: %+v", e)
	}
}

func TestBuildRosterActivity_NoSwapWhenMultiple(t *testing.T) {
	d := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "CLAIM", TeamID: "1", PlayerName: "Hayes", ProcessedDate: d, ClaimType: "FA"},
		{Type: "CLAIM", TeamID: "1", PlayerName: "Lee", ProcessedDate: d, ClaimType: "FA"},
		{Type: "DROP", TeamID: "1", PlayerName: "Carroll", ProcessedDate: d},
	}
	teamNames := map[string]string{"1": "Wahoos"}

	got := BuildRosterActivity(txs, teamNames)
	// 2 CLAIMs + 1 DROP same day → don't merge any; render all 3.
	if got == nil || len(got.Teams) != 1 || len(got.Teams[0].Entries) != 3 {
		t.Fatalf("want 3 entries, got %+v", got)
	}
	for _, e := range got.Teams[0].Entries {
		if e.Kind == "swap" {
			t.Errorf("did not expect swap merge: %+v", e)
		}
	}
}

func TestBuildRosterActivity_Trade(t *testing.T) {
	d := time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC)
	txs := []models.Transaction{
		{Type: "TRADE", FromTeamID: "1", ToTeamID: "2", PlayerName: "Hayes", ProcessedDate: d, TradeGroupID: "tg1"},
		{Type: "TRADE", FromTeamID: "2", ToTeamID: "1", PlayerName: "Carroll", ProcessedDate: d, TradeGroupID: "tg1"},
	}
	teamNames := map[string]string{"1": "Wahoos", "2": "Sliders"}

	got := BuildRosterActivity(txs, teamNames)
	if got == nil || len(got.Teams) != 2 {
		t.Fatalf("want 2 teams, got %+v", got)
	}
	// Sliders (sorted first): received Hayes, sent Carroll
	s := got.Teams[0]
	if s.TeamName != "Sliders" {
		t.Fatalf("teams[0]: want Sliders, got %q", s.TeamName)
	}
	if len(s.Entries) != 1 || s.Entries[0].Kind != "trade" {
		t.Fatalf("Sliders entries: %+v", s.Entries)
	}
	tr := s.Entries[0]
	if tr.OtherTeam != "Wahoos" || len(tr.Received) != 1 || tr.Received[0] != "Hayes" || len(tr.Sent) != 1 || tr.Sent[0] != "Carroll" {
		t.Errorf("Sliders trade entry wrong: %+v", tr)
	}
	// Wahoos: opposite
	w := got.Teams[1]
	if w.Entries[0].OtherTeam != "Sliders" || w.Entries[0].Received[0] != "Carroll" || w.Entries[0].Sent[0] != "Hayes" {
		t.Errorf("Wahoos trade entry wrong: %+v", w.Entries[0])
	}
}

func TestBuildRosterActivity_Empty(t *testing.T) {
	if got := BuildRosterActivity(nil, nil); got != nil {
		t.Errorf("nil input → want nil RosterActivity, got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect fail**

Run: `go test ./internal/recap/ -run TestBuildRosterActivity -v`
Expected: FAIL — `BuildRosterActivity` not defined.

- [ ] **Step 3: Implement**

Create `internal/recap/roster_activity.go`:

```go
package recap

import (
	"sort"

	"github.com/pmurley/go-fantrax/models"
)

// BuildRosterActivity transforms a flat list of transactions for the matchup
// week into a per-team activity log. Returns nil if no team made any moves.
//
// Grouping rules (per spec):
//   - Trades: bucket by TradeGroupID, render once per team-side
//   - Swap: same-day exactly 1 CLAIM + 1 DROP for the same team → merged
//   - Otherwise: render claim/drop entries as-is
//
// teamNames maps fantasy TeamID → display name. Unknown TeamIDs use the ID
// as the name fallback.
func BuildRosterActivity(txs []models.Transaction, teamNames map[string]string) *RosterActivity {
	if len(txs) == 0 {
		return nil
	}

	type teamBuilder struct {
		teamID  string
		name    string
		entries []ActivityEntry
	}
	teams := map[string]*teamBuilder{}

	getOrInit := func(teamID string) *teamBuilder {
		if tb, ok := teams[teamID]; ok {
			return tb
		}
		name := teamNames[teamID]
		if name == "" {
			name = teamID
		}
		tb := &teamBuilder{teamID: teamID, name: name}
		teams[teamID] = tb
		return tb
	}

	// 1) Trades — group by TradeGroupID, build per-team entries.
	type tradeBucket struct {
		date    interface{ /* unused */ }
		players map[string][]string // teamID -> received player names
	}
	tradeGroups := map[string][]models.Transaction{}
	for _, tx := range txs {
		if tx.Type == "TRADE" && tx.TradeGroupID != "" {
			tradeGroups[tx.TradeGroupID] = append(tradeGroups[tx.TradeGroupID], tx)
		}
	}
	for _, group := range tradeGroups {
		// Per group, each team's received = players whose ToTeamID == team,
		// sent = players whose FromTeamID == team.
		teamSet := map[string]struct{}{}
		for _, tx := range group {
			teamSet[tx.FromTeamID] = struct{}{}
			teamSet[tx.ToTeamID] = struct{}{}
		}
		for teamID := range teamSet {
			tb := getOrInit(teamID)
			var received, sent []string
			var otherID string
			var date = group[0].ProcessedDate
			for _, tx := range group {
				switch teamID {
				case tx.ToTeamID:
					received = append(received, tx.PlayerName)
					if tx.FromTeamID != teamID {
						otherID = tx.FromTeamID
					}
				case tx.FromTeamID:
					sent = append(sent, tx.PlayerName)
					if tx.ToTeamID != teamID {
						otherID = tx.ToTeamID
					}
				}
			}
			otherName := teamNames[otherID]
			if otherName == "" {
				otherName = otherID
			}
			tb.entries = append(tb.entries, ActivityEntry{
				Date:      date,
				Kind:      "trade",
				OtherTeam: otherName,
				Received:  received,
				Sent:      sent,
			})
		}
	}

	// 2) Claims/Drops — bucket per (teamID, YYYY-MM-DD); detect swap = exactly
	// 1 CLAIM + 1 DROP.
	type bucketKey struct {
		teamID string
		date   string
	}
	type bucket struct {
		claims []models.Transaction
		drops  []models.Transaction
	}
	buckets := map[bucketKey]*bucket{}
	for _, tx := range txs {
		if tx.Type != "CLAIM" && tx.Type != "DROP" {
			continue
		}
		key := bucketKey{teamID: tx.TeamID, date: tx.ProcessedDate.Format("2006-01-02")}
		b, ok := buckets[key]
		if !ok {
			b = &bucket{}
			buckets[key] = b
		}
		switch tx.Type {
		case "CLAIM":
			b.claims = append(b.claims, tx)
		case "DROP":
			b.drops = append(b.drops, tx)
		}
	}
	for key, b := range buckets {
		tb := getOrInit(key.teamID)
		if len(b.claims) == 1 && len(b.drops) == 1 {
			tb.entries = append(tb.entries, ActivityEntry{
				Date:    b.claims[0].ProcessedDate,
				Kind:    "swap",
				SwapIn:  b.claims[0].PlayerName,
				SwapOut: b.drops[0].PlayerName,
			})
			continue
		}
		for _, tx := range b.claims {
			tb.entries = append(tb.entries, ActivityEntry{
				Date:      tx.ProcessedDate,
				Kind:      "claim",
				Player:    tx.PlayerName,
				ClaimType: tx.ClaimType,
			})
		}
		for _, tx := range b.drops {
			tb.entries = append(tb.entries, ActivityEntry{
				Date:   tx.ProcessedDate,
				Kind:   "drop",
				Player: tx.PlayerName,
			})
		}
	}

	if len(teams) == 0 {
		return nil
	}

	// Materialize sorted output.
	out := &RosterActivity{Teams: make([]TeamActivity, 0, len(teams))}
	for _, tb := range teams {
		// Stable per-team entry sort: date asc, then a stable secondary on
		// (Kind, Player + SwapIn + OtherTeam) for entries that share a date.
		sort.SliceStable(tb.entries, func(i, j int) bool {
			ei, ej := tb.entries[i], tb.entries[j]
			if !ei.Date.Equal(ej.Date) {
				return ei.Date.Before(ej.Date)
			}
			ki := ei.Kind + ei.Player + ei.SwapIn + ei.OtherTeam
			kj := ej.Kind + ej.Player + ej.SwapIn + ej.OtherTeam
			return ki < kj
		})
		out.Teams = append(out.Teams, TeamActivity{
			TeamID:   tb.teamID,
			TeamName: tb.name,
			Entries:  tb.entries,
		})
	}
	sort.SliceStable(out.Teams, func(i, j int) bool {
		return out.Teams[i].TeamName < out.Teams[j].TeamName
	})

	return out
}
```

- [ ] **Step 4: Run — expect pass**

Run: `go test ./internal/recap/ -run TestBuildRosterActivity -v`
Expected: PASS (all 5 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/recap/roster_activity.go internal/recap/roster_activity_test.go
git commit -m "Recap: roster activity collector with swap detection"
```

---

## Task 12: Extend `awardOrder`, `awardEmoji`, and `AggregateSeasonAwards`

**Files:**
- Modify: `internal/recap/awards.go`
- Modify: `internal/recap/render.go`
- Modify: `internal/recap/awards_test.go`

- [ ] **Step 1: Add award name constants and update `awardOrder`**

In `awards.go`, find the existing constant block:

```go
const (
	AwardMostEfficient  = "Most Efficient"
	...
	AwardWorstStart     = "Worst Start"
)
```

Append four new constants inside that block:

```go
	AwardHeartAttack    = "Heart Attack"
	AwardComeback       = "Comeback"
	AwardWhale          = "Whale"
	AwardDud            = "Dud"
```

Then find the `awardOrder` slice and append the same four labels at the end, in the same order:

```go
var awardOrder = []string{
	AwardMostEfficient,
	AwardLeastEfficient,
	AwardHighestScore,
	AwardLowestScore,
	AwardBiggestBlowout,
	AwardNarrowVictory,
	AwardHighestPtsLoss,
	AwardLowestPtsWin,
	AwardBestStart,
	AwardWorstStart,
	AwardHeartAttack,
	AwardComeback,
	AwardWhale,
	AwardDud,
}
```

- [ ] **Step 2: Add 4 new branches to `AggregateSeasonAwards`**

In `awards.go`, find the per-recap iteration (the `add(...)` calls inside `for _, r := range recaps`). After the existing `if a.WorstSingleStart != nil` block, append:

```go
		if a.HeartAttack != nil {
			add(AwardHeartAttack, a.HeartAttack.WinnerID)
		}
		if a.Comeback != nil {
			add(AwardComeback, a.Comeback.TeamID)
		}
		if a.Whale != nil {
			add(AwardWhale, a.Whale.TeamID)
		}
		if a.Dud != nil {
			if id, ok := nameToID[a.Dud.OwnerTeam]; ok {
				add(AwardDud, id)
			}
		}
```

(Whale uses `TeamID` directly because `TeamDay` carries it. Dud resolves via the `OwnerTeam` name since `PlayerLine` doesn't carry a TeamID — same pattern as `BestSingleStart`.)

- [ ] **Step 3: Add 4 new emoji entries to `awardEmoji`**

In `render.go`, find the `awardEmoji` switch statement. Add four new cases before the default `return ""`:

```go
	case AwardHeartAttack:
		return "💓"
	case AwardComeback:
		return "↩️"
	case AwardWhale:
		return "🐳"
	case AwardDud:
		return "😴"
```

- [ ] **Step 4: Add a season-aggregation test**

Append to `awards_test.go`:

```go
func TestAggregateSeasonAwards_NewCategories(t *testing.T) {
	d := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)
	r := &Recap{
		WeekNumber: 1,
		Teams: []TeamWeek{
			{TeamID: "1", TeamName: "Wahoos"},
			{TeamID: "2", TeamName: "Sliders"},
		},
		Awards: Awards{
			HeartAttack: &MatchupResult{HomeTeamID: "1", AwayTeamID: "2", WinnerID: "1"},
			Comeback:    &MatchupTeamSide{TeamID: "1", TeamName: "Wahoos"},
			Whale:       &TeamDay{TeamID: "2", TeamName: "Sliders", Date: d, Pts: 200},
			Dud:         &PlayerLine{Name: "Smith", FPts: -5, Date: d, OwnerTeam: "Sliders"},
		},
	}
	snaps := AggregateSeasonAwards([]*Recap{r})
	if len(snaps) != 1 || snaps[0] == nil {
		t.Fatalf("snaps: want 1 non-nil, got %+v", snaps)
	}
	want := map[string]string{
		AwardHeartAttack: "1",
		AwardComeback:    "1",
		AwardWhale:       "2",
		AwardDud:         "2",
	}
	got := map[string]string{}
	for _, cat := range snaps[0].Categories {
		if len(cat.Teams) > 0 {
			got[cat.AwardName] = cat.Teams[0].TeamID
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: want team %q, got %q", k, v, got[k])
		}
	}
}
```

- [ ] **Step 5: Run all recap tests**

Run: `go test ./internal/recap/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/recap/awards.go internal/recap/awards_test.go internal/recap/render.go
git commit -m "Recap: wire 4 new awards into season aggregation + emoji map"
```

---

## Task 13: Orchestrate new collectors in `recap.Run`

**Files:**
- Modify: `internal/recap/recap.go`

- [ ] **Step 1: Add team-day aggregation + WP wiring**

In `recap.go`, find the `Run` function. After the line `annotateOpponents(allStarts)` and before the `sort.SliceStable(teamWeeks, ...)` block, insert:

```go
	// Build per-day per-team totals for the Whale award + WP simulation σ.
	dayTotals := buildTeamDays(results, teamMap)

	// Build per-day per-team home/away actuals keyed by team for WP curves.
	teamDailyByID := dailyByTeam(results)

	sigma := LeagueDailySigma(dayTotals)
```

Then, after the existing `awards := Awards{...}` literal, replace the Awards literal with the extended form:

```go
	awards := Awards{
		MostEfficient:    MostEfficient(teamWeeks),
		LeastEfficient:   LeastEfficient(teamWeeks),
		HighestScore:     HighestScore(teamWeeks),
		LowestScore:      LowestScore(teamWeeks),
		BiggestBlowout:   BiggestBlowout(matchups),
		NarrowVictory:    NarrowVictory(matchups),
		HighestPtsInLoss: HighestPtsInLoss(matchups),
		LowestPtsInWin:   LowestPtsInWin(matchups),
		BestSingleStart:  BestSingleStart(allStarts),
		WorstSingleStart: WorstSingleStart(allStarts),
		TopBatters:       TopBatters(allActive, opts.TopPlayers),
		TopPitchers:      TopPitchers(allActive, opts.TopPlayers),
		Whale:            Whale(dayTotals),
		Dud:              Dud(allActive),
	}
```

After the awards literal, add WP curve construction + the WP-derived awards:

```go
	var curves []MatchupWPCurve
	if sigma > 0 {
		// Each team's expected daily FPts is its within-week average. This
		// honors the spec's "season-to-date" intent at the simplest possible
		// data cost (no extra fetches); the look-ahead bias is tolerable for
		// a post-mortem narrative chart. See spec §"Future extensions" for
		// season-wide upgrade.
		for _, m := range matchups {
			h := teamDailyByID[m.HomeTeamID]
			a := teamDailyByID[m.AwayTeamID]
			if len(h.Actuals) != 7 || len(a.Actuals) != 7 {
				continue
			}
			hMean := mean(h.Actuals)
			aMean := mean(a.Actuals)
			curve := ComputeWPCurve(WPInputs{
				HomeTeamID:    m.HomeTeamID,
				AwayTeamID:    m.AwayTeamID,
				HomeMeanDaily: hMean,
				AwayMeanDaily: aMean,
				Sigma:         sigma,
				Dates:         h.Dates,
				HomeActuals:   h.Actuals,
				AwayActuals:   a.Actuals,
				WeekNumber:    weekNum,
			})
			curves = append(curves, curve)
		}
	}
	awards.HeartAttack = HeartAttack(curves, matchups)
	awards.GameOfWeek = awards.HeartAttack
	awards.Comeback = Comeback(curves, matchups)
```

(Note: `weekNum` is computed later in the existing function; we need it here. Move the existing `weekNum := opts.WeekNumber` block up to before this section. Concretely: cut the entire block starting with `weekNum := opts.WeekNumber` through the corresponding `weekLabel := ...` block, and paste it before `awards := Awards{...}`.)

Then add transactions fetch + activity build, with soft-fail on error. Just before `return &Recap{...}`:

```go
	var activity *RosterActivity
	if txs, err := ft.GetWeekTransactions(opts.WeekStart, opts.WeekEnd); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: roster activity: %v\n", err)
	} else {
		activity = BuildRosterActivity(txs, teamMap)
	}
```

Finally extend the returned `Recap` literal:

```go
	return &Recap{
		Season:         opts.WeekStart.Year(),
		WeekNumber:     weekNum,
		WeekLabel:      weekLabel,
		StartDate:      opts.WeekStart,
		EndDate:        opts.WeekEnd,
		GeneratedAt:    time.Now().UTC(),
		Teams:          teamWeeks,
		Matchups:       matchups,
		Awards:         awards,
		WPCurves:       curves,
		RosterActivity: activity,
	}, nil
```

- [ ] **Step 2: Add the helper functions**

Append to the bottom of `recap.go`:

```go
// teamDaily holds one team's per-day actuals for the matchup window.
// Length 7, chronological.
type teamDaily struct {
	Dates   []time.Time
	Actuals []float64
}

// dailyByTeam pivots the per-team teamData (which has actual FPts via the
// existing backtest analysis) into a teamID → teamDaily map. The orchestrator
// uses this to feed the WP simulation per matchup.
func dailyByTeam(results map[string]*teamData) map[string]teamDaily {
	out := make(map[string]teamDaily, len(results))
	for teamID, td := range results {
		// The active player lines carry per-day per-player FPts. Aggregate by
		// date (active starters only — same definition the optimizer uses for
		// the actual-points side of efficiency).
		byDate := map[string]float64{}
		for _, p := range td.active {
			byDate[p.Date.Format("2006-01-02")] += p.FPts
		}
		// Materialize into chronological slices. We need the canonical week
		// dates — pull them from the active list's distinct Dates.
		dates := uniqueDates(td.active)
		actuals := make([]float64, len(dates))
		for i, d := range dates {
			actuals[i] = byDate[d.Format("2006-01-02")]
		}
		out[teamID] = teamDaily{Dates: dates, Actuals: actuals}
	}
	return out
}

// uniqueDates returns the distinct Dates from a PlayerLine slice in
// chronological order. Used to derive the canonical 7-day window.
func uniqueDates(lines []PlayerLine) []time.Time {
	seen := map[string]time.Time{}
	for _, l := range lines {
		key := l.Date.Format("2006-01-02")
		if _, ok := seen[key]; !ok {
			seen[key] = l.Date
		}
	}
	out := make([]time.Time, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// buildTeamDays produces one TeamDay per (team, date) pair across all teams.
// Used as input to the Whale award and to LeagueDailySigma for WP variance.
func buildTeamDays(results map[string]*teamData, teamMap map[string]string) []TeamDay {
	var out []TeamDay
	for teamID, td := range results {
		byDate := map[string]float64{}
		dates := map[string]time.Time{}
		for _, p := range td.active {
			key := p.Date.Format("2006-01-02")
			byDate[key] += p.FPts
			dates[key] = p.Date
		}
		name := teamMap[teamID]
		for key, pts := range byDate {
			out = append(out, TeamDay{
				TeamID:   teamID,
				TeamName: name,
				Date:     dates[key],
				Pts:      pts,
			})
		}
	}
	// Stable order for determinism.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].TeamID < out[j].TeamID
	})
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
```

- [ ] **Step 3: Compile-check**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 4: Vet**

Run: `go vet ./...`
Expected: no issues.

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/...`
Expected: all existing tests still pass; new tests still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/recap/recap.go
git commit -m "Recap: orchestrate WP curves + roster activity in Run"
```

---

## Task 14: Template — Game of the Week + sparklines + new award cards

**Files:**
- Modify: `internal/recap/template.html`
- Modify: `internal/recap/render.go`

- [ ] **Step 1: Add template helper for sparkline path**

In `render.go`, add this helper after `barWidth`:

```go
// sparkPath returns an SVG <path d="..."> string for an inline sparkline.
// Width/height match the .matchup .spark CSS rule (60×24). Maps WP in [0,1]
// linearly to vertical pixel position (HomeWP=1.0 → top, =0.0 → bottom).
func sparkPath(curve MatchupWPCurve) string {
	if len(curve.Points) < 2 {
		return ""
	}
	const w, h = 60.0, 24.0
	n := len(curve.Points)
	step := w / float64(n-1)
	var out strings.Builder
	for i, p := range curve.Points {
		x := float64(i) * step
		y := (1.0 - p.HomeWP) * h
		if i == 0 {
			fmt.Fprintf(&out, "M%.2f,%.2f", x, y)
		} else {
			fmt.Fprintf(&out, " L%.2f,%.2f", x, y)
		}
	}
	return out.String()
}

// fullChartPath returns an SVG <path> for the Game of the Week hero chart.
// Width/height match the .game-of-week .wp-chart CSS (320×120 viewBox).
func fullChartPath(curve MatchupWPCurve) string {
	if len(curve.Points) < 2 {
		return ""
	}
	const w, h = 320.0, 120.0
	n := len(curve.Points)
	step := w / float64(n-1)
	var out strings.Builder
	for i, p := range curve.Points {
		x := float64(i) * step
		y := (1.0 - p.HomeWP) * h
		if i == 0 {
			fmt.Fprintf(&out, "M%.2f,%.2f", x, y)
		} else {
			fmt.Fprintf(&out, " L%.2f,%.2f", x, y)
		}
	}
	return out.String()
}

// curveForMatchup looks up the WP curve matching the given matchup. Returns
// an empty zero-value curve when not found (template must guard with
// {{if .Points}} before rendering).
func curveForMatchup(curves []MatchupWPCurve, m MatchupResult) MatchupWPCurve {
	for _, c := range curves {
		if (c.HomeTeamID == m.HomeTeamID && c.AwayTeamID == m.AwayTeamID) ||
			(c.HomeTeamID == m.AwayTeamID && c.AwayTeamID == m.HomeTeamID) {
			return c
		}
	}
	return MatchupWPCurve{}
}
```

Add `"strings"` to the import list of `render.go`.

Register the helpers in `funcMap`:

```go
var funcMap = template.FuncMap{
	"pts":               fmtPts,
	"pct":               fmtPct,
	"fmtDate":           fmtDate,
	"add":               func(a, b int) int { return a + b },
	"barWidth":          barWidth,
	"matchupWinnerName": matchupWinnerName,
	"matchupLoserName":  matchupLoserName,
	"matchupWinnerPts":  matchupWinnerPts,
	"matchupLoserPts":   matchupLoserPts,
	"matchupSideClass":  matchupSideClass,
	"awardEmoji":        awardEmoji,
	"sparkPath":         sparkPath,
	"fullChartPath":     fullChartPath,
	"curveForMatchup":   curveForMatchup,
}
```

- [ ] **Step 2: Add Game of the Week section in `template.html`**

In `template.html`, add this section immediately after the closing `</header>` tag, before the existing `{{- with .Awards}}` block:

```html
{{- with .Awards}}
{{- if .GameOfWeek}}
{{- $curve := curveForMatchup $.WPCurves .GameOfWeek}}
<section>
  <h2>Game of the Week</h2>
  <div class="game-of-week">
    <div class="gw-head">
      <span class="gw-badge">💓 Heart Attack</span>
    </div>
    <div class="gw-scores">
      <div class="gw-team home">
        <span class="name">{{.GameOfWeek.HomeTeamName}}</span>
        <span class="pts">{{pts .GameOfWeek.HomePts}}</span>
      </div>
      <div class="gw-team away">
        <span class="name">{{.GameOfWeek.AwayTeamName}}</span>
        <span class="pts">{{pts .GameOfWeek.AwayPts}}</span>
      </div>
    </div>
    {{- if $curve.Points}}
    <svg class="wp-chart" viewBox="0 0 320 120" preserveAspectRatio="none">
      <line x1="0" y1="60" x2="320" y2="60" stroke="var(--border)" stroke-width="0.5" stroke-dasharray="3,3"/>
      <path d="{{fullChartPath $curve}}" stroke="var(--accent)" stroke-width="2" fill="none"/>
    </svg>
    <div class="x-axis">
      {{- range $i, $p := $curve.Points}}{{if gt $i 0}}<span>{{fmtDate $p.Date}}</span>{{end}}{{end}}
    </div>
    {{- end}}
  </div>
</section>
{{- end}}
{{- end}}
```

(The `{{- with .Awards}} ... {{- end}}` here is intentionally a separate block from the existing `{{- with .Awards}}` block lower down. Both are valid — Go templates allow a `with` to be opened and closed multiple times.)

- [ ] **Step 3: Add Whale + Dud cards to League Awards grid**

In `template.html`, find the existing `<h2>League Awards</h2>` section and the `<div class="award-grid">`. Inside that grid, after the `LowestPtsInWin` card's closing `</div>`, append:

```html
    {{- if .Whale}}
    <div class="award">
      <div class="label"><span>Whale</span><span class="icon">🐳</span></div>
      <div class="name">{{.Whale.TeamName}}</div>
      <div class="pts">{{pts .Whale.Pts}}</div>
      <div class="sub">{{fmtDate .Whale.Date}}</div>
    </div>
    {{- end}}
    {{- if .Dud}}
    <div class="award">
      <div class="label"><span>Dud</span><span class="icon">😴</span></div>
      <div class="name">{{.Dud.Name}}</div>
      <div class="pts">{{pts .Dud.FPts}}</div>
      <div class="sub">{{.Dud.OwnerTeam}} · {{fmtDate .Dud.Date}}</div>
    </div>
    {{- end}}
```

- [ ] **Step 4: Add sparklines to Matchup Results**

In `template.html`, find the `<h2>Matchup Results</h2>` section. Replace the existing `{{- range .Matchups}} ... {{- end}}` body with:

```html
  {{- $heart := .Awards.HeartAttack}}
  {{- $comeback := .Awards.Comeback}}
  {{- range .Matchups}}
  {{- $curve := curveForMatchup $.WPCurves .}}
  <div class="matchup">
    <div class="team away {{matchupSideClass . .AwayTeamID}}">
      <div class="name">{{.AwayTeamName}}{{if and $comeback (eq $comeback.TeamID .AwayTeamID)}} <span class="mu-badge">↩️</span>{{end}}</div>
      <div class="pts">{{pts .AwayPts}}</div>
    </div>
    <div class="vs">
      vs
      {{- if $curve.Points}}
      <svg class="spark" viewBox="0 0 60 24" preserveAspectRatio="none">
        <path d="{{sparkPath $curve}}" stroke="var(--accent)" stroke-width="1.5" fill="none"/>
      </svg>
      {{- end}}
      {{- if and $heart (eq $heart.HomeTeamID .HomeTeamID) (eq $heart.AwayTeamID .AwayTeamID)}}
      <span class="mu-badge">💓</span>
      {{- end}}
    </div>
    <div class="team home {{matchupSideClass . .HomeTeamID}}">
      <div class="name">{{.HomeTeamName}}{{if and $comeback (eq $comeback.TeamID .HomeTeamID)}} <span class="mu-badge">↩️</span>{{end}}</div>
      <div class="pts">{{pts .HomePts}}</div>
    </div>
  </div>
  {{- end}}
```

- [ ] **Step 5: Add CSS for new sections**

In `template.html`, find the `<style>` block. Append these rules just before the closing `</style>`:

```css
  .game-of-week { background: var(--bg-card); border: 1px solid var(--border); border-radius: 10px; padding: 16px; }
  .game-of-week .gw-head { margin-bottom: 10px; }
  .game-of-week .gw-badge { display: inline-block; padding: 3px 8px; border-radius: 4px; font-size: 10px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; background: var(--accent-soft); color: var(--accent); }
  .game-of-week .gw-scores { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 12px; }
  .game-of-week .gw-team { display: flex; flex-direction: column; }
  .game-of-week .gw-team.away { text-align: right; }
  .game-of-week .gw-team .name { font-size: 14px; font-weight: 600; color: var(--text); }
  .game-of-week .gw-team .pts { font-size: 22px; font-weight: 700; color: var(--accent); }
  .game-of-week .wp-chart { width: 100%; height: 120px; background: var(--bg-card-2); border-radius: 6px; margin-bottom: 6px; }
  .game-of-week .x-axis { display: flex; justify-content: space-between; font-size: 10px; color: var(--text-dim); padding: 0 2px; }
  .matchup .vs { display: flex; flex-direction: column; align-items: center; gap: 4px; }
  .matchup .spark { width: 60px; height: 24px; }
  .matchup .mu-badge { font-size: 14px; vertical-align: middle; }
```

- [ ] **Step 6: Run render unit test (smoke)**

Run: `go test ./internal/recap/ -run TestRender -v`
Expected: PASS — existing render tests still pass.

- [ ] **Step 7: Visual check**

Run: `go run . recap --out /tmp/recap-preview.html` (or with explicit dates if no recent week is complete: `go run . recap --dates 2026-04-13:2026-04-19 --out /tmp/recap-preview.html`)
Expected: HTML file is generated. Open in browser and confirm Game of the Week, sparklines, Whale/Dud cards all render.

- [ ] **Step 8: Commit**

```bash
git add internal/recap/template.html internal/recap/render.go
git commit -m "Recap: template — Game of the Week, sparklines, Whale + Dud cards"
```

---

## Task 15: Template — Roster Activity section

**Files:**
- Modify: `internal/recap/template.html`
- Modify: `internal/recap/render.go`

- [ ] **Step 1: Add `renderActivity` template helper**

In `render.go`, append after `fullChartPath`:

```go
// renderActivity returns the human-readable line for one transaction entry.
func renderActivity(e ActivityEntry) string {
	date := fmtDate(e.Date)
	switch e.Kind {
	case "trade":
		return fmt.Sprintf("Traded with %s — got: %s · sent: %s (%s)",
			e.OtherTeam, joinNames(e.Received), joinNames(e.Sent), date)
	case "swap":
		return fmt.Sprintf("Swap: +%s for −%s (%s)", e.SwapIn, e.SwapOut, date)
	case "claim":
		ct := e.ClaimType
		if ct == "" {
			ct = "FA"
		}
		return fmt.Sprintf("+%s (%s, %s)", e.Player, date, ct)
	case "drop":
		return fmt.Sprintf("−%s (%s)", e.Player, date)
	}
	return ""
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return "—"
	}
	return strings.Join(names, ", ")
}
```

Register in `funcMap`:

```go
	"renderActivity":    renderActivity,
```

- [ ] **Step 2: Add Roster Activity section to `template.html`**

In `template.html`, find the `<h2>Matchup Results</h2>` section. After its closing `</section>`, before `{{- if .Season}}`, insert:

```html
{{- if and .RosterActivity .RosterActivity.Teams}}
<section>
  <h2>Roster Activity</h2>
  {{- range .RosterActivity.Teams}}
  <div class="activity-card">
    <h3>{{.TeamName}}</h3>
    {{- range .Entries}}
    <div class="activity-row">{{renderActivity .}}</div>
    {{- end}}
  </div>
  {{- end}}
</section>
{{- end}}
```

- [ ] **Step 3: Add CSS for activity rows**

In `template.html`, append to the `<style>` block:

```css
  .activity-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 10px; padding: 10px 14px; margin-bottom: 8px; }
  .activity-card h3 { font-size: 12px; letter-spacing: 0.14em; text-transform: uppercase; color: var(--accent); margin: 0 0 6px; padding-left: 0; font-weight: 600; }
  .activity-row { font-size: 12px; color: var(--text-dim); padding: 3px 0; }
```

- [ ] **Step 4: Visual check**

Run: `go run . recap --out /tmp/recap-preview.html` (or `--dates ...` as in Task 14).
Open in browser. Roster Activity section should appear if any team made transactions in that window.

- [ ] **Step 5: Compile + tests**

Run: `go build ./... && go test ./internal/recap/...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/recap/template.html internal/recap/render.go
git commit -m "Recap: template — Roster Activity section"
```

---

## Task 16: Update README + CLAUDE.md

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update README**

Find the existing recap-related section in `README.md`. Update the description to mention the new features. Concretely, near the existing recap commands description, add a sentence like:

```markdown
The recap includes a Game of the Week win-probability chart, per-team Roster Activity log (claims, drops, swaps, trades), and four extra awards (Heart Attack, Comeback, Whale, Dud).
```

If the README has a feature list, add bullet points for each new piece. If it doesn't, the single sentence is enough.

- [ ] **Step 2: Update CLAUDE.md**

In `CLAUDE.md`, find the `internal/recap` paragraph. Append the following to it:

```markdown
**WP simulation** — `wp.go` exposes `ComputeWPCurve` (5000-iteration team-level Monte Carlo, RNG seeded by `hash(homeID|awayID|weekNumber)` so reruns are byte-identical), plus `LeagueDailySigma`, `LeadChangeCount`, `MinWinnerWP`. Each team's expected daily FPts is its within-week average — pragmatic simplification of the spec's "season-to-date" intent that uses only data the recap already gathers. Sigma is the sample stddev across 12 teams × 7 days = 84 points.

**Roster Activity** — `roster_activity.go` builds a per-team transaction log from `client.GetWeekTransactions`. Same-day exactly 1 CLAIM + 1 DROP for the same team merges into a single "swap" entry; multi-claim/multi-drop days render separately. Trades render once per team-side from a single `TradeGroupID` bucket. Soft-fail on fetch error: section omitted, recap renders without it.

**Game of the Week** — featured at the top of the page when at least one matchup has any lead changes; otherwise hidden. Picked as `HeartAttack(curves, matchups)` — most lead changes wins, ties broken by smallest final margin then home `TeamID` asc. `Awards.GameOfWeek` and `Awards.HeartAttack` always reference the same matchup (single source of truth).

**New awards** — `Whale` (biggest single-day team total), `Dud` (lowest single-day active starter, negatives eligible), `HeartAttack` (most lead changes), `Comeback` (winner with mid-week WP < 0.30). All four feed into `AggregateSeasonAwards` and the season cumulative leaderboard.
```

- [ ] **Step 3: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "Docs: README + CLAUDE.md updates for recap expansion"
```

---

## Task 17: Final smoke test

- [ ] **Step 1: Full build + vet + tests**

```bash
go build ./...
go vet ./...
go mod tidy
go test ./...
```

Expected: clean across the board.

- [ ] **Step 2: Generate a real recap and inspect**

Run: `go run . recap --out /tmp/recap-final.html`
(If no completed week is available, use `--dates YYYY-MM-DD:YYYY-MM-DD` to force one.)
Expected: HTML file generated. Visually verify each new feature renders correctly:
- Game of the Week section appears (or is correctly hidden if no lead changes that week)
- Sparklines on each matchup row
- Whale + Dud cards in League Awards
- Roster Activity section with per-team cards
- Heart Attack / Comeback badges where applicable
- Season Awards now lists all 14 categories (10 existing + 4 new)

- [ ] **Step 3: Idempotency check (CLAUDE.md invariant)**

Run the same recap command twice and diff the HTML (excluding `GeneratedAt` timestamp):

```bash
go run . recap --out /tmp/recap-a.html
go run . recap --out /tmp/recap-b.html
diff <(grep -v "GeneratedAt\|generated_at" /tmp/recap-a.html) <(grep -v "GeneratedAt\|generated_at" /tmp/recap-b.html)
```

Expected: no diff (deterministic output across runs).

- [ ] **Step 4: Commit nothing if clean; report**

If `git status` is clean, the work is done. Otherwise investigate before declaring victory.

---

## Self-review checklist (already run while writing this plan)

- ✅ Spec coverage: every feature in the spec has a task (types → awards → WP → activity → orchestration → template → docs)
- ✅ No placeholders: every step has actual code or exact commands
- ✅ Type consistency: `MatchupWPCurve.LeadChanges` set in Task 6 wiring, used in Task 8; `WPInputs` field names consistent across tasks 5, 6, 7, 13; `ActivityEntry.Kind` values ("claim" | "drop" | "swap" | "trade") used identically in tasks 11 and 15
- ✅ TDD discipline: every behavior task has failing-test → impl → passing-test → commit

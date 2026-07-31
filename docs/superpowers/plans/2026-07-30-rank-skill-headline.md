# Rank-Skill Headline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the projection dashboard's MAE headline with within-day Spearman rank skill by role plus the realized-vs-hindsight lineup gap, demoting MAE to a calibration diagnostic that carries a visible skill-vs-baseline score.

**Architecture:** Rank skill is a pure addition to `internal/report` (new `rank.go`), computed by grouping already-stored `analysis.GradeRow`s by `dt` before correlating. The lineup gap needs a new durable date-partitioned NDJSON store, `internal/lineupgap`, produced by `cmd/grade.go` (which already holds the necessary `[]fantrax.DayRoster`) and published as its own `gap.json` alongside `model.json`/`value.json`/`views.json`.

**Tech Stack:** Go 1.x (stdlib only for the new math — no new dependencies), `internal/ndjsonstore` generics, vanilla-JS dashboard SPA with vendored Chart.js.

**Spec:** `docs/superpowers/specs/2026-07-30-rank-skill-headline-design.md`
**Issue:** rosterbot-aei
**Branch:** `feat/rosterbot-aei-rank-skill-headline` (already created)

## Global Constraints

- **No new Go dependencies.** All correlation math is stdlib (`math`, `sort`).
- **Tie-corrected Spearman only.** The shortcut `1 - 6*sum(d^2)/(n(n^2-1))` is forbidden — graded rows are full of ties at `actual = 0`.
- **`minDayRows = 3`** — days with fewer rows in the role are excluded from the rho mean. **Corrected in review** (task 9): this plan originally specified 5, an unmeasured guess that failed the reproduction gate on pitchers (it discards 15 of 23 usable pitcher-days, since most graded pitcher-days carry only 3-4 rows). A threshold sweep against the 850 production rows showed 3 is the only value that reproduces both roles' triage statistics exactly — see the corrected Task 1 code block and `docs/superpowers/specs/2026-07-30-rank-skill-headline-design.md` §1.2.
- **Rho is never pooled across roles.** `View.Rho`, `SystemScore.Rho`, and `Model.Compare[w|all]` are all nil when `role == "all"`.
- **`Metrics` (MAE/Bias/RMSE/N) keeps its exact current meaning.** Existing tests in `internal/report/aggregate_test.go` must not be edited to pass.
- After every code change: `go vet ./...` and `go mod tidy` (gofmt/vet also run via PostToolUse hooks).
- Commit after each task. Do not push until the final task.
- `internal/report` and `internal/lineupgap` are **pure** — no I/O, no network, no `os` calls.

---

## File Structure

**New files**

| File | Responsibility |
|---|---|
| `internal/report/rank.go` | Tie-corrected Spearman, `RhoStat`, `withinDayRho` |
| `internal/report/rank_test.go` | Correlation math + aggregation tests |
| `internal/lineupgap/lineupgap.go` | `Row`, `Writer`, key layout, marshal helpers |
| `internal/lineupgap/reader.go` | `Reader` |
| `internal/lineupgap/model.go` | `Model`, `WindowSummary`, `Point`, `BuildModel` |
| `internal/lineupgap/lineupgap_test.go` | Row/store round-trip |
| `internal/lineupgap/model_test.go` | `BuildModel` window math |

**Modified files**

| File | Change |
|---|---|
| `internal/report/aggregate.go` | `Metrics.SkillVsMean`, `SystemScore.Rho`, `rankSystems` ordering + Best gate |
| `internal/report/model.go` | `View.Rho`, nil-for-"all" wiring in `Aggregate` |
| `internal/statestore/layout/layout.go` | `LineupGaps` artifact + `All()` |
| `internal/statestore/layout/layout_test.go` | Add prefix to the `want` list |
| `internal/statestore/statestore.go` | `lineupGapArtifact`, `LineupGapWriter`, `LineupGapReader` |
| `cmd/grade.go` | Compute + soft-fail write of gap rows |
| `cmd/projection-site.go` | `renderGapSite` |
| `web/dashboard/api.js` | `reportGap()` |
| `web/dashboard/projections.js` | Headline block, rho ranking, MAE demotion |
| `CLAUDE.md`, `README.md` | New store + changed headline semantics |

---

## Task 1: Tie-corrected Spearman and RhoStat

**Files:**
- Create: `internal/report/rank.go`
- Create: `internal/report/rank_test.go`

**Interfaces:**
- Consumes: `analysis.GradeRow` (fields `Dt`, `Projected`, `Actual`) from `internal/analysis`.
- Produces: `type RhoStat struct{ Rho, SE float64; Days, DaysPositive int }`; `func withinDayRho(rows []analysis.GradeRow) *RhoStat`; `func spearman(xs, ys []float64) (float64, bool)`; `const minDayRows = 3`.

- [ ] **Step 1: Write the failing test**

Create `internal/report/rank_test.go`:

```go
// internal/report/rank_test.go
package report

import (
	"math"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func TestSpearman_PerfectConcordanceAndInversion(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	up := []float64{10, 20, 30, 40, 50}
	down := []float64{50, 40, 30, 20, 10}

	if got, ok := spearman(xs, up); !ok || !approx(got, 1) {
		t.Errorf("concordant: got %v ok=%v, want 1", got, ok)
	}
	if got, ok := spearman(xs, down); !ok || !approx(got, -1) {
		t.Errorf("inverted: got %v ok=%v, want -1", got, ok)
	}
}

// The shortcut formula 1-6*sum(d^2)/(n(n^2-1)) is WRONG in the presence of
// ties, and graded rows are full of them (every player who didn't play sits at
// actual=0). This case pins the average-rank correction.
//
// xs ranks: 1, 2, 3, 4        (no ties)
// ys = {5, 5, 9, 9} ranks: 1.5, 1.5, 3.5, 3.5
// Pearson over those rank vectors:
//   mean(xr)=2.5, mean(yr)=2.5
//   cov terms: (-1.5)(-1)+(-0.5)(-1)+(0.5)(1)+(1.5)(1) = 1.5+0.5+0.5+1.5 = 4
//   sxx = 2.25+0.25+0.25+2.25 = 5 ; syy = 1+1+1+1 = 4
//   rho = 4 / sqrt(5*4) = 4/sqrt(20) = 0.894427190999916
func TestSpearman_TieCorrected(t *testing.T) {
	xs := []float64{1, 2, 3, 4}
	ys := []float64{5, 5, 9, 9}
	got, ok := spearman(xs, ys)
	if !ok {
		t.Fatal("spearman reported undefined for a well-defined tied case")
	}
	want := 4 / math.Sqrt(20)
	if !approx(got, want) {
		t.Errorf("tied spearman = %v, want %v (did you use the no-ties shortcut?)", got, want)
	}
}

func TestSpearman_ZeroVarianceIsUndefined(t *testing.T) {
	if _, ok := spearman([]float64{1, 2, 3}, []float64{7, 7, 7}); ok {
		t.Error("all-tied y must be undefined, not a number")
	}
	if _, ok := spearman([]float64{4, 4, 4}, []float64{1, 2, 3}); ok {
		t.Error("all-tied x must be undefined, not a number")
	}
	if _, ok := spearman([]float64{1}, []float64{1}); ok {
		t.Error("n<2 must be undefined")
	}
}

func gradeDay(dt string, pairs ...[2]float64) []analysis.GradeRow {
	out := make([]analysis.GradeRow, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, analysis.GradeRow{Dt: dt, Projected: p[0], Actual: p[1]})
	}
	return out
}

func TestWithinDayRho_AveragesPerDayAndCountsPositives(t *testing.T) {
	var rows []analysis.GradeRow
	// Day 1: perfectly concordant → +1
	rows = append(rows, gradeDay("2026-06-01",
		[2]float64{1, 1}, [2]float64{2, 2}, [2]float64{3, 3}, [2]float64{4, 4}, [2]float64{5, 5})...)
	// Day 2: perfectly inverted → -1
	rows = append(rows, gradeDay("2026-06-02",
		[2]float64{1, 5}, [2]float64{2, 4}, [2]float64{3, 3}, [2]float64{4, 2}, [2]float64{5, 1})...)

	rs := withinDayRho(rows)
	if rs == nil {
		t.Fatal("withinDayRho returned nil for two valid days")
	}
	if rs.Days != 2 {
		t.Errorf("Days = %d, want 2", rs.Days)
	}
	if !approx(rs.Rho, 0) {
		t.Errorf("Rho = %v, want 0 (mean of +1 and -1)", rs.Rho)
	}
	if rs.DaysPositive != 1 {
		t.Errorf("DaysPositive = %d, want 1", rs.DaysPositive)
	}
	// sd of {+1,-1} with n-1 denominator = sqrt(2); se = sqrt(2)/sqrt(2) = 1
	if !approx(rs.SE, 1) {
		t.Errorf("SE = %v, want 1", rs.SE)
	}
}

func TestWithinDayRho_DropsThinAndDegenerateDays(t *testing.T) {
	var rows []analysis.GradeRow
	// 2 rows — below minDayRows(3), must be dropped.
	rows = append(rows, gradeDay("2026-06-01",
		[2]float64{1, 1}, [2]float64{2, 2})...)
	// 5 rows but every actual identical — undefined, must be dropped.
	rows = append(rows, gradeDay("2026-06-02",
		[2]float64{1, 7}, [2]float64{2, 7}, [2]float64{3, 7}, [2]float64{4, 7}, [2]float64{5, 7})...)

	if rs := withinDayRho(rows); rs != nil {
		t.Errorf("want nil when no day qualifies, got %+v", rs)
	}
}

func TestWithinDayRho_EmptyInput(t *testing.T) {
	if rs := withinDayRho(nil); rs != nil {
		t.Errorf("want nil for no rows, got %+v", rs)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/report/ -run 'TestSpearman|TestWithinDayRho' -v`
Expected: FAIL — compile error, `undefined: spearman`, `undefined: withinDayRho`.

- [ ] **Step 3: Write the implementation**

Create `internal/report/rank.go`:

```go
package report

import (
	"math"
	"sort"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// RhoStat is within-day rank skill for one role: the mean of per-day Spearman
// correlations between projected and actual, with the spread across days that
// says whether the mean is real.
//
// It exists because MAE at player-day grain is near-blind to model skill —
// hitter projections barely vary (sd 0.66) against enormous daily outcome
// variance (sd 5.52), so MAE measures baseball, not the model. Rank skill is
// what a lineup optimiser actually consumes: it only ever needs today's roster
// in the right order.
type RhoStat struct {
	Rho          float64 `json:"rho"`          // mean of per-day rho
	SE           float64 `json:"se"`           // sd(daily rho) / sqrt(days)
	Days         int     `json:"days"`         // days meeting minDayRows with defined rho
	DaysPositive int     `json:"daysPositive"` // of those, how many were > 0
}

// minDayRows is the smallest number of players in a (day, role) cell worth
// correlating. Calibrated, not guessed: below 3, thin 1-2-row days inject
// noise into the hitter mean; above 3, the pitcher sample collapses (most
// graded pitcher-days carry only 3-4 rows). 3 is the only value reproducing
// both roles' reference statistics. Corrected in review — originally 5.
const minDayRows = 3

// averageRanks returns 1-based ranks for xs, with tied values sharing the mean
// of the ranks they span. The average-rank correction is what makes Spearman
// valid under ties — and graded rows are full of them, since every player who
// did not play sits at actual = 0.
func averageRanks(xs []float64) []float64 {
	n := len(xs)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j+1 < n && xs[idx[j+1]] == xs[idx[i]] {
			j++
		}
		// Positions i..j (0-based) occupy ranks i+1..j+1; their mean is
		// (i+j)/2 + 1. Every member of the tie block gets it.
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			ranks[idx[k]] = avg
		}
		i = j + 1
	}
	return ranks
}

// pearson returns the correlation of xs and ys. ok is false when either vector
// has zero variance, where correlation is undefined rather than zero.
func pearson(xs, ys []float64) (float64, bool) {
	n := float64(len(xs))
	if n < 2 {
		return 0, false
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= n
	my /= n

	var sxy, sxx, syy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// spearman is the tie-corrected rank correlation: Pearson over average ranks.
//
// Do NOT replace this with 1 - 6*sum(d^2)/(n(n^2-1)). That shortcut is only
// valid when there are no ties, and this data always has them.
func spearman(xs, ys []float64) (float64, bool) {
	if len(xs) != len(ys) {
		return 0, false
	}
	return pearson(averageRanks(xs), averageRanks(ys))
}

// withinDayRho groups rows by date, correlates projected against actual within
// each day, and returns the mean across days with its standard error.
//
// Grouping by day first is the whole point: it strips the day effect (weather,
// schedule size, who had a game) and the scale, leaving only "did we order this
// day's players correctly".
//
// Callers must pass a ROLE-FILTERED slice. Pooling hitters and pitchers would
// let a system that merely knows "a starting pitcher outscores a bench catcher"
// post a high rho carrying zero within-role skill — and the optimiser runs the
// two roles in separate passes, so cross-role ordering is information it never
// uses. Returns nil when no day qualifies.
func withinDayRho(rows []analysis.GradeRow) *RhoStat {
	byDay := map[string][]analysis.GradeRow{}
	for _, r := range rows {
		byDay[r.Dt] = append(byDay[r.Dt], r)
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days) // deterministic output regardless of map order

	var vals []float64
	for _, d := range days {
		g := byDay[d]
		if len(g) < minDayRows {
			continue
		}
		proj := make([]float64, len(g))
		act := make([]float64, len(g))
		for i, r := range g {
			proj[i] = r.Projected
			act[i] = r.Actual
		}
		rho, ok := spearman(proj, act)
		if !ok {
			continue // a day with no variance carries no ordering information
		}
		vals = append(vals, rho)
	}
	if len(vals) == 0 {
		return nil
	}

	var sum float64
	pos := 0
	for _, v := range vals {
		sum += v
		if v > 0 {
			pos++
		}
	}
	mean := sum / float64(len(vals))

	se := 0.0
	if len(vals) > 1 {
		var ss float64
		for _, v := range vals {
			d := v - mean
			ss += d * d
		}
		sd := math.Sqrt(ss / float64(len(vals)-1))
		se = sd / math.Sqrt(float64(len(vals)))
	}
	return &RhoStat{Rho: mean, SE: se, Days: len(vals), DaysPositive: pos}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/report/ -run 'TestSpearman|TestWithinDayRho' -v`
Expected: PASS, 5 tests.

Then run the whole package to confirm nothing regressed:
Run: `go test ./internal/report/`
Expected: PASS (`approx` is already defined in `aggregate_test.go`; if the compiler reports it as redeclared, delete the duplicate from `rank_test.go` — this plan does not add one).

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add internal/report/rank.go internal/report/rank_test.go
git commit -m "feat(report): tie-corrected within-day Spearman rank skill (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 2: Skill score against a constant-mean baseline

**Files:**
- Modify: `internal/report/aggregate.go` (add field to `Metrics` at lines 14-20; add helper; call from `computeMetrics` at lines 56-69)
- Modify: `internal/report/rank_test.go` (append tests)

**Interfaces:**
- Consumes: `analysis.GradeRow` (`Actual`), `Metrics` from Task 1's package.
- Produces: `Metrics.SkillVsMean float64` (JSON `skillVsMean`); `func skillVsMean(rows []analysis.GradeRow, mae float64) float64`.

- [ ] **Step 1: Write the failing test**

Append to `internal/report/rank_test.go`:

```go
// A projection pinned at the sample mean IS the baseline, so it must score
// exactly 0 skill. This is the number that makes the MAE demotion self-evident
// on the dashboard rather than merely asserted.
func TestSkillVsMean_ConstantAtMeanScoresZero(t *testing.T) {
	// actuals {0, 4, 8} → mean 4. A model projecting 4 every time has
	// MAE = (4+0+4)/3 = 8/3, identical to the baseline.
	rows := []analysis.GradeRow{
		{Projected: 4, Actual: 0, Diff: -4},
		{Projected: 4, Actual: 4, Diff: 0},
		{Projected: 4, Actual: 8, Diff: 4},
	}
	m := computeMetrics(rows)
	if !approx(m.SkillVsMean, 0) {
		t.Errorf("SkillVsMean = %v, want 0 for a constant-at-mean model", m.SkillVsMean)
	}
}

func TestSkillVsMean_PerfectModelScoresOne(t *testing.T) {
	rows := []analysis.GradeRow{
		{Projected: 0, Actual: 0, Diff: 0},
		{Projected: 4, Actual: 4, Diff: 0},
		{Projected: 8, Actual: 8, Diff: 0},
	}
	m := computeMetrics(rows)
	if !approx(m.SkillVsMean, 1) {
		t.Errorf("SkillVsMean = %v, want 1 for a perfect model", m.SkillVsMean)
	}
}

func TestSkillVsMean_WorseThanBaselineIsNegative(t *testing.T) {
	// actuals {0,4,8} → baseline MAE 8/3. This model is deliberately awful.
	rows := []analysis.GradeRow{
		{Projected: 8, Actual: 0, Diff: -8},
		{Projected: 0, Actual: 4, Diff: 4},
		{Projected: 0, Actual: 8, Diff: 8},
	}
	m := computeMetrics(rows)
	if m.SkillVsMean >= 0 {
		t.Errorf("SkillVsMean = %v, want negative for a model worse than the baseline", m.SkillVsMean)
	}
}

func TestSkillVsMean_DegenerateInputsDoNotDivideByZero(t *testing.T) {
	if m := computeMetrics(nil); m.SkillVsMean != 0 {
		t.Errorf("empty: SkillVsMean = %v, want 0", m.SkillVsMean)
	}
	// Every actual identical → baseline MAE is 0; guard against 1 - x/0.
	rows := []analysis.GradeRow{
		{Projected: 1, Actual: 5, Diff: 4},
		{Projected: 2, Actual: 5, Diff: 3},
	}
	m := computeMetrics(rows)
	if math.IsInf(m.SkillVsMean, 0) || math.IsNaN(m.SkillVsMean) {
		t.Errorf("SkillVsMean = %v, want a finite value when the baseline MAE is 0", m.SkillVsMean)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/report/ -run TestSkillVsMean -v`
Expected: FAIL — `m.SkillVsMean undefined (type Metrics has no field or method SkillVsMean)`.

- [ ] **Step 3: Write the implementation**

In `internal/report/aggregate.go`, replace the `Metrics` struct (lines 14-20) with:

```go
// Metrics is the accuracy summary for a set of graded rows.
//
// MAE/Bias/RMSE are CALIBRATION diagnostics, not a scoreboard. At player-day
// grain they are dominated by irreducible single-game variance, which is why
// SkillVsMean sits beside them: it states plainly how the model compares to
// projecting the sample mean every time. See internal/report/rank.go for the
// metric that does carry decision-relevant signal.
type Metrics struct {
	MAE  float64 `json:"mae"`
	Bias float64 `json:"bias"` // mean(actual - projected); positive = under-projecting
	RMSE float64 `json:"rmse"`
	N    int     `json:"n"`

	// SkillVsMean is 1 - MAE_model/MAE_constantAtSampleMean. Zero means the
	// model is exactly as good as ignoring every input and guessing the mean;
	// negative means worse than that.
	SkillVsMean float64 `json:"skillVsMean"`
}
```

Replace `computeMetrics` (lines 56-69) with:

```go
func computeMetrics(rows []analysis.GradeRow) Metrics {
	if len(rows) == 0 {
		return Metrics{}
	}
	var sumAbs, sumSigned, sumSq float64
	for _, r := range rows {
		d := r.Diff
		sumAbs += math.Abs(d)
		sumSigned += d
		sumSq += d * d
	}
	n := float64(len(rows))
	mae := sumAbs / n
	return Metrics{
		MAE:         mae,
		Bias:        sumSigned / n,
		RMSE:        math.Sqrt(sumSq / n),
		N:           len(rows),
		SkillVsMean: skillVsMean(rows, mae),
	}
}

// skillVsMean scores mae against the MAE of a constant projection at the
// sample mean of Actual. Returns 0 when there is nothing to compare against —
// no rows, or a baseline MAE of 0 (every actual identical), where the ratio is
// undefined rather than infinitely good.
func skillVsMean(rows []analysis.GradeRow, mae float64) float64 {
	if len(rows) == 0 {
		return 0
	}
	n := float64(len(rows))
	var sum float64
	for _, r := range rows {
		sum += r.Actual
	}
	mean := sum / n

	var base float64
	for _, r := range rows {
		base += math.Abs(r.Actual - mean)
	}
	base /= n
	if base == 0 {
		return 0
	}
	return 1 - mae/base
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/report/ -v`
Expected: PASS — all tests including the pre-existing `TestComputeMetrics` (which asserts only MAE/Bias/RMSE/N and must not need editing).

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add internal/report/aggregate.go internal/report/rank_test.go
git commit -m "feat(report): score MAE against a constant-mean baseline (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 3: Wire rho into View, SystemScore, and the ranking

**Files:**
- Modify: `internal/report/model.go` (`View` struct lines 18-28; `Aggregate` lines 88-167)
- Modify: `internal/report/aggregate.go` (`SystemScore` lines 71-80; `rankSystems` lines 122-150)
- Modify: `internal/report/rank_test.go` (append tests)

**Interfaces:**
- Consumes: `RhoStat`, `withinDayRho` (Task 1); `Metrics.SkillVsMean` (Task 2).
- Produces: `View.Rho *RhoStat` (JSON `rho`); `SystemScore.Rho *RhoStat` (JSON `rho`); `rankSystems` ordered by rho descending with the combined-SE `Best` gate; `Model.Compare["<w>|all"] == nil`.

- [ ] **Step 1: Write the failing test**

Append to `internal/report/rank_test.go`:

```go
import "time" // add to the existing import block if not present

// rhoDays builds `days` consecutive days of `n` rows each, with actual = k*projected
// so every day is perfectly concordant (rho = +1) when asc is true, and
// perfectly inverted (rho = -1) when false.
func rhoDays(system string, isPitcher bool, days, n int, asc bool) []analysis.GradeRow {
	var out []analysis.GradeRow
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for d := 0; d < days; d++ {
		dt := base.AddDate(0, 0, d).Format("2006-01-02")
		for i := 1; i <= n; i++ {
			actual := float64(i)
			if !asc {
				actual = float64(n - i + 1)
			}
			out = append(out, analysis.GradeRow{
				Dt: dt, System: system, PlayerID: string(rune('a' + i)),
				Projected: float64(i), Actual: actual, Diff: actual - float64(i),
				IsPitcher: isPitcher,
			})
		}
	}
	return out
}

// Rho has no defensible value pooled across roles, so it must be absent rather
// than computed-and-hoped-for. The type enforces it.
func TestAggregate_RhoIsNilForAllRole(t *testing.T) {
	rows := append(
		rhoDays(analysis.LegacySystem, false, 6, 8, true),
		rhoDays(analysis.LegacySystem, true, 6, 8, true)...,
	)
	latest := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	m := Aggregate(rows, latest, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	if v := m.Views[detailKey(analysis.LegacySystem, 30, "all")]; v.Rho != nil {
		t.Errorf(`View.Rho must be nil for role "all", got %+v`, v.Rho)
	}
	if v := m.Views[detailKey(analysis.LegacySystem, 30, "hitters")]; v.Rho == nil {
		t.Error(`View.Rho must be populated for role "hitters"`)
	}
	if c := m.Compare[viewKey(30, "all")]; c != nil {
		t.Errorf(`Compare["30|all"] must be nil — a pooled ranking has no defensible order, got %+v`, c)
	}
	if c := m.Compare[viewKey(30, "hitters")]; len(c) == 0 {
		t.Error(`Compare["30|hitters"] must be populated`)
	}
}

func TestRankSystems_OrdersByRhoDescending(t *testing.T) {
	// good orders every day correctly (+1); bad inverts every day (-1).
	rows := append(
		rhoDays("good-sys", false, 6, 8, true),
		rhoDays("bad-sys", false, 6, 8, false)...,
	)
	latest := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	got := rankSystems(rows, []string{"bad-sys", "good-sys"}, latest, 30, "hitters")

	if len(got) != 2 {
		t.Fatalf("got %d scores, want 2", len(got))
	}
	if got[0].System != "good-sys" {
		t.Errorf("leader = %q, want good-sys (ranking must be by rho, not MAE)", got[0].System)
	}
	if got[0].Rho == nil || !approx(got[0].Rho.Rho, 1) {
		t.Errorf("leader rho = %+v, want +1", got[0].Rho)
	}
	if !got[0].Best {
		t.Error("a leader separated by far more than the combined SE must be flagged Best")
	}
}

// Two systems whose rho means are within the combined standard error are not
// distinguishable, so neither may be crowned.
func TestRankSystems_NoBestWhenWithinCombinedSE(t *testing.T) {
	// Both systems perfectly concordant every day → identical rho (+1), SE 0.
	// A zero separation cannot exceed a zero threshold, so no Best.
	rows := append(
		rhoDays("sys-a", false, 6, 8, true),
		rhoDays("sys-b", false, 6, 8, true)...,
	)
	latest := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	got := rankSystems(rows, []string{"sys-a", "sys-b"}, latest, 30, "hitters")

	for _, s := range got {
		if s.Best {
			t.Errorf("system %q flagged Best despite a tie — expected too-close-to-call", s.System)
		}
	}
}

func TestRankSystems_EmptySystemsSortLastAndAreNeverBest(t *testing.T) {
	rows := rhoDays("has-data", false, 6, 8, true)
	latest := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	got := rankSystems(rows, []string{"empty-sys", "has-data"}, latest, 30, "hitters")

	if got[0].System != "has-data" {
		t.Errorf("leader = %q, want has-data", got[0].System)
	}
	if got[1].N != 0 || got[1].Best {
		t.Errorf("empty system must sort last and never be Best: %+v", got[1])
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/report/ -run 'TestAggregate_RhoIsNil|TestRankSystems' -v`
Expected: FAIL — `v.Rho undefined`, `got[0].Rho undefined`.

- [ ] **Step 3: Write the implementation**

**3a.** In `internal/report/model.go`, replace the `View` struct (lines 18-28):

```go
// View is the fully precomputed dashboard for one (system, window, role) triple.
type View struct {
	System    string        `json:"system"`
	Window    int           `json:"window"`
	Role      string        `json:"role"`
	Scorecard Scorecard     `json:"scorecard"`
	ByPos     []PositionRow `json:"byPos"`
	Calib     []CalibPoint  `json:"calib"`
	Misses    []Miss        `json:"misses"`
	Insights  []Insight     `json:"insights"`

	// Rho is within-day rank skill, the headline metric. It is nil for
	// role == "all": pooling hitters and pitchers has no defensible
	// interpretation (see withinDayRho), so the UI reads the two role views
	// instead of being handed a flattering pooled number.
	Rho *RhoStat `json:"rho,omitempty"`
}
```

**3b.** In `internal/report/model.go`, inside `Aggregate`'s detail loop, replace the `m.Views[key] = View{...}` assignment (lines 139-148) with:

```go
				// Rank skill is only defined within a single role.
				var rho *RhoStat
				if role != "all" {
					rho = withinDayRho(cur)
				}
				m.Views[key] = View{
					System:    sys,
					Window:    w,
					Role:      role,
					Scorecard: Scorecard{Cur: curM, Prior: priorM},
					ByPos:     bp,
					Calib:     calibration(cur),
					Misses:    worstMisses(cur, 25),
					Insights:  generateInsights(curM, priorM, bp, windowLabel(w)),
					Rho:       rho,
				}
```

**3c.** In `internal/report/model.go`, replace the comparison-panel loop (lines 153-165) with:

```go
	// Comparison panel: every captured system, ranked head-to-head per
	// window×role by rank skill.
	//
	// Compare is deliberately left nil for role == "all" — ranking systems on a
	// pooled cross-role rho has no defensible order, so the panel reads the
	// hitters and pitchers keys and stacks two tables. CompareTrends stays
	// populated for all three roles: an MAE trend line remains a legitimate
	// diagnostic under any role filter.
	for _, role := range stdRoles {
		for _, w := range stdWindows {
			key := viewKey(w, role)
			if role != "all" {
				m.Compare[key] = rankSystems(rows, m.Systems, latest, w, role)
			}
			trends := map[string][]TrendPoint{}
			for _, sys := range m.Systems {
				sysRole := filterRole(filterSystem(rows, sys), role)
				trends[sys] = windowTrend(sysRole, latest, w)
			}
			m.CompareTrends[key] = trends
		}
	}
```

**3d.** In `internal/report/aggregate.go`, replace the `SystemScore` struct (lines 71-80):

```go
// SystemScore is one projection system's accuracy for a window×role, used by
// the head-to-head comparison panel.
//
// Best flags the highest-rho system, but only when it is separated from the
// runner-up by more than the combined standard error — otherwise the panel
// reports too-close-to-call rather than crowning a coin flip. MAE/Bias/RMSE
// remain as columns but no longer decide the winner: see rank.go for why.
type SystemScore struct {
	System string   `json:"system"`
	MAE    float64  `json:"mae"`
	Bias   float64  `json:"bias"`
	RMSE   float64  `json:"rmse"`
	N      int      `json:"n"`
	Rho    *RhoStat `json:"rho,omitempty"`
	Best   bool     `json:"best"`
}
```

**3e.** In `internal/report/aggregate.go`, replace `rankSystems` (lines 122-150):

```go
// rankSystems scores each system over the window×role slice and returns them
// ordered by within-day rank skill descending (best first).
//
// Callers pass a single role — never "all". Systems with no rows in the window
// sort last (N 0) and are never marked Best.
func rankSystems(rows []analysis.GradeRow, systems []string, latest time.Time, window int, role string) []SystemScore {
	out := make([]SystemScore, 0, len(systems))
	for _, sys := range systems {
		slice := windowRows(filterRole(filterSystem(rows, sys), role), latest, window)
		m := computeMetrics(slice)
		out = append(out, SystemScore{
			System: sys, MAE: m.MAE, Bias: m.Bias, RMSE: m.RMSE, N: m.N,
			Rho: withinDayRho(slice),
		})
	}

	// A system with no rho (too few qualifying days) still outranks one with no
	// data at all, but sorts below any system that produced a real correlation.
	rhoKey := func(s SystemScore) float64 {
		if s.Rho == nil {
			return math.Inf(-1)
		}
		return s.Rho.Rho
	}
	sort.Slice(out, func(i, j int) bool {
		// Empty (N==0) systems always sort after any system with data.
		if (out[i].N == 0) != (out[j].N == 0) {
			return out[j].N == 0
		}
		ki, kj := rhoKey(out[i]), rhoKey(out[j])
		if ki != kj {
			return ki > kj // higher rank skill first
		}
		return out[i].System < out[j].System // stable tiebreak
	})

	flagBest(out)
	return out
}

// flagBest marks the leader only when its rho beats the runner-up's by more
// than the combined standard error. Anything closer is indistinguishable from
// noise, and crowning it would restate the over-precision problem this metric
// was introduced to fix.
//
// "No competitor" and "uncomputable competitor" are NOT the same case, even
// though both leave out[1].Rho nil, and must not be collapsed back together.
// out[1].N == 0 means there is nothing to compare against, so the leader wins
// unopposed. out[1].N > 0 with a nil Rho means a real competitor exists but
// never cleared minDayRows on any single day — the SE inequality this
// function exists to enforce simply cannot be evaluated, so per its own
// prose ("otherwise") it does not hold, and no system is flagged.
func flagBest(out []SystemScore) {
	if len(out) == 0 || out[0].N == 0 || out[0].Rho == nil {
		return
	}
	if len(out) == 1 || out[1].N == 0 {
		out[0].Best = true
		return
	}
	if out[1].Rho == nil {
		return
	}
	lead, next := out[0].Rho, out[1].Rho
	sep := math.Sqrt(lead.SE*lead.SE + next.SE*next.SE)
	if lead.Rho-next.Rho > sep {
		out[0].Best = true
	}
}
```

> **Corrected in review (commit `91b1875`).** The original guard above —
> `if len(out) == 1 || out[1].N == 0 || out[1].Rho == nil` — crowned the
> leader whenever the runner-up's rho was merely *uncomputable*
> (`out[1].N > 0` but no single day cleared `minDayRows`), bypassing the
> standard-error comparison this function exists to enforce. The code block
> above reflects the shipped fix, which splits the `N == 0` ("no competitor")
> case from the `Rho == nil` ("uncomputable competitor") case so only the
> former crowns unopposed.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/report/ -v`
Expected: PASS. If `TestAggregate_*` tests in the pre-existing `model_test.go` assert on `Compare["<w>|all"]` being non-empty, that assertion is now wrong by design — update it to assert nil and note the reason in the test comment.

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add internal/report/
git commit -m "feat(report): rank systems by within-day rho with a combined-SE Best gate (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 4: The Lineup Gap Store

**Files:**
- Create: `internal/lineupgap/lineupgap.go`
- Create: `internal/lineupgap/reader.go`
- Create: `internal/lineupgap/model.go`
- Create: `internal/lineupgap/lineupgap_test.go`
- Create: `internal/lineupgap/model_test.go`

**Interfaces:**
- Consumes: `ndjsonstore.Store`, `ndjsonstore.Write`, `ndjsonstore.ReadAll`, `ndjsonstore.NewFileStore`, `ndjsonstore.NewMemStore` from `internal/ndjsonstore`.
- Produces: `lineupgap.Row`; `Row.Efficiency() float64`; `Writer` interface with `WriteGaps(date time.Time, rows []Row) error`; `Reader` interface with `ReadAll() ([]Row, error)`; `NewWriter(ndjsonstore.Store) Writer`; `NewFileWriter(root string) Writer`; `NewReader(ndjsonstore.Store) Reader`; `NewFileReader(root string) Reader`; `ObjectKey(date time.Time) string`; `BuildModel(rows []Row, generatedAt time.Time) *Model`.

- [ ] **Step 1: Write the failing tests**

Create `internal/lineupgap/lineupgap_test.go`:

```go
package lineupgap

import (
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

func TestRow_Efficiency(t *testing.T) {
	r := Row{ActualPts: 90, OptimalPts: 100}
	if got := r.Efficiency(); got != 0.9 {
		t.Errorf("Efficiency = %v, want 0.9", got)
	}
	// A day where the hindsight-optimal lineup scored nothing (no games) must
	// not divide by zero.
	if got := (Row{ActualPts: 0, OptimalPts: 0}).Efficiency(); got != 0 {
		t.Errorf("Efficiency with zero optimal = %v, want 0", got)
	}
}

func TestObjectKey(t *testing.T) {
	d := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if got, want := ObjectKey(d), "dt=2026-07-20/gaps.ndjson"; got != want {
		t.Errorf("ObjectKey = %q, want %q", got, want)
	}
}

func TestWriteThenReadRoundTrip(t *testing.T) {
	store := ndjsonstore.NewMemStore()
	w := NewWriter(store)

	d1 := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGaps(d1, []Row{{Dt: "2026-07-20", ActualPts: 90, OptimalPts: 100, Gap: -10, StartedN: 13, BenchedN: 2}}); err != nil {
		t.Fatalf("write d1: %v", err)
	}
	if err := w.WriteGaps(d2, []Row{{Dt: "2026-07-21", ActualPts: 80, OptimalPts: 80, Gap: 0, StartedN: 13, BenchedN: 0}}); err != nil {
		t.Fatalf("write d2: %v", err)
	}

	rows, err := NewReader(store).ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// dt= partitions sort lexically, which is chronological.
	if rows[0].Dt != "2026-07-20" || rows[1].Dt != "2026-07-21" {
		t.Errorf("rows out of chronological order: %q, %q", rows[0].Dt, rows[1].Dt)
	}
	if rows[0].Gap != -10 || rows[0].StartedN != 13 || rows[0].BenchedN != 2 {
		t.Errorf("round-trip lost fields: %+v", rows[0])
	}
}

func TestReadAll_EmptyStoreIsNotAnError(t *testing.T) {
	rows, err := NewReader(ndjsonstore.NewMemStore()).ReadAll()
	if err != nil {
		t.Fatalf("empty store must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows from an empty store, want 0", len(rows))
	}
}
```

Create `internal/lineupgap/model_test.go`:

```go
package lineupgap

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// seriesRows builds `n` consecutive days ending at `end`, each losing `gap`
// points against a 100-point optimal.
func seriesRows(end time.Time, n int, gap float64) []Row {
	var out []Row
	for i := n - 1; i >= 0; i-- {
		d := end.AddDate(0, 0, -i)
		out = append(out, Row{
			Dt:         d.Format("2006-01-02"),
			ActualPts:  100 + gap,
			OptimalPts: 100,
			Gap:        gap,
			StartedN:   13,
		})
	}
	return out
}

func TestBuildModel_WindowSums(t *testing.T) {
	end := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	rows := seriesRows(end, 40, -5) // 40 days, 5 points lost per day
	m := BuildModel(rows, time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC))

	if m.LatestDate != "2026-07-30" {
		t.Errorf("LatestDate = %q, want 2026-07-30", m.LatestDate)
	}
	if len(m.Series) != 40 {
		t.Fatalf("Series has %d points, want 40", len(m.Series))
	}
	if m.Series[0].Dt >= m.Series[len(m.Series)-1].Dt {
		t.Error("Series must be ascending by date")
	}

	w7 := m.Windows["7"]
	if w7.Days != 7 {
		t.Errorf("7d Days = %d, want 7", w7.Days)
	}
	if !approx(w7.GapPts, -35) {
		t.Errorf("7d GapPts = %v, want -35", w7.GapPts)
	}
	if !approx(w7.OptimalPts, 700) {
		t.Errorf("7d OptimalPts = %v, want 700", w7.OptimalPts)
	}
	if !approx(w7.Efficiency, 665.0/700.0) {
		t.Errorf("7d Efficiency = %v, want %v", w7.Efficiency, 665.0/700.0)
	}

	// Season window ("0") spans everything.
	if s := m.Windows["0"]; s.Days != 40 || !approx(s.GapPts, -200) {
		t.Errorf("season window = %+v, want 40 days / -200 gap", s)
	}
}

func TestBuildModel_EmptyRows(t *testing.T) {
	m := BuildModel(nil, time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC))
	if m == nil {
		t.Fatal("BuildModel must return a valid empty model, not nil")
	}
	if len(m.Series) != 0 {
		t.Errorf("Series = %d points, want 0", len(m.Series))
	}
	if m.LatestDate != "" {
		t.Errorf("LatestDate = %q, want empty", m.LatestDate)
	}
	for _, k := range []string{"7", "14", "30", "0"} {
		if w, ok := m.Windows[k]; !ok || w.Days != 0 {
			t.Errorf("window %q must exist and be zeroed, got %+v ok=%v", k, w, ok)
		}
	}
}

func TestBuildModel_ZeroOptimalDoesNotDivideByZero(t *testing.T) {
	m := BuildModel([]Row{{Dt: "2026-07-30", ActualPts: 0, OptimalPts: 0}},
		time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC))
	if e := m.Windows["7"].Efficiency; math.IsNaN(e) || math.IsInf(e, 0) {
		t.Errorf("Efficiency = %v, want a finite value", e)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/lineupgap/ -v`
Expected: FAIL — the package does not exist yet.

If `ndjsonstore.NewMemStore` does not exist, check its actual name with `grep -n "func NewMemStore\|func NewFileStore" internal/ndjsonstore/filestore.go` and use the real one.

- [ ] **Step 3: Write the implementation**

Create `internal/lineupgap/lineupgap.go`:

```go
// Package lineupgap writes the durable Lineup Gap Store: how each day's
// applied lineup scored against the hindsight-optimal one, as NDJSON
// partitioned by date.
//
// This is the only metric on the dashboard denominated in points the league
// actually awards. Projection accuracy (internal/report) says how well a system
// ranks players; this says what that ranking cost or saved in the standings.
//
// There is deliberately NO system dimension. Exactly one lineup was applied
// each day, and the hindsight-optimal total does not depend on which projection
// system was consulted — so the partition key is dt= alone and the entity name
// lives in the storage prefix, exactly as in internal/teamvalue. The
// consequence worth remembering: this store cannot discriminate between
// projection systems. Rank skill answers "which system"; this answers "what is
// the whole pipeline costing us".
//
// Unlike the Team Value Store, this series IS backfillable: past-period roster
// snapshots are immutable and cached, so a missed day is recoverable with
// `grade --dates`.
package lineupgap

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// gapsFilename is the leaf object name in every partition.
const gapsFilename = "gaps.ndjson"

// Row is one day's lineup decision graded against hindsight.
//
// Gap is signed as actual - optimal, so it is zero at best and negative when
// points were left on the bench. That sign convention matches
// backtest.LineupDayResult.Gap, which is where these numbers come from.
type Row struct {
	Dt         string  `json:"dt"`
	ActualPts  float64 `json:"actual_pts"`
	OptimalPts float64 `json:"optimal_pts"`
	Gap        float64 `json:"gap"`       // actual - optimal; negative = left on bench
	StartedN   int     `json:"started_n"`
	BenchedN   int     `json:"benched_n"` // scored but not started
}

// Efficiency is the share of the hindsight-optimal total actually captured.
// Zero when there was no optimal total to capture (e.g. an off day).
func (r Row) Efficiency() float64 {
	if r.OptimalPts == 0 {
		return 0
	}
	return r.ActualPts / r.OptimalPts
}

// Writer persists a day's gap rows to the store.
type Writer interface {
	WriteGaps(date time.Time, rows []Row) error
}

// MarshalNDJSON serializes rows as newline-delimited JSON (one row per line).
func MarshalNDJSON(rows []Row) ([]byte, error) { return ndjsonstore.Marshal(rows) }

// UnmarshalNDJSON parses newline-delimited JSON (one Row per line).
func UnmarshalNDJSON(b []byte) ([]Row, error) { return ndjsonstore.Unmarshal[Row](b) }

func objectKey(date time.Time) string {
	return fmt.Sprintf("dt=%s/%s", date.UTC().Format("2006-01-02"), gapsFilename)
}

// ObjectKey is the store-relative partition key (dt=YYYY-MM-DD/gaps.ndjson).
func ObjectKey(date time.Time) string { return objectKey(date) }

type writer struct{ store ndjsonstore.Store }

// NewWriter returns a Writer persisting rows to store, partitioned as
// dt=YYYY-MM-DD/gaps.ndjson.
func NewWriter(store ndjsonstore.Store) Writer { return writer{store: store} }

// NewFileWriter returns a Writer over a local directory root.
func NewFileWriter(root string) Writer { return NewWriter(ndjsonstore.NewFileStore(root)) }

func (w writer) WriteGaps(date time.Time, rows []Row) error {
	return ndjsonstore.Write(w.store, objectKey(date), rows)
}
```

Create `internal/lineupgap/reader.go`:

```go
package lineupgap

import (
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore"
)

// Reader loads rows from the Lineup Gap Store (opposite of Writer). The whole
// series is read wholesale to draw the trend.
type Reader interface {
	ReadAll() ([]Row, error)
}

type reader struct{ store ndjsonstore.Store }

// NewReader returns a Reader over rows in store, partitioned as
// dt=YYYY-MM-DD/gaps.ndjson.
func NewReader(store ndjsonstore.Store) Reader { return reader{store: store} }

// NewFileReader returns a Reader over a local directory root.
func NewFileReader(root string) Reader { return NewReader(ndjsonstore.NewFileStore(root)) }

func (r reader) ReadAll() ([]Row, error) {
	// Empty prefix: every dt= partition sits directly under the store root.
	return ndjsonstore.ReadAll[Row](r.store, "", gapsFilename, nil)
}
```

Create `internal/lineupgap/model.go`:

```go
package lineupgap

import (
	"sort"
	"strconv"
	"time"
)

// stdWindows mirrors report.stdWindows so the dashboard's existing window
// toggle indexes Model.Windows directly. 0 is the season window.
var stdWindows = []int{7, 14, 30, 0}

// Model is the payload written to gap.json for the dashboard SPA.
type Model struct {
	GeneratedAt string                   `json:"generatedAt"`
	LatestDate  string                   `json:"latestDate"`
	Windows     map[string]WindowSummary `json:"windows"` // keyed "7","14","30","0"
	Series      []Point                  `json:"series"`  // daily, ascending
}

// WindowSummary is the gap rolled up over one trailing window.
type WindowSummary struct {
	Days       int     `json:"days"`
	ActualPts  float64 `json:"actualPts"`
	OptimalPts float64 `json:"optimalPts"`
	GapPts     float64 `json:"gapPts"`
	Efficiency float64 `json:"efficiency"`
}

// Point is one day on the gap chart.
type Point struct {
	Dt         string  `json:"dt"`
	GapPts     float64 `json:"gapPts"`
	ActualPts  float64 `json:"actualPts"`
	OptimalPts float64 `json:"optimalPts"`
}

// BuildModel rolls the stored series into the precomputed windows and the daily
// chart series. Pure: no I/O.
//
// An empty input still yields a valid Model with every window present and
// zeroed, so the dashboard renders "no data yet" rather than erroring.
func BuildModel(rows []Row, generatedAt time.Time) *Model {
	sorted := make([]Row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Dt < sorted[j].Dt })

	m := &Model{
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		Windows:     map[string]WindowSummary{},
		Series:      make([]Point, 0, len(sorted)),
	}
	for _, r := range sorted {
		m.Series = append(m.Series, Point{
			Dt: r.Dt, GapPts: r.Gap, ActualPts: r.ActualPts, OptimalPts: r.OptimalPts,
		})
	}
	if len(sorted) > 0 {
		m.LatestDate = sorted[len(sorted)-1].Dt
	}

	for _, w := range stdWindows {
		m.Windows[strconv.Itoa(w)] = summarize(sorted, m.LatestDate, w)
	}
	return m
}

// summarize sums the last `window` days ending at latest (inclusive).
// window <= 0 covers the whole series. ISO date strings sort lexicographically,
// so string comparison is correct — the same trick internal/report uses.
func summarize(rows []Row, latest string, window int) WindowSummary {
	var cutoff string
	if window > 0 && latest != "" {
		d, err := time.Parse("2006-01-02", latest)
		if err != nil {
			return WindowSummary{}
		}
		cutoff = d.AddDate(0, 0, -(window - 1)).Format("2006-01-02")
	}

	var s WindowSummary
	for _, r := range rows {
		if cutoff != "" && r.Dt < cutoff {
			continue
		}
		s.Days++
		s.ActualPts += r.ActualPts
		s.OptimalPts += r.OptimalPts
		s.GapPts += r.Gap
	}
	if s.OptimalPts != 0 {
		s.Efficiency = s.ActualPts / s.OptimalPts
	}
	return s
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/lineupgap/ -v`
Expected: PASS, 7 tests.

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add internal/lineupgap/
git commit -m "feat(lineupgap): durable Lineup Gap Store + pure gap model (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 5: Layout and statestore wiring

**Files:**
- Modify: `internal/statestore/layout/layout.go` (var block ~lines 63-80; `All()` ~lines 86-92)
- Modify: `internal/statestore/layout/layout_test.go` (the `want` list in `TestAll_CoversEveryKnownPrefix`)
- Modify: `internal/statestore/statestore.go` (imports; `artifact` var block lines 42-52; append constructors after `TeamValueReader` at line 145)

**Interfaces:**
- Consumes: `lineupgap.Writer`, `lineupgap.Reader`, `lineupgap.NewWriter`, `lineupgap.NewFileWriter`, `lineupgap.NewReader`, `lineupgap.NewFileReader` (Task 4); `s3ndjson.New`, `pick[T]`.
- Produces: `layout.LineupGaps` artifact; `(*statestore.Selector).LineupGapWriter() (lineupgap.Writer, error)`; `(*statestore.Selector).LineupGapReader() (lineupgap.Reader, error)`.

- [ ] **Step 1: Write the failing test**

In `internal/statestore/layout/layout_test.go`, add `"analysis/lineup-gaps/"` to the `want` slice in `TestAll_CoversEveryKnownPrefix`, immediately after `"analysis/team-values/"`:

```go
	want := []string{
		"cache/",
		"analysis/grades/",
		"analysis/team-values/",
		"analysis/lineup-gaps/",
		"runledger/",
		"runs/",
		"notifications/",
		"lineup/",
		"archive/",
		"backtest/",
		"claims/",
		"session/",
	}
```

Also append a new test to the same file:

```go
// The Lineup Gap Store IS backfillable, unlike Team Values — past-period roster
// snapshots are immutable and cached, so a missed day is recoverable with
// `grade --dates`. Flagging it NoBackfill would make the Infra page rank a
// recoverable gap as permanent data loss.
func TestLineupGaps_IsBackfillable(t *testing.T) {
	for _, a := range All() {
		if a.S3Prefix == "analysis/lineup-gaps/" {
			if a.NoBackfill {
				t.Error("lineup-gaps is recoverable via `grade --dates`; it must not be flagged NoBackfill")
			}
			if !a.Partitioned {
				t.Error("lineup-gaps is dt-partitioned; gap detection needs that flag")
			}
			return
		}
	}
	t.Fatal("lineup-gaps artifact missing from All()")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/statestore/... -v`
Expected: FAIL — `prefix "analysis/lineup-gaps/" is not in All()` and `lineup-gaps artifact missing from All()`.

- [ ] **Step 3: Write the implementation**

**5a.** In `internal/statestore/layout/layout.go`, add to the var block immediately after the `TeamValues` line:

```go
	LineupGaps   = Artifact{Name: "Lineup Gap Store", S3Prefix: "analysis/lineup-gaps/", LocalDir: ".lineupgap", Durable: true, MaxAge: 2 * Day, Producer: "Grade", Partitioned: true}
```

**5b.** In the same file, add `LineupGaps` to `All()`:

```go
func All() []Artifact {
	return []Artifact{
		TeamValues, Analysis, LineupGaps, Archive, Backtest,
		Lineup, RunLedger, RunOutput, Notification, Claims, Session,
		Cache,
	}
}
```

**5c.** In `internal/statestore/statestore.go`, add the import:

```go
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
```

**5d.** In the same file, add to the `artifact` var block after `teamValueArtifact`:

```go
	lineupGapArtifact    = artifact{layout.LineupGaps.S3Prefix, layout.LineupGaps.LocalDir}
```

**5e.** In the same file, append after `TeamValueReader` (ends line 145):

```go
// LineupGapWriter returns the write side of the Lineup Gap Store — S3 when
// STATE_BUCKET is set, else the local .lineupgap directory.
func (s *Selector) LineupGapWriter() (lineupgap.Writer, error) {
	return pick(s, lineupGapArtifact,
		func(ctx context.Context, b, p string) (lineupgap.Writer, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return lineupgap.NewWriter(st), nil
		},
		func(dir string) lineupgap.Writer { return lineupgap.NewFileWriter(dir) })
}

// LineupGapReader returns the read side of the Lineup Gap Store.
func (s *Selector) LineupGapReader() (lineupgap.Reader, error) {
	return pick(s, lineupGapArtifact,
		func(ctx context.Context, b, p string) (lineupgap.Reader, error) {
			st, err := s3ndjson.New(ctx, b, p)
			if err != nil {
				return nil, err
			}
			return lineupgap.NewReader(st), nil
		},
		func(dir string) lineupgap.Reader { return lineupgap.NewFileReader(dir) })
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/statestore/... ./internal/lineupapi/... -v`
Expected: PASS. `internal/lineupapi/infra_test.go:231` compares against `len(layout.All())`, so it adapts automatically.

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add internal/statestore/
git commit -m "feat(statestore): register the Lineup Gap Store in the layout table (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 6: Produce gap rows from the grade command

**Files:**
- Modify: `cmd/grade.go` (imports; insert after the grade-write loop that ends at line 140)

**Interfaces:**
- Consumes: `backtest.RunLineupAnalysis(days []fantrax.DayRoster, hitterSlots, pitcherSlots []fantrax.Slot) []backtest.LineupDayResult`; `backtest.LineupDayResult{Date time.Time; ActualPts, OptimalPts, Gap float64; Started, Benched []backtest.PlayerPts}`; `ft.GetActiveSlots()` (corrected in review — `GetHitterSlots` does not exist; see note below), `ft.GetPitcherSlots()`; `statestore.FromEnv().LineupGapWriter()` (Task 5); `lineupgap.Row` (Task 4).
- Produces: nothing consumed by later Go code. Writes `analysis/lineup-gaps/dt=*/gaps.ndjson`.

- [ ] **Step 1: Write the failing test**

Create `cmd/grade_gap_test.go`:

```go
package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
)

type fakeGapWriter struct {
	rows []lineupgap.Row
	err  error
}

func (f *fakeGapWriter) WriteGaps(date time.Time, rows []lineupgap.Row) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func TestWriteLineupGaps_MapsResultsToRows(t *testing.T) {
	w := &fakeGapWriter{}
	results := []backtest.LineupDayResult{{
		Date:       time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		ActualPts:  90,
		OptimalPts: 100,
		Gap:        -10,
		Started:    []backtest.PlayerPts{{PlayerID: "a"}, {PlayerID: "b"}},
		Benched:    []backtest.PlayerPts{{PlayerID: "c"}},
	}}

	if err := writeLineupGaps(w, results); err != nil {
		t.Fatalf("writeLineupGaps: %v", err)
	}
	if len(w.rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(w.rows))
	}
	got := w.rows[0]
	if got.Dt != "2026-07-20" || got.ActualPts != 90 || got.OptimalPts != 100 || got.Gap != -10 {
		t.Errorf("row mapped wrong: %+v", got)
	}
	if got.StartedN != 2 || got.BenchedN != 1 {
		t.Errorf("counts wrong: StartedN=%d BenchedN=%d, want 2/1", got.StartedN, got.BenchedN)
	}
}

// Grades are the irreplaceable artifact; the gap is recomputable. A gap-write
// failure must never take the grade run down with it.
func TestWriteLineupGaps_ReturnsErrorForCallerToSoftFail(t *testing.T) {
	w := &fakeGapWriter{err: errors.New("s3 exploded")}
	results := []backtest.LineupDayResult{{Date: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}}
	if err := writeLineupGaps(w, results); err == nil {
		t.Error("want an error the caller can log-and-continue on, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/ -run TestWriteLineupGaps -v`
Expected: FAIL — `undefined: writeLineupGaps`.

- [ ] **Step 3: Write the implementation**

In `cmd/grade.go`, add these imports to the existing block:

```go
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
```

At the end of `runGrade`, replace the final `return nil` (line 141) with:

```go
	// The Lineup Gap Store: how the lineup we actually applied scored against
	// the hindsight-optimal one. Computed here because this command already
	// holds `days` and a live client — the read-side (projection-site) has
	// neither.
	//
	// Soft-fail: grades are irreplaceable, and a gap day is recoverable with
	// `grade --dates`, so a gap hiccup must never fail the run.
	if err := recordLineupGaps(ft, days); err != nil {
		fmt.Fprintf(os.Stderr, "warning: lineup gaps not written: %v\n", err)
	}
	return nil
}

// recordLineupGaps grades each day's applied lineup against hindsight and
// persists the result. Slot lists are stableTTL-cached (7d), so this costs one
// cold fetch per week on top of the actuals runGrade already holds.
func recordLineupGaps(ft *fantrax.Client, days []fantrax.DayRoster) error {
	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return fmt.Errorf("get hitter slots: %w", err)
	}
	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return fmt.Errorf("get pitcher slots: %w", err)
	}

	results := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)
	if len(results) == 0 {
		return nil
	}

	w, err := statestore.FromEnv().LineupGapWriter()
	if err != nil {
		return fmt.Errorf("init lineup gap store: %w", err)
	}
	return writeLineupGaps(w, results)
}

// writeLineupGaps maps lineup-grade results to store rows, one partition per
// date. Split from recordLineupGaps so the mapping is testable without a
// Fantrax client.
func writeLineupGaps(w lineupgap.Writer, results []backtest.LineupDayResult) error {
	for _, d := range results {
		dt := d.Date.UTC().Format("2006-01-02")
		row := lineupgap.Row{
			Dt:         dt,
			ActualPts:  d.ActualPts,
			OptimalPts: d.OptimalPts,
			Gap:        d.Gap,
			StartedN:   len(d.Started),
			BenchedN:   len(d.Benched),
		}
		if err := w.WriteGaps(d.Date, []lineupgap.Row{row}); err != nil {
			return fmt.Errorf("write lineup gap %s: %w", dt, err)
		}
		fmt.Printf("wrote lineup gap for %s (actual %.1f, optimal %.1f, gap %.1f)\n",
			dt, d.ActualPts, d.OptimalPts, d.Gap)
	}
	return nil
}
```

Add `"os"` and `"github.com/nixon-commits/rosterbot/internal/fantrax"` to the imports if they are not already present (check the existing block first — `cmd/grade.go` currently imports neither).

> **Corrected in review.** The plan above originally called `ft.GetHitterSlots()`,
> which does not exist on `*fantrax.Client`. The real method is
> `ft.GetActiveSlots()` (`internal/fantrax/client.go:559`), cached under the
> hitter-slots key and already paired with `GetPitcherSlots()` exactly this way
> in `cmd/backtest.go:78-83`. The code block above and the Interfaces line at
> the top of this task reflect the shipped call.

**Dry-run guard:** `runGrade` returns early at line 124 when `cfg.DryRun`, so the gap write is already skipped in dry-run. Confirm that early return is still above the new call; if the code has moved, keep the gap write below it.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/ -run TestWriteLineupGaps -v`
Expected: PASS, 2 tests.

Run: `go build ./... && go test ./cmd/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add cmd/grade.go cmd/grade_gap_test.go
git commit -m "feat(grade): record daily lineup gap alongside graded snapshots (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 7: Publish gap.json

**Files:**
- Modify: `cmd/projection-site.go` (imports; insert the call after `renderViewsSite` at line 89; append the function)

**Interfaces:**
- Consumes: `statestore.FromEnv().LineupGapReader()` (Task 5); `lineupgap.BuildModel` (Task 4).
- Produces: `<out>/gap.json`, published by the existing `cmd/sync.go:96` whole-directory publish of `./report` — **no sync change required**.

- [ ] **Step 1: Write the failing test**

Create `cmd/projection_site_gap_test.go`:

```go
package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupgap"
)

func TestRenderGapSite_WritesModelFromStore(t *testing.T) {
	stateDir := t.TempDir()
	outDir := t.TempDir()

	w := lineupgap.NewFileWriter(stateDir)
	d := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if err := w.WriteGaps(d, []lineupgap.Row{{
		Dt: "2026-07-20", ActualPts: 90, OptimalPts: 100, Gap: -10, StartedN: 13, BenchedN: 2,
	}}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if err := writeGapModel(lineupgap.NewFileReader(stateDir), outDir); err != nil {
		t.Fatalf("writeGapModel: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(outDir, "gap.json"))
	if err != nil {
		t.Fatalf("read gap.json: %v", err)
	}
	var m lineupgap.Model
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("gap.json is not valid JSON: %v", err)
	}
	if m.LatestDate != "2026-07-20" {
		t.Errorf("LatestDate = %q, want 2026-07-20", m.LatestDate)
	}
	if len(m.Series) != 1 || m.Series[0].GapPts != -10 {
		t.Errorf("Series = %+v, want one point with gap -10", m.Series)
	}
}

// A fresh deploy has an empty store. That must still produce a valid file, so
// the dashboard's headline block renders "no data yet" instead of erroring.
func TestRenderGapSite_EmptyStoreStillWritesValidJSON(t *testing.T) {
	outDir := t.TempDir()
	if err := writeGapModel(lineupgap.NewFileReader(t.TempDir()), outDir); err != nil {
		t.Fatalf("writeGapModel on empty store: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(outDir, "gap.json"))
	if err != nil {
		t.Fatalf("read gap.json: %v", err)
	}
	var m lineupgap.Model
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("gap.json is not valid JSON: %v", err)
	}
	if len(m.Series) != 0 {
		t.Errorf("Series = %d points, want 0", len(m.Series))
	}
	if _, ok := m.Windows["30"]; !ok {
		t.Error(`Windows["30"] must be present even when empty`)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/ -run TestRenderGapSite -v`
Expected: FAIL — `undefined: writeGapModel`.

- [ ] **Step 3: Write the implementation**

In `cmd/projection-site.go`, add the import:

```go
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
```

Insert after the `renderViewsSite` block (which ends at line 89), before the `if projSiteOpen` block:

```go
	// Emit gap.json (realized-vs-hindsight lineup gap) on the same terms: its
	// own store, additive, soft-failing so a gap hiccup never blocks the
	// accuracy dashboard deploy.
	if err := renderGapSite(projSiteOut); err != nil {
		fmt.Fprintf(os.Stderr, "warning: gap.json not written: %v\n", err)
	}
```

Append these two functions to the end of the file:

```go
// renderGapSite reads the Lineup Gap Store (S3 when STATE_BUCKET is set, else
// local .lineupgap/) and writes <outDir>/gap.json.
func renderGapSite(outDir string) error {
	reader, err := statestore.FromEnv().LineupGapReader()
	if err != nil {
		return fmt.Errorf("init lineup gap reader: %w", err)
	}
	return writeGapModel(reader, outDir)
}

// writeGapModel is the I/O-light half of renderGapSite, split out so the model
// and file shape are testable against a local store with no environment.
//
// An empty store still writes a valid (empty) model rather than erroring: the
// gap block is the first thing on the Projections tab, and a fresh deploy must
// render "no data yet" rather than a broken headline.
func writeGapModel(reader lineupgap.Reader, outDir string) error {
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read lineup gaps: %w", err)
	}
	m := lineupgap.BuildModel(rows, time.Now().UTC())

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	outPath := filepath.Join(outDir, "gap.json")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("encode gap model: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d lineup-gap days)\n", outPath, len(rows))
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/ -run TestRenderGapSite -v`
Expected: PASS, 2 tests.

- [ ] **Step 5: Commit**

```bash
go vet ./... && go mod tidy
git add cmd/projection-site.go cmd/projection_site_gap_test.go
git commit -m "feat(projection-site): publish gap.json beside model/value/views (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 8: Dashboard headline block

**Files:**
- Modify: `web/dashboard/api.js` (report-JSON block near line 63)
- Modify: `web/dashboard/projections.js` (`renderProjections`, `buildLayout`, `paint`, `renderCompare`, `renderScorecard`)

**Interfaces:**
- Consumes: `gap.json` shaped as `lineupgap.Model` (Task 4/7); `model.json` `views[k].rho` as `RhoStat` and `compare[k][i].rho` (Task 3); `metrics.skillVsMean` (Task 2).
- Produces: no exports beyond the existing `renderProjections(root)`.

There is no JS test harness in this repo, so this task is verified by rendering. Build the model files first, then load the page.

- [ ] **Step 1: Add the API method**

In `web/dashboard/api.js`, add to the report block after `reportViews`:

```js
  reportGap: () => request("GET", "/report/gap.json"),
```

- [ ] **Step 2: Fetch the gap model, tolerating its absence**

In `web/dashboard/projections.js`, replace the body of `renderProjections` between the `model` fetch and the `const systems` line with:

```js
  // gap.json is a separate artifact with its own producer, so its absence is
  // ordinary (local dev, or a fresh deploy before grade next runs). The
  // headline block degrades to rho-only rather than failing the whole tab.
  let gap = null;
  try {
    gap = await api.reportGap();
  } catch {
    gap = null;
  }
```

Then change the `state`/`rerender` wiring to thread `gap` through:

```js
  const state = { window: 30, role: "all", system: defaultSystem };
  const el = buildLayout(root);
  const rerender = () => paint(el, model, gap, state);
  wireToggles(el, model, state, rerender);
  paint(el, model, gap, state);
```

- [ ] **Step 3: Add the headline block to the layout**

In `buildLayout`, insert this markup immediately after the `<div class="controls">` block containing `winseg`/`roleseg`, and **before** the `Projection system comparison` section:

```html
    <section class="card">
      <h2>Decision quality <span class="muted" data-ref="headlineSub"></span></h2>
      <div class="stat-row" data-ref="headline"></div>
      <div class="chart-box"><canvas data-ref="gapChart"></canvas></div>
    </section>
```

- [ ] **Step 4: Render the headline tiles**

Add these functions to `projections.js` (place them above the `// ---- Scorecard` divider):

```js
// ---- Headline: decision quality -------------------------------------------

// rhoTile formats a RhoStat from internal/report. A nil rho (too few
// qualifying days, or role "all" where pooling is undefined) renders as "—"
// rather than a misleading 0.
function rhoTile(label, rs) {
  if (!rs || !rs.days) {
    return tile(label, "—", `<div class="delta flat">not enough graded days</div>`);
  }
  const sign = rs.rho >= 0 ? "+" : "";
  const value = `${sign}${fmt(rs.rho, 3)}`;
  const sub = `±${fmt(rs.se, 3)} · ${rs.daysPositive}/${rs.days} days positive`;
  return tile(label, value, `<div class="delta flat">${sub}</div>`);
}

function renderHeadline(el, model, gap, state) {
  const w = String(state.window);
  const win = gap && gap.windows ? gap.windows[w] : null;

  // Rho is only defined within a role, so the headline always shows both —
  // never a pooled number. Read the two role views directly rather than
  // whatever the role toggle currently says.
  const hv = (model.views && model.views[model.detailSystem + "|" + state.window + "|hitters"]) || EMPTY_VIEW;
  const pv = (model.views && model.views[model.detailSystem + "|" + state.window + "|pitchers"]) || EMPTY_VIEW;

  let tiles = "";
  if (win && win.days) {
    const gapSign = win.gapPts > 0 ? "+" : "";
    tiles += tile("Points left on bench", `${gapSign}${fmt(win.gapPts, 1)}`,
      `<div class="delta flat">over ${win.days} d</div>`);
    tiles += tile("Lineup efficiency", `${fmt(win.efficiency * 100, 1)}%`,
      `<div class="delta flat">${fmt(win.actualPts, 0)} of ${fmt(win.optimalPts, 0)} pts</div>`);
  } else {
    tiles += tile("Points left on bench", "—",
      `<div class="delta flat">no lineup gap data yet</div>`);
  }
  tiles += rhoTile("Hitter rank skill", hv.rho);
  tiles += rhoTile("Pitcher rank skill", pv.rho);

  el.headline.innerHTML = tiles;
  el.headlineSub.textContent =
    "· " + winLabel(state.window) + " · rank skill is measured within each day, never pooled across roles";

  renderGapChart(el, gap, state);
}

function renderGapChart(el, gap, state) {
  destroy(el, "gapChart");
  if (!gap || !gap.series || !gap.series.length) return;

  // Trim the series to the selected window; season (0) shows everything.
  let pts = gap.series;
  if (state.window > 0) pts = pts.slice(-state.window);

  const t = themeColors();
  el.charts.gapChart = barChart(el.gapChart, {
    data: {
      labels: pts.map((p) => p.dt),
      datasets: [{
        label: "Gap (pts)",
        data: pts.map((p) => p.gapPts),
        borderColor: t.palette[0],
        backgroundColor: t.palette[0],
      }],
    },
    options: {
      interaction: { mode: "index", intersect: false },
      plugins: {
        tooltip: {
          callbacks: {
            label: (ctx) => {
              const p = pts[ctx.dataIndex];
              return `gap ${fmt(p.gapPts, 1)} · actual ${fmt(p.actualPts, 1)} of ${fmt(p.optimalPts, 1)}`;
            },
          },
        },
      },
    },
  });
}
```

- [ ] **Step 5: Call it from paint, and demote MAE**

Change `paint`'s signature and add the headline call:

```js
function paint(el, model, gap, state) {
  el.meta.textContent =
    "Latest graded: " + model.latestDate + " · season since " + model.seasonStart + " · generated " + model.generatedAt;

  paintSegActive(el.winseg, state, "window");
  paintSegActive(el.roleseg, state, "role");
  paintSegActive(el.sysseg, state, "system");

  renderHeadline(el, model, gap, state);
  renderCompare(el, model, state);
  // ... rest unchanged
```

Replace `renderScorecard` so MAE reads as a diagnostic and carries its skill score:

```js
function renderScorecard(el, view) {
  const s = view.scorecard || {};
  const c = s.cur || {};
  const p = s.prior || {};
  if (!c.n) {
    el.scorecard.innerHTML = `<p class="muted">No graded data in this window yet.</p>`;
    return;
  }
  // MAE at player-day grain is dominated by single-game variance, so it sits
  // here as a calibration check rather than a scoreboard — the skill score
  // under it says so numerically, and updates daily. The headline metrics are
  // in the Decision quality block above.
  const skill = c.skillVsMean || 0;
  const skillSign = skill >= 0 ? "+" : "";
  const skillCls = skill > 0 ? "good" : (skill < 0 ? "bad" : "flat");
  el.scorecard.innerHTML =
    tile("MAE", fmt(c.mae),
      `<div class="delta ${skillCls}">${skillSign}${fmt(skill * 100, 1)}% vs constant baseline</div>`) +
    tile("Bias", fmt(c.bias), deltaCell(Math.abs(c.bias), Math.abs(p.bias), true)) +
    tile("RMSE", fmt(c.rmse), deltaCell(c.rmse, p.rmse, true)) +
    tile("Sample (player-days)", (c.n || 0).toLocaleString(), `<div class="delta flat">prior ${(p.n || 0).toLocaleString()}</div>`);
}
```

In `buildLayout`, change the detail scorecard heading area — replace the line `<div class="stat-row" data-ref="scorecard"></div>` with:

```html
    <h3>Calibration diagnostics</h3>
    <p class="muted">Error magnitude at player-day grain is mostly single-game variance, not model skill. Kept for calibration; see Decision quality above for the metrics that drive lineups.</p>
    <div class="stat-row" data-ref="scorecard"></div>
```

- [ ] **Step 6: Handle the two-table comparison under "All"**

Replace the opening of `renderCompare` with:

```js
function renderCompare(el, model, state) {
  // Compare is nil for role "all" — a pooled cross-role ranking has no
  // defensible order (see internal/report/model.go), so render the two role
  // tables stacked instead.
  if (state.role === "all") {
    renderCompareTable(el, model, state, "hitters");
    renderCompareTable(el, model, state, "pitchers", true);
    renderCompareChart(el, model, state);
    el.compareSub.textContent = "· ranked by within-day rank skill · " + winLabel(state.window);
    return;
  }
  el.compareTable.innerHTML = "";
  renderCompareTable(el, model, state, state.role);
  renderCompareChart(el, model, state);
  el.compareSub.textContent =
    "· ranked by within-day rank skill (higher = better) · " + winLabel(state.window) + " · " + state.role;
}

// renderCompareTable appends one role's ranked table. append=false resets the
// container first, so the "all" path can stack two.
function renderCompareTable(el, model, state, role, append = false) {
  if (!append) el.compareTable.innerHTML = "";
  const scores = (model.compare && model.compare[state.window + "|" + role]) || [];
  const withData = scores.filter((s) => s.n > 0);

  const t = themeColors();
  const systems = model.systems || [];
  const colorFor = (sys) => t.palette[Math.max(0, systems.indexOf(sys)) % t.palette.length];

  const heading = `<h3>${roleLabel(role)}</h3>`;
  if (!withData.length) {
    el.compareTable.insertAdjacentHTML("beforeend",
      heading + `<p class="muted">No graded data across systems in this window yet.</p>`);
    return;
  }

  const anyBest = withData.some((s) => s.best);
  const head = `<tr><th>System</th><th class="num">Rank skill</th><th class="num">MAE</th><th class="num">Bias</th><th class="num">RMSE</th><th class="num">Sample</th></tr>`;
  const body = scores.map((s) => {
    if (s.n === 0) return "";
    const prod = s.system === model.detailSystem ? ` <span class="badge">prod</span>` : "";
    const best = s.best ? ` <span class="badge">best</span>` : "";
    const sw = `<span class="swatch" style="background:${colorFor(s.system)}"></span>`;
    const rho = s.rho && s.rho.days
      ? `${s.rho.rho >= 0 ? "+" : ""}${fmt(s.rho.rho, 3)} <span class="muted">±${fmt(s.rho.se, 3)}</span>`
      : "—";
    return `<tr class="${s.best ? "best" : ""}"><td>${sw}${escapeHtml(sysLabel(s.system))}${prod}${best}</td>` +
      `<td class="num">${rho}</td>` +
      `<td class="num">${fmt(s.mae, 2)}</td>` +
      `<td class="num">${s.bias > 0 ? "+" : ""}${fmt(s.bias, 2)}</td>` +
      `<td class="num">${fmt(s.rmse, 2)}</td>` +
      `<td class="num">${s.n.toLocaleString()}</td></tr>`;
  }).join("");

  // No system separated from the runner-up by more than the combined standard
  // error, so naming a winner would be reporting noise.
  const note = anyBest ? "" : `<p class="muted">Too close to call — no system leads by more than the combined standard error.</p>`;
  el.compareTable.insertAdjacentHTML("beforeend",
    heading + `<table class="data-table"><thead>${head}</thead><tbody>${body}</tbody></table>` + note);
}
```

Then rename the existing chart half of the old `renderCompare` into `renderCompareChart(el, model, state)` — take everything from the `// Overlaid MAE trend` comment to the end of the old function body verbatim, and change its first line to use `const key = compareKey(state);` so `compareTrends` (still populated for all three roles) resolves.

- [ ] **Step 7: Verify by rendering**

Build the JSON locally and serve the SPA:

```bash
go build -o rosterbot . && ./rosterbot projection-site --out /tmp/rbreport
ls -la /tmp/rbreport/   # expect model.json, value.json, gap.json
python3 -m json.tool /tmp/rbreport/gap.json | head -30
```

Then check that `model.json` carries the new fields:

```bash
python3 -c "
import json
m = json.load(open('/tmp/rbreport/model.json'))
v = m['views'][m['detailSystem'] + '|30|hitters']
print('hitter rho:', v.get('rho'))
print('skillVsMean:', v['scorecard']['cur'].get('skillVsMean'))
print('compare[30|all]:', m['compare'].get('30|all'))
print('compare[30|hitters][0]:', m['compare']['30|hitters'][0])
"
```

Expected: a non-null `rho` object with `rho`/`se`/`days`/`daysPositive`; a `skillVsMean` near `-0.005`; `compare[30|all]` is `None`; the hitters entry carries a `rho`.

Serve and eyeball the page (the dashboard is passkey-gated, so use the local `serve` path):

```bash
./rosterbot serve --web
```

Confirm on the Projections tab: the Decision quality block renders first with four tiles; the two rho tiles show `±se` and days-positive; switching the window toggle updates gap and rho together; under role All the comparison shows two stacked tables; the Calibration diagnostics heading and its explanatory line appear above the MAE tiles; the MAE tile shows the skill percentage. Check light and dark, and at 390px width.

- [ ] **Step 8: Commit**

```bash
git add web/dashboard/api.js web/dashboard/projections.js
git commit -m "feat(dashboard): lead with rank skill and lineup gap, demote MAE (rosterbot-aei)

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"
```

---

## Task 9: Reproduction gate, docs, and smoke test

**Files:**
- Create: `internal/report/reproduce_test.go` (temporary — deleted in step 3)
- Modify: `CLAUDE.md` (the `internal/report` paragraph ~line 159; add a `internal/lineupgap` paragraph after `internal/valuereport` ~line 163)
- Modify: `README.md` (dashboard/report section ~line 309)

**Interfaces:**
- Consumes: everything from Tasks 1-8.
- Produces: verified, documented feature.

- [ ] **Step 1: Run the reproduction gate**

The spec's central claim is that the new code reproduces the triage measurement. Verify it against real graded rows.

Fetch the production grade rows into a local `.analysis/` if not already present (this needs AWS credentials; skip to step 2 with an explicit note if unavailable):

```bash
aws s3 sync "s3://$STATE_BUCKET/analysis/grades/" .analysis/grades/ --region us-west-1
```

Create `internal/report/reproduce_test.go`:

```go
//go:build reproduce

package report

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

// TestReproduceTriageMeasurement checks the implementation against the numbers
// independently reproduced twice during rosterbot-aei triage. A mismatch means
// the implementation is wrong — most likely the tie correction in spearman.
//
// Build-tagged: it reads a real .analysis/ directory and is not part of the
// normal suite. Run with:
//   go test -tags reproduce ./internal/report/ -run TestReproduce -v
func TestReproduceTriageMeasurement(t *testing.T) {
	rows, err := analysis.NewFileReader(".analysis").ReadAll()
	if err != nil {
		t.Skipf("no local Analysis Store: %v", err)
	}
	if len(rows) == 0 {
		t.Skip("no graded rows available")
	}
	rows = normalizeSystems(rows)
	prod := filterSystem(rows, detailSystem)

	h := withinDayRho(filterRole(prod, "hitters"))
	p := withinDayRho(filterRole(prod, "pitchers"))
	t.Logf("rows=%d hitters=%+v pitchers=%+v", len(prod), h, p)

	if h == nil || p == nil {
		t.Fatal("expected both roles to produce a RhoStat")
	}
	// Tolerances are loose: the store has grown since triage, so the sample is
	// not identical. The point is to catch a sign error or a broken tie
	// correction, not to pin three decimals.
	if h.Rho < 0.05 || h.Rho > 0.20 {
		t.Errorf("hitter rho = %.4f, want ~+0.1218 (triage) — check the tie correction", h.Rho)
	}
	if p.Rho < 0.30 || p.Rho > 0.55 {
		t.Errorf("pitcher rho = %.4f, want ~+0.4292 (triage) — check the tie correction", p.Rho)
	}
}
```

Run: `go test -tags reproduce ./internal/report/ -run TestReproduce -v`
Expected: PASS, with a log line showing hitter rho near +0.12 and pitcher rho near +0.43.

**If it fails**, do not proceed. The most likely causes, in order: (a) `spearman` is using the no-ties shortcut; (b) `withinDayRho` is being handed an unfiltered (pooled) slice; (c) `minDayRows` is excluding too many days. Fix and re-run.

- [ ] **Step 2: Record the measured values**

Note the actual measured rho values from step 1 — they go in the commit message and the bd close reason as evidence.

- [ ] **Step 3: Delete the temporary test**

```bash
rm internal/report/reproduce_test.go
```

It reads a real store and cannot run in CI; its job was the one-time gate.

- [ ] **Step 4: Update CLAUDE.md**

In the `internal/report` paragraph (~line 159), replace the sentence describing `Compare` ranking:

> `Compare` (keyed `"window|role"` → `[]SystemScore` ranked by MAE ascending, lowest-MAE-with-data flagged `Best`)

with:

> `Compare` (keyed `"window|role"` → `[]SystemScore` ranked by **within-day Spearman rho descending**; `Best` is flagged only when the leader beats the runner-up by more than the combined standard error, otherwise the panel reports *too close to call*. **`Compare["<w>|all"]` is nil** — a pooled cross-role ranking has no defensible order, so the panel stacks the hitters and pitchers tables instead)

Then append to the same paragraph:

> **Rank skill is the headline metric** (`internal/report/rank.go`, rosterbot-aei). MAE at player-day grain is near-blind to model skill: measured over 850 production rows, hitter MAE 4.380 *loses* to a constant-at-sample-mean baseline of 4.356 (−0.5% skill) because projections barely vary (sd 0.66) against daily outcome variance (sd 5.52). What the model does have is rank skill — exactly what a lineup optimiser consumes — so `RhoStat` carries the mean of per-day Spearman correlations plus its standard error, day count and days-positive. Two rules are load-bearing: **Spearman is tie-corrected** (average ranks, Pearson over ranks) because every player who didn't play sits at `actual = 0` and the `1 - 6Σd²/(n(n²-1))` shortcut is invalid under ties; and **rho is never pooled across roles** — `View.Rho` and `SystemScore.Rho` are `*RhoStat` and nil for `role == "all"`, since a pooled number would reward knowing only that starting pitchers outscore bench catchers, which the optimiser never uses (it runs the two roles in separate passes). `Metrics.SkillVsMean` (`1 - MAE/MAE_constantAtMean`) renders under the MAE tile so the demotion stays a daily-recomputed fact rather than an assertion.

Add a new paragraph after the `internal/valuereport` paragraph:

> **`internal/lineupgap`** — the durable **Lineup Gap Store**: how each day's applied lineup scored against the hindsight-optimal one, as date-partitioned NDJSON (`dt=YYYY-MM-DD/gaps.ndjson`) over `internal/ndjsonstore`, plus its own pure `BuildModel` (following `internal/recaplog`'s store-plus-model shape rather than the `teamvalue`/`valuereport` split, since the model is a few dozen lines of windowed sums). `Row` carries actual/optimal/gap points and started/benched counts; `Efficiency()` is derived. **There is no system dimension** — exactly one lineup was applied each day and hindsight-optimal doesn't depend on a projection system — so the partition key is `dt=` alone and the entity lives in the prefix (`analysis/lineup-gaps/` ↔ `.lineupgap`), mirroring `teamvalue`. The consequence: rho answers *which projection system*, the gap answers *what the whole pipeline costs*; they are complementary, not comparable. The producer is `cmd/grade.go`, which already holds the `DailyFantasyPoints` result and a live client (`projection-site` has neither), and calls the existing `backtest.RunLineupAnalysis`; the write **soft-fails** so a gap hiccup never takes down the irreplaceable grades. Unlike the Team Value Store it is `NoBackfill: false` — past-period roster snapshots are immutable and cached, so a missed day is recoverable with `grade --dates`. `cmd/projection-site.go`'s `renderGapSite` writes `gap.json` beside `model.json`/`value.json`/`views.json`, soft-failing independently; the SPA's Projections tab leads with it.

- [ ] **Step 5: Update README.md**

At line 309, change `(report/model.json + report/value.json)` to `(report/model.json + report/value.json + report/views.json + report/gap.json)`.

Then add to the dashboard-features description:

> The **Projections** tab leads with a *Decision quality* block: points left on the bench versus the hindsight-optimal lineup, lineup efficiency, and within-day rank skill (mean Spearman rho, with standard error and days-positive) for hitters and pitchers separately. MAE, bias and RMSE remain below as calibration diagnostics, each MAE figure annotated with its skill score against a constant-at-sample-mean baseline.

- [ ] **Step 6: Run the full smoke test**

```bash
go vet ./... && go mod tidy && make test
```
Expected: PASS.

```bash
make build && make run-all
```
Expected: every command completes; `grade` prints `wrote lineup gap for <date> ...` lines.

- [ ] **Step 7: Commit and push**

```bash
git add CLAUDE.md README.md
git commit -m "docs: rank-skill headline and the Lineup Gap Store (rosterbot-aei)

Reproduction gate passed against the production Analysis Store:
hitter rho <MEASURED>, pitcher rho <MEASURED> — consistent with the
+0.1218 / +0.4292 measured independently during triage.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01WUUjcdmPD5H8sbznmzfLCM"

git pull --rebase
git push -u origin feat/rosterbot-aei-rank-skill-headline
git status   # MUST show "up to date with origin"
```

Replace `<MEASURED>` with the real values from step 2.

- [ ] **Step 8: Close the issue**

```bash
bd close rosterbot-aei --reason="Shipped on feat/rosterbot-aei-rank-skill-headline. Dashboard now leads with within-day Spearman rho by role (tie-corrected, never pooled across roles, ±se and days-positive shown) and the realized-vs-hindsight lineup gap from the new durable Lineup Gap Store (internal/lineupgap, produced by grade, published as gap.json). MAE demoted to a Calibration diagnostics section carrying Metrics.SkillVsMean against a constant-at-sample-mean baseline. System comparison now ranks by rho with a combined-SE too-close-to-call gate. Reproduction gate passed: hitter rho <MEASURED>, pitcher rho <MEASURED>."
git add .beads/ && git commit -m "chore: bd export — close rosterbot-aei" && git push
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| §1.1 tie-corrected Spearman | 1 |
| §1.2 minimum day size | 1 |
| §1.3 never pooled across roles | 1 (guard), 3 (nil wiring) |
| §1.4 Metrics unchanged | 2 (additive field only) |
| §1.5 skill score on MAE | 2 |
| §1.6 ranking by rho + Best gate + Compare nil for "all" | 3 |
| §2.1-2.2 package shape, no system dimension | 4 |
| §2.3 producer in grade | 6 |
| §2.4 soft failure policy | 6 |
| §2.5 layout, statestore, NoBackfill:false, no sync change | 5 (7 confirms no sync change) |
| §3.1 gap.json | 7 |
| §3.2 headline block | 8 |
| §3.3 empty state | 7 (valid empty model), 8 (UI fallback) |
| §3.4 MAE demotion | 8 |
| §4 testing | every task |
| §4.1 reproduction gate | 9 |

No gaps.

**Type consistency check:** `RhoStat` fields (`Rho`, `SE`, `Days`, `DaysPositive`) are used identically in Tasks 1, 3, and 8 (JS reads `rho`, `se`, `days`, `daysPositive` — matching the JSON tags). `lineupgap.Row` fields match between Tasks 4, 6, and 7. `WindowSummary` fields (`days`, `actualPts`, `optimalPts`, `gapPts`, `efficiency`) match between Task 4's Go tags and Task 8's JS reads. `writeLineupGaps` / `recordLineupGaps` / `writeGapModel` / `renderGapSite` are each defined once and referenced consistently.

**Known risk flagged in Task 9:** the reproduction gate needs AWS credentials and a synced `.analysis/`. If unavailable, the gate cannot run and the tie correction is verified only by the hand-computed unit test in Task 1 — which is a real but weaker check. Say so explicitly rather than claiming the gate passed.

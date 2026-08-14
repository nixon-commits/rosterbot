// Package report aggregates the durable Graded Snapshots (analysis.GradeRow)
// into a compact Model of precomputed views (per timeframe x role) for the
// projection-accuracy dashboard. Pure: no I/O.
package report

import (
	"math"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

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

// PositionRow is per-bucket accuracy.
type PositionRow struct {
	Bucket string  `json:"bucket"`
	MAE    float64 `json:"mae"`
	Bias   float64 `json:"bias"`
	N      int     `json:"n"`
}

// CalibPoint is a calibration bin: mean projected vs mean actual.
type CalibPoint struct {
	Proj   float64 `json:"proj"`
	Actual float64 `json:"actual"`
	N      int     `json:"n"`
}

// Miss is one large projection error (player-day).
type Miss struct {
	Date      string  `json:"date"`
	PlayerID  string  `json:"playerID"`
	Name      string  `json:"name"`
	MLBTeam   string  `json:"mlbTeam"`
	Bucket    string  `json:"bucket"`
	IsPitcher bool    `json:"isPitcher"`
	Projected float64 `json:"projected"`
	Actual    float64 `json:"actual"`
	Diff      float64 `json:"diff"`
}

var (
	hitterBuckets  = []string{"C", "INF", "OF", "UT"}
	pitcherBuckets = []string{"SP", "RP"}
	calibEdges     = []float64{0, 2, 4, 6, 8, 10, 12, 15, 20} // last bin = [20, +inf)
)

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

	// TotalN is the system's own row count in the window, before pairing.
	// Every other field is computed on the common player-days shared with the
	// systems it is compared against, so TotalN - N is what the pairing
	// discarded. It is reported rather than dropped because a comparison that
	// silently throws away half a system's coverage repeats, one level down,
	// the invisibility that made the unpaired ranking wrong.
	TotalN int `json:"totalN"`
}

// normalizeSystems returns a copy of rows with any empty System attributed to
// detailSystem (the production system / legacy attribution), so un-attributed
// input still feeds the detail dashboard. Non-empty systems pass through.
func normalizeSystems(rows []analysis.GradeRow) []analysis.GradeRow {
	out := make([]analysis.GradeRow, len(rows))
	copy(out, rows)
	for i := range out {
		if out[i].System == "" {
			out[i].System = detailSystem
		}
	}
	return out
}

// distinctSystems returns the sorted set of projection systems present in rows.
func distinctSystems(rows []analysis.GradeRow) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		if r.System != "" {
			seen[r.System] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// dropLegacy removes rows read from pre-migration partitions.
//
// They are kept in the detail Views — they are the only record of the
// pre-migration season — but must not enter a head-to-head ranking. They
// carry System == LegacySystem == "depthcharts-ros", so pooled they credit
// the incumbent with days recorded before any challenger existed: measured in
// production 2026-08-14, 14 such days at MAE 4.8915 (n=298) against the real
// system's 5.1007, dragging the reported figure to 5.0457.
func dropLegacy(rows []analysis.GradeRow) []analysis.GradeRow {
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if !r.Legacy {
			out = append(out, r)
		}
	}
	return out
}

func filterSystem(rows []analysis.GradeRow, system string) []analysis.GradeRow {
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.System == system {
			out = append(out, r)
		}
	}
	return out
}

// playerDay is the join key of the paired comparison: one player on one date.
type playerDay struct{ dt, playerID string }

// commonPlayerDays returns the (date, player) keys every COMPETING system
// graded — the only ground on which a head-to-head comparison is honest.
//
// A system with no rows in the window is not competing, and is skipped rather
// than intersected: intersecting against an empty set would erase the
// comparison instead of narrowing it, so one system that has not started
// capturing yet would blank the whole panel.
func commonPlayerDays(systems []string, slices map[string][]analysis.GradeRow) map[playerDay]bool {
	var common map[playerDay]bool
	for _, sys := range systems {
		rs := slices[sys]
		if len(rs) == 0 {
			continue
		}
		seen := make(map[playerDay]bool, len(rs))
		for _, r := range rs {
			seen[playerDay{r.Dt, r.PlayerID}] = true
		}
		if common == nil {
			common = seen
			continue
		}
		for k := range common {
			if !seen[k] {
				delete(common, k)
			}
		}
	}
	return common
}

func filterPlayerDays(rows []analysis.GradeRow, keep map[playerDay]bool) []analysis.GradeRow {
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if keep[playerDay{r.Dt, r.PlayerID}] {
			out = append(out, r)
		}
	}
	return out
}

// rankSystems scores each system over the window×role slice and returns them
// ordered by within-day rank skill descending (best first).
//
// Every score is computed on the INTERSECTION of (date, player) keys across
// the competing systems, so no system is credited with days its rivals never
// had a chance to compete on. Without the pairing the panel compared
// unequal samples and the wider-covered system won on coverage rather than
// skill: measured in production 2026-08-14, depthcharts-ros posted pitcher
// rho +0.3323 over 29 days against rivals' 19, and once paired fell to
// +0.1555 and from first place to last. Each system's own pre-pairing count
// survives as TotalN so the discarded rows stay visible.
//
// Legacy rows are excluded upstream (see Aggregate): they predate every
// challenger, so pooling them into the incumbent credited it with 14 days
// nobody else ran on.
//
// Callers pass a single role — never "all". Systems with no rows in the window
// sort last (N 0) and are never marked Best.
func rankSystems(rows []analysis.GradeRow, systems []string, latest time.Time, window int, role string) []SystemScore {
	slices := make(map[string][]analysis.GradeRow, len(systems))
	for _, sys := range systems {
		slices[sys] = windowRows(filterRole(filterSystem(rows, sys), role), latest, window)
	}
	common := commonPlayerDays(systems, slices)

	out := make([]SystemScore, 0, len(systems))
	for _, sys := range systems {
		full := slices[sys]
		slice := filterPlayerDays(full, common)
		m := computeMetrics(slice)
		out = append(out, SystemScore{
			System: sys, MAE: m.MAE, Bias: m.Bias, RMSE: m.RMSE, N: m.N,
			Rho: withinDayRho(slice), TotalN: len(full),
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

// minCompareDays is the smallest number of common graded days a system needs
// before it can be crowned Best.
//
// It exists because the combined-SE gate INVERTS on a thin sample rather than
// tightening. withinDayRho can only compute a standard deviation from two or
// more daily correlations (rank.go), so a one-day sample leaves SE at 0; the
// separation term is then sqrt(0+0) = 0 and every difference, however small,
// clears it. Reproduced against the production Analysis Store on 2026-08-14:
// restricted to a single day the gate crowned steamer-ros at rho +0.0000 over
// atc-ros at -0.6000 — a system with no measured skill at all, wearing the
// "best" badge. The gate built to prevent over-precision was at its most
// permissive exactly where the sample was thinnest.
//
// 10 is a FLOOR, not a fitted optimum. The standard error the gate divides by
// is itself estimated from d days, with relative uncertainty ~1/sqrt(2(d-1)):
// +/-100% at 2 days, +/-50% at 3, +/-35% at 5, +/-24% at 10. Ten is where the
// quantity doing the gating is known well enough to gate with. It does not
// bind on current data — paired coverage on 2026-08-14 is 35 common days for
// hitters and 19 for pitchers — so this changes no verdict today and exists to
// catch the thin-sample band, whether that arrives through a new system, a
// short window, or an outage thinning the intersection.
const minCompareDays = 10

// flagBest marks the leader only when its rho beats the runner-up's by more
// than the combined standard error, on a common sample large enough for that
// standard error to mean anything. Anything closer is indistinguishable from
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
//
// The minCompareDays floor applies to the leader BEFORE the unopposed branch:
// winning by default still requires a sample worth reporting.
func flagBest(out []SystemScore) {
	if len(out) == 0 || out[0].N == 0 || out[0].Rho == nil || out[0].Rho.Days < minCompareDays {
		return
	}
	if len(out) == 1 || out[1].N == 0 {
		out[0].Best = true
		return
	}
	if out[1].Rho == nil || out[1].Rho.Days < minCompareDays {
		return
	}
	lead, next := out[0].Rho, out[1].Rho
	sep := math.Sqrt(lead.SE*lead.SE + next.SE*next.SE)
	if lead.Rho-next.Rho > sep {
		out[0].Best = true
	}
}

func filterRole(rows []analysis.GradeRow, role string) []analysis.GradeRow {
	if role == "all" {
		return rows
	}
	want := role == "pitchers"
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.IsPitcher == want {
			out = append(out, r)
		}
	}
	return out
}

// windowRows returns rows in the last `window` days ending at latest (inclusive).
// window <= 0 returns all rows (season). ISO date strings sort lexicographically,
// so string comparison is correct.
func windowRows(rows []analysis.GradeRow, latest time.Time, window int) []analysis.GradeRow {
	if window <= 0 {
		return rows
	}
	cutoff := latest.AddDate(0, 0, -(window - 1)).Format("2006-01-02")
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.Dt >= cutoff {
			out = append(out, r)
		}
	}
	return out
}

// windowTrend builds the trend series the chart plots for a given window.
// w>0: one point per day across the last w graded days (daily metric), so the
// x-axis spans exactly the window. w<=0 (Season): rolling-7 over the whole
// season, a denoised season-long view.
func windowTrend(rows []analysis.GradeRow, latest time.Time, w int) []TrendPoint {
	if w <= 0 {
		return rollingTrend(rows, 7)
	}
	return rollingTrend(windowRows(rows, latest, w), 1)
}

// priorWindowRows returns the equal-length window immediately before the current
// one. Returns nil for the season window (no prior).
func priorWindowRows(rows []analysis.GradeRow, latest time.Time, window int) []analysis.GradeRow {
	if window <= 0 {
		return nil
	}
	hi := latest.AddDate(0, 0, -window).Format("2006-01-02")
	lo := latest.AddDate(0, 0, -(2*window - 1)).Format("2006-01-02")
	out := make([]analysis.GradeRow, 0, len(rows))
	for _, r := range rows {
		if r.Dt >= lo && r.Dt <= hi {
			out = append(out, r)
		}
	}
	return out
}

func byPosition(rows []analysis.GradeRow) []PositionRow {
	groups := map[string][]analysis.GradeRow{}
	for _, r := range rows {
		groups[r.Bucket] = append(groups[r.Bucket], r)
	}
	order := append(append([]string{}, hitterBuckets...), pitcherBuckets...)
	var out []PositionRow
	for _, b := range order {
		g, ok := groups[b]
		if !ok {
			continue
		}
		m := computeMetrics(g)
		out = append(out, PositionRow{Bucket: b, MAE: m.MAE, Bias: m.Bias, N: m.N})
	}
	return out
}

func calibBinIndex(p float64) int {
	for i := len(calibEdges) - 1; i >= 0; i-- {
		if p >= calibEdges[i] {
			return i
		}
	}
	return 0
}

func calibration(rows []analysis.GradeRow) []CalibPoint {
	type acc struct {
		sumP, sumA float64
		n          int
	}
	bins := make([]acc, len(calibEdges))
	for _, r := range rows {
		i := calibBinIndex(r.Projected)
		bins[i].sumP += r.Projected
		bins[i].sumA += r.Actual
		bins[i].n++
	}
	var out []CalibPoint
	for _, b := range bins {
		if b.n == 0 {
			continue
		}
		out = append(out, CalibPoint{Proj: b.sumP / float64(b.n), Actual: b.sumA / float64(b.n), N: b.n})
	}
	return out
}

func worstMisses(rows []analysis.GradeRow, n int) []Miss {
	sorted := make([]analysis.GradeRow, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		ai, aj := math.Abs(sorted[i].Diff), math.Abs(sorted[j].Diff)
		if ai != aj {
			return ai > aj
		}
		return sorted[i].PlayerID < sorted[j].PlayerID // stable tiebreak
	})
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	out := make([]Miss, 0, len(sorted))
	for _, r := range sorted {
		out = append(out, Miss{
			Date: r.Dt, PlayerID: r.PlayerID, Name: r.Name, MLBTeam: r.MLBTeam,
			Bucket: r.Bucket, IsPitcher: r.IsPitcher,
			Projected: r.Projected, Actual: r.Actual, Diff: r.Diff,
		})
	}
	return out
}

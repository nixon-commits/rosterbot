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
type Metrics struct {
	MAE  float64 `json:"mae"`
	Bias float64 `json:"bias"` // mean(actual - projected); positive = under-projecting
	RMSE float64 `json:"rmse"`
	N    int     `json:"n"`
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
	return Metrics{MAE: sumAbs / n, Bias: sumSigned / n, RMSE: math.Sqrt(sumSq / n), N: len(rows)}
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

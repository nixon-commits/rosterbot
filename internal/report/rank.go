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
// correlating. A day with 3 rows yields a rho of +/-1 that is pure noise.
const minDayRows = 5

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

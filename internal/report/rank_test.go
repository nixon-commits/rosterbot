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
//
//	mean(xr)=2.5, mean(yr)=2.5
//	cov terms: (-1.5)(-1)+(-0.5)(-1)+(0.5)(1)+(1.5)(1) = 1.5+0.5+0.5+1.5 = 4
//	sxx = 2.25+0.25+0.25+2.25 = 5 ; syy = 1+1+1+1 = 4
//	rho = 4 / sqrt(5*4) = 4/sqrt(20) = 0.894427190999916
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
	// 4 rows — below minDayRows(5), must be dropped.
	rows = append(rows, gradeDay("2026-06-01",
		[2]float64{1, 1}, [2]float64{2, 2}, [2]float64{3, 3}, [2]float64{4, 4})...)
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

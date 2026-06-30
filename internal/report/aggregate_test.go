// internal/report/aggregate_test.go
package report

import (
	"math"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeMetrics(t *testing.T) {
	rows := []analysis.GradeRow{
		{Diff: 2}, {Diff: -2}, {Diff: 4},
	}
	m := computeMetrics(rows)
	if m.N != 3 || !approx(m.MAE, (2+2+4)/3.0) || !approx(m.Bias, (2-2+4)/3.0) {
		t.Fatalf("metrics: %+v", m)
	}
	if !approx(m.RMSE, math.Sqrt((4+4+16)/3.0)) {
		t.Fatalf("rmse: %v", m.RMSE)
	}
	if z := computeMetrics(nil); z.N != 0 || z.MAE != 0 {
		t.Fatalf("empty metrics not zero: %+v", z)
	}
}

func TestFilterRole(t *testing.T) {
	rows := []analysis.GradeRow{{PlayerID: "h", IsPitcher: false}, {PlayerID: "p", IsPitcher: true}}
	if got := filterRole(rows, "all"); len(got) != 2 {
		t.Fatalf("all: %d", len(got))
	}
	if got := filterRole(rows, "hitters"); len(got) != 1 || got[0].PlayerID != "h" {
		t.Fatalf("hitters: %+v", got)
	}
	if got := filterRole(rows, "pitchers"); len(got) != 1 || got[0].PlayerID != "p" {
		t.Fatalf("pitchers: %+v", got)
	}
}

func TestWindowRows(t *testing.T) {
	latest := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{Dt: "2026-06-10"}, {Dt: "2026-06-13"}, {Dt: "2026-06-14"}, {Dt: "2026-06-15"},
	}
	// window 3 = [06-13, 06-15]
	if got := windowRows(rows, latest, 3); len(got) != 3 {
		t.Fatalf("window 3: %d", len(got))
	}
	// season (0) = all
	if got := windowRows(rows, latest, 0); len(got) != 4 {
		t.Fatalf("season: %d", len(got))
	}
	// prior of window 3 = [06-10, 06-12] -> only 06-10
	if got := priorWindowRows(rows, latest, 3); len(got) != 1 || got[0].Dt != "2026-06-10" {
		t.Fatalf("prior: %+v", got)
	}
	if got := priorWindowRows(rows, latest, 0); got != nil {
		t.Fatalf("season has no prior: %+v", got)
	}
}

func TestByPosition_OrderAndMetrics(t *testing.T) {
	rows := []analysis.GradeRow{
		{Bucket: "OF", Diff: 2}, {Bucket: "C", Diff: -4}, {Bucket: "SP", Diff: 1, IsPitcher: true},
	}
	got := byPosition(rows)
	// order is C, INF, OF, UT, SP, RP — present buckets only -> C, OF, SP
	if len(got) != 3 || got[0].Bucket != "C" || got[1].Bucket != "OF" || got[2].Bucket != "SP" {
		t.Fatalf("order: %+v", got)
	}
	if !approx(got[0].MAE, 4) || !approx(got[0].Bias, -4) {
		t.Fatalf("C metrics: %+v", got[0])
	}
}

func TestCalibration_Bins(t *testing.T) {
	rows := []analysis.GradeRow{
		{Projected: 1, Actual: 1}, {Projected: 1.5, Actual: 3}, // bin [0,2)
		{Projected: 21, Actual: 25}, // bin [20, inf)
	}
	pts := calibration(rows)
	if len(pts) != 2 {
		t.Fatalf("want 2 non-empty bins, got %d: %+v", len(pts), pts)
	}
	if !approx(pts[0].Proj, 1.25) || !approx(pts[0].Actual, 2) || pts[0].N != 2 {
		t.Fatalf("bin0: %+v", pts[0])
	}
}

func TestWorstMisses_SortedByAbsDiff(t *testing.T) {
	rows := []analysis.GradeRow{
		{PlayerID: "a", Diff: 1}, {PlayerID: "b", Diff: -9}, {PlayerID: "c", Diff: 5},
	}
	got := worstMisses(rows, 2)
	if len(got) != 2 || got[0].PlayerID != "b" || got[1].PlayerID != "c" {
		t.Fatalf("misses: %+v", got)
	}
}

func TestRankSystems_OrdersByMAEAndFlagsBest(t *testing.T) {
	latest := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		// atc-ros: |diff| avg 1  (best)
		{System: "atc-ros", Dt: "2026-06-15", Diff: 1},
		{System: "atc-ros", Dt: "2026-06-15", Diff: -1},
		// steamer-ros: |diff| avg 3
		{System: "steamer-ros", Dt: "2026-06-15", Diff: 3},
		{System: "steamer-ros", Dt: "2026-06-15", Diff: -3},
		// thebatx-ros: present in set but no rows in window -> sorts last, never best
	}
	systems := []string{"atc-ros", "steamer-ros", "thebatx-ros"}
	got := rankSystems(rows, systems, latest, 7, "all")
	if len(got) != 3 {
		t.Fatalf("want 3 scores, got %d", len(got))
	}
	if got[0].System != "atc-ros" || !got[0].Best {
		t.Fatalf("want atc-ros best first, got %+v", got[0])
	}
	if got[1].System != "steamer-ros" || got[1].Best {
		t.Fatalf("want steamer-ros second, not best: %+v", got[1])
	}
	if got[2].System != "thebatx-ros" || got[2].N != 0 || got[2].Best {
		t.Fatalf("want empty thebatx-ros last: %+v", got[2])
	}
}

func TestAggregate_DetailUsesProductionSystemOnly(t *testing.T) {
	gen := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{System: detailSystem, Dt: "2026-06-15", Diff: 2, IsPitcher: false, Bucket: "OF"},
		{System: "atc-ros", Dt: "2026-06-15", Diff: 8, IsPitcher: false, Bucket: "OF"},
	}
	m := Aggregate(rows, gen, seasonStart)
	// Detail scorecard (all/season) must reflect only the production system's row.
	v := m.Views["0|all"]
	if v.Scorecard.Cur.N != 1 || !approx(v.Scorecard.Cur.MAE, 2) {
		t.Fatalf("detail should use %s slice only: %+v", detailSystem, v.Scorecard.Cur)
	}
	// Comparison must include both systems.
	if len(m.Systems) != 2 {
		t.Fatalf("want 2 systems, got %v", m.Systems)
	}
	cmp := m.Compare["0|all"]
	if len(cmp) != 2 || cmp[0].System != detailSystem || !cmp[0].Best {
		t.Fatalf("want production system best in compare: %+v", cmp)
	}
}

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

// TestRankSystems_ReportsErrorMetricsAndSortsEmptyLast used to pin
// MAE-ascending ordering, which rankSystems no longer does — it orders by
// within-day rho descending (see TestRankSystems_OrdersByRhoDescending). This
// test survives repurposed: MAE/Bias/RMSE are still computed and reported as
// columns even though they no longer decide sort order, and a system with no
// rows still sorts last and is never Best. Do NOT restore an MAE-ordering
// assertion here — that behavior was deliberately removed.
//
// Role is "hitters" (rankSystems is never called with "all" in production —
// see model.go's comparison-panel loop) and each system has only 2 rows on a
// single day, below minDayRows(3), so Rho is nil for both systems with data;
// that's expected for this test, not something it needs to work around.
func TestRankSystems_ReportsErrorMetricsAndSortsEmptyLast(t *testing.T) {
	latest := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{System: "atc-ros", Dt: "2026-06-15", Diff: 1, IsPitcher: false},
		{System: "atc-ros", Dt: "2026-06-15", Diff: -1, IsPitcher: false},
		{System: "steamer-ros", Dt: "2026-06-15", Diff: 3, IsPitcher: false},
		{System: "steamer-ros", Dt: "2026-06-15", Diff: -3, IsPitcher: false},
		// thebatx-ros: present in set but no rows -> sorts last, never best
	}
	systems := []string{"atc-ros", "steamer-ros", "thebatx-ros"}
	got := rankSystems(rows, systems, latest, 7, "hitters")
	if len(got) != 3 {
		t.Fatalf("want 3 scores, got %d", len(got))
	}

	byName := map[string]SystemScore{}
	for _, s := range got {
		byName[s.System] = s
	}
	if !approx(byName["atc-ros"].MAE, 1) {
		t.Errorf("atc-ros MAE = %v, want 1 (MAE must still be reported as a column)", byName["atc-ros"].MAE)
	}
	if !approx(byName["steamer-ros"].MAE, 3) {
		t.Errorf("steamer-ros MAE = %v, want 3 (MAE must still be reported as a column)", byName["steamer-ros"].MAE)
	}

	last := got[len(got)-1]
	if last.System != "thebatx-ros" || last.N != 0 || last.Best {
		t.Fatalf("want empty thebatx-ros last, N 0, not best: %+v", last)
	}
}

func TestAggregate_ViewsPerSystem(t *testing.T) {
	gen := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	rows := []analysis.GradeRow{
		{System: detailSystem, Dt: "2026-06-15", Diff: 2, IsPitcher: false, Bucket: "OF"},
		{System: "atc-ros", Dt: "2026-06-15", Diff: 8, IsPitcher: false, Bucket: "OF"},
	}
	m := Aggregate(rows, gen, seasonStart)
	// Each system gets its own Detail view (system|window|role), not just the
	// production system, so the dashboard's system picker has data to show.
	prodView := m.Views[detailKey(detailSystem, 0, "all")]
	if prodView.Scorecard.Cur.N != 1 || !approx(prodView.Scorecard.Cur.MAE, 2) {
		t.Fatalf("%s view: %+v", detailSystem, prodView.Scorecard.Cur)
	}
	atcView := m.Views[detailKey("atc-ros", 0, "all")]
	if atcView.Scorecard.Cur.N != 1 || !approx(atcView.Scorecard.Cur.MAE, 8) {
		t.Fatalf("atc-ros view: %+v", atcView.Scorecard.Cur)
	}
	// DetailSystem still names the default-selected system for the UI.
	if m.DetailSystem != detailSystem {
		t.Fatalf("DetailSystem: %q", m.DetailSystem)
	}
	// Comparison must include both systems.
	if len(m.Systems) != 2 {
		t.Fatalf("want 2 systems, got %v", m.Systems)
	}
	// A pooled cross-role ranking has no defensible order (see withinDayRho's
	// doc comment): role "all" is deliberately left uncompared.
	if cmp := m.Compare["0|all"]; cmp != nil {
		t.Fatalf(`want Compare["0|all"] nil, got %+v`, cmp)
	}
	// Both rows are IsPitcher: false, so the head-to-head comparison these
	// rows actually drive lives under "hitters" now.
	cmp := m.Compare["0|hitters"]
	if len(cmp) != 2 {
		t.Fatalf(`want 2 systems in Compare["0|hitters"], got %+v`, cmp)
	}
	// Neither system can be crowned Best here: each has exactly 1 row total,
	// far below minDayRows(3), so withinDayRho finds no qualifying day and
	// Rho is nil for both — the combined-SE gate correctly refuses to pick a
	// winner from a sample this thin, rather than crowning whichever system
	// happens to sort first.
	for _, s := range cmp {
		if s.Best {
			t.Fatalf("system %q flagged Best with no qualifying rho sample: %+v", s.System, s)
		}
	}
}

func TestAggregate_DetailSystemFallsBackWhenNoProductionRows(t *testing.T) {
	gen := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	// Only a non-production system has been captured so far (e.g. early in a
	// shadow rollout) — the production system's Detail view must still exist,
	// just empty, so the default UI selection never looks up a missing key.
	rows := []analysis.GradeRow{
		{System: "atc-ros", Dt: "2026-06-15", Diff: 8, IsPitcher: false, Bucket: "OF"},
	}
	m := Aggregate(rows, gen, seasonStart)
	if len(m.Systems) != 1 || m.Systems[0] != "atc-ros" {
		t.Fatalf("want only atc-ros in Systems, got %v", m.Systems)
	}
	prodView, ok := m.Views[detailKey(detailSystem, 0, "all")]
	if !ok {
		t.Fatalf("missing fallback %s view; keys=%v", detailSystem, m.Views)
	}
	if prodView.Scorecard.Cur.N != 0 {
		t.Fatalf("fallback view should be empty: %+v", prodView.Scorecard.Cur)
	}
}

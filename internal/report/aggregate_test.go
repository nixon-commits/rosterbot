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

// gradedDay builds one day's rows for a system: three players whose actuals
// are fixed (10/5/0) and whose projections either match that order (rho +1) or
// reverse it (rho -1). Three rows is exactly minDayRows, so every day here
// produces a defined correlation.
func gradedDay(system, dt string, ordered bool) []analysis.GradeRow {
	actual := []float64{10, 5, 0}
	proj := []float64{10, 5, 0}
	if !ordered {
		proj = []float64{0, 5, 10}
	}
	ids := []string{"p1", "p2", "p3"}
	out := make([]analysis.GradeRow, 3)
	for i := range ids {
		out[i] = analysis.GradeRow{
			System: system, Dt: dt, PlayerID: ids[i],
			Projected: proj[i], Actual: actual[i], Diff: actual[i] - proj[i],
		}
	}
	return out
}

// A system graded on days its rivals never saw must not be scored on them.
// Before the paired join, rankSystems filtered each system independently, so a
// system with wider coverage was compared on a sample nobody else competed on
// — measured in production as depthcharts-ros posting pitcher rho +0.3323 over
// 29 days against rivals' 19, a lead that vanished entirely once paired.
func TestRankSystems_ScoresEverySystemOnTheSameCommonPlayerDays(t *testing.T) {
	latest := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	var rows []analysis.GradeRow
	// Four days both systems graded, with identical projections, so any
	// difference in their scores can only come from the unpaired days below.
	for _, dt := range []string{"2026-06-11", "2026-06-12", "2026-06-13", "2026-06-14"} {
		rows = append(rows, gradedDay("atc-ros", dt, false)...)
		rows = append(rows, gradedDay("steamer-ros", dt, false)...)
	}
	// Four more days only atc-ros graded, on which it orders perfectly.
	for _, dt := range []string{"2026-06-15", "2026-06-16", "2026-06-17", "2026-06-18"} {
		rows = append(rows, gradedDay("atc-ros", dt, true)...)
	}

	got := rankSystems(rows, []string{"atc-ros", "steamer-ros"}, latest, 0, "hitters")
	byName := map[string]SystemScore{}
	for _, s := range got {
		byName[s.System] = s
	}
	atc, steamer := byName["atc-ros"], byName["steamer-ros"]

	if atc.N != steamer.N {
		t.Errorf("paired sample differs: atc-ros N=%d, steamer-ros N=%d — a head-to-head "+
			"comparison must score both systems on the same player-days", atc.N, steamer.N)
	}
	if atc.Rho == nil || steamer.Rho == nil {
		t.Fatalf("both systems must produce a rho: atc=%+v steamer=%+v", atc.Rho, steamer.Rho)
	}
	if atc.Rho.Days != steamer.Rho.Days {
		t.Errorf("paired days differ: atc-ros %dd, steamer-ros %dd", atc.Rho.Days, steamer.Rho.Days)
	}
	// On the common days the two systems projected identically, so once the
	// unpaired days are excluded their rank skill must be identical too.
	if !approx(atc.Rho.Rho, steamer.Rho.Rho) {
		t.Errorf("atc-ros rho %v != steamer-ros rho %v; the four atc-only days are "+
			"still inflating its score", atc.Rho.Rho, steamer.Rho.Rho)
	}
}

// TotalN keeps the discarded rows visible. A paired comparison silently
// throwing away half a system's coverage is the same invisibility this bead
// exists to fix, one level down.
func TestRankSystems_ReportsUnpairedCoverageAlongsideThePairedSample(t *testing.T) {
	latest := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	var rows []analysis.GradeRow
	for _, dt := range []string{"2026-06-11", "2026-06-12"} {
		rows = append(rows, gradedDay("atc-ros", dt, false)...)
		rows = append(rows, gradedDay("steamer-ros", dt, false)...)
	}
	rows = append(rows, gradedDay("atc-ros", "2026-06-18", true)...)

	got := rankSystems(rows, []string{"atc-ros", "steamer-ros"}, latest, 0, "hitters")
	byName := map[string]SystemScore{}
	for _, s := range got {
		byName[s.System] = s
	}
	if got, want := byName["atc-ros"].TotalN, 9; got != want {
		t.Errorf("atc-ros TotalN = %d, want %d (its own unpaired row count)", got, want)
	}
	if got, want := byName["atc-ros"].N, 6; got != want {
		t.Errorf("atc-ros N = %d, want %d (the paired count)", got, want)
	}
	if got, want := byName["steamer-ros"].TotalN, 6; got != want {
		t.Errorf("steamer-ros TotalN = %d, want %d", got, want)
	}
}

// withinDayRho leaves SE at zero when only one day qualifies, because a
// standard deviation needs two observations. flagBest then computes
// sep = sqrt(0+0) = 0, so ANY difference clears the gate the SE exists to
// enforce. Reproduced against production data: on a single day it crowned
// steamer-ros at rho +0.0000 over atc-ros at -0.6000 — zero measured skill,
// "best" badge.
func TestFlagBest_WithholdsTheCrownWhenTheCommonSampleIsTooThin(t *testing.T) {
	latest := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	var rows []analysis.GradeRow
	rows = append(rows, gradedDay("atc-ros", "2026-06-11", true)...)      // rho +1
	rows = append(rows, gradedDay("steamer-ros", "2026-06-11", false)...) // rho -1

	got := rankSystems(rows, []string{"atc-ros", "steamer-ros"}, latest, 0, "hitters")
	if got[0].Rho == nil || got[0].Rho.Days != 1 {
		t.Fatalf("fixture should yield exactly one qualifying day: %+v", got[0].Rho)
	}
	if got[0].Rho.SE != 0 {
		t.Fatalf("fixture should yield SE 0 on a single day: %+v", got[0].Rho)
	}
	for _, s := range got {
		if s.Best {
			t.Errorf("system %q flagged Best on a %d-day common sample (SE %v): a "+
				"single day makes the standard error zero, so the separation gate "+
				"admits any difference at all", s.System, s.Rho.Days, s.Rho.SE)
		}
	}
}

// Pins the floor itself at its boundary. A threshold nobody tests at the edge
// drifts silently: the separation is identical on both sides here (+1 vs -1,
// SE 0), so the ONLY thing deciding the verdict is the common day count.
func TestFlagBest_RequiresMinCompareDaysCommonSample(t *testing.T) {
	run := func(days int) []SystemScore {
		rows := append(
			rhoDays("good-sys", false, days, 8, true),
			rhoDays("bad-sys", false, days, 8, false)...,
		)
		return rankSystems(rows, []string{"bad-sys", "good-sys"}, time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC), 0, "hitters")
	}

	below := run(minCompareDays - 1)
	if below[0].Rho == nil || below[0].Rho.Days != minCompareDays-1 {
		t.Fatalf("fixture should yield %d common days: %+v", minCompareDays-1, below[0].Rho)
	}
	if below[0].Best {
		t.Errorf("leader flagged Best on %d common days, one below the minCompareDays(%d) floor",
			below[0].Rho.Days, minCompareDays)
	}

	at := run(minCompareDays)
	if !at[0].Best {
		t.Errorf("leader NOT flagged Best at exactly minCompareDays(%d) common days; the floor "+
			"must be inclusive, or a perfectly separated leader is never crowned", minCompareDays)
	}
}

// analysis.Reader stamps pre-migration partitions (no system= segment) with
// LegacySystem, which is the literal string "depthcharts-ros" — so a legacy
// row is indistinguishable from a real production row unless provenance is
// carried explicitly. Pooled, the incumbent was credited with 14 days
// (MAE 4.8915, n=298) recorded before any challenger existed.
//
// Pairing on (date, player) removes legacy days whenever they predate every
// challenger, which is the situation today. It is NOT sufficient on its own:
// re-grading a legacy date writes dt=X/system=Y/ while the old
// dt=X/grades.ndjson survives, so the incumbent gets two rows for the same
// player-day and both pass the pairing filter. That doubles its weight on
// exactly the days a recovery run touched.
func TestAggregate_LegacyRowsAreExcludedFromComparisonButKeptInDetail(t *testing.T) {
	gen := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	seasonStart := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)

	var rows []analysis.GradeRow
	dates := []string{"2026-06-11", "2026-06-12", "2026-06-13"}
	for _, dt := range dates {
		// Both systems grade the same players, both ordering them backwards.
		rows = append(rows, gradedDay(detailSystem, dt, false)...)
		rows = append(rows, gradedDay("atc-ros", dt, false)...)
		// A re-graded legacy partition covering the SAME player-days, ordering
		// them perfectly. Pooled, this drags the incumbent's rho upward.
		for _, r := range gradedDay(detailSystem, dt, true) {
			r.Legacy = true
			rows = append(rows, r)
		}
	}

	m := Aggregate(rows, gen, seasonStart)
	byName := map[string]SystemScore{}
	for _, s := range m.Compare[viewKey(0, "hitters")] {
		byName[s.System] = s
	}
	prod, atc := byName[detailSystem], byName["atc-ros"]

	if prod.TotalN != atc.TotalN {
		t.Errorf("%s TotalN=%d vs atc-ros TotalN=%d: legacy rows are still entering the "+
			"comparison and double-counting the incumbent's player-days",
			detailSystem, prod.TotalN, atc.TotalN)
	}
	if prod.Rho == nil || atc.Rho == nil {
		t.Fatalf("both systems need a rho: prod=%+v atc=%+v", prod.Rho, atc.Rho)
	}
	if !approx(prod.Rho.Rho, atc.Rho.Rho) {
		t.Errorf("%s rho %v != atc-ros rho %v; the two systems projected identically on "+
			"every compared player-day, so only pooled legacy rows can separate them",
			detailSystem, prod.Rho.Rho, atc.Rho.Rho)
	}

	// ...but the detail dashboard keeps its pre-migration history.
	v := m.Views[detailKey(detailSystem, 0, "hitters")]
	if v.Scorecard.Cur.N != len(dates)*6 {
		t.Errorf("detail view N = %d, want %d (legacy rows must be RETAINED here — "+
			"they are the only record of the pre-migration season)",
			v.Scorecard.Cur.N, len(dates)*6)
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

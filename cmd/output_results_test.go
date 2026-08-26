package cmd

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
)

func TestBacktestToWireResult(t *testing.T) {
	rep := backtest.Report{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		Lineup: []backtest.LineupDayResult{
			{Date: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC), ActualPts: 40, OptimalPts: 42, Gap: -2},
		},
		ProjectionSummary: &backtest.ProjectionSummary{MAE: 1.4, Bias: 0.3, RMSE: 2.1, TotalPlayerDays: 240,
			ByPosition: []backtest.PositionMAE{{Bucket: "OF", N: 50, MAE: 1.1, Bias: 0.2}}},
	}
	out := backtestToWireResult(rep)
	if out.Start != "2026-06-08" || out.End != "2026-06-14" || len(out.Days) != 1 {
		t.Fatalf("out: %+v", out)
	}
	if out.Days[0].Gap != -2 || out.Days[0].Actual != 40 {
		t.Fatalf("day0: %+v", out.Days[0])
	}
	if out.Accuracy == nil || out.Accuracy.MAE != 1.4 || out.Accuracy.N != 240 || len(out.Accuracy.ByPosition) != 1 {
		t.Fatalf("accuracy: %+v", out.Accuracy)
	}
}

func TestGradeToWireResult(t *testing.T) {
	byDate := map[string]int{"2026-06-18": 10, "2026-06-19": 12}
	out := gradeToWireResult(byDate)
	if out.RowsWritten != 22 {
		t.Fatalf("rows: %d", out.RowsWritten)
	}
	if len(out.Dates) != 2 || out.Dates[0] != "2026-06-18" || out.Dates[1] != "2026-06-19" {
		t.Fatalf("dates not sorted: %+v", out.Dates)
	}
}

// TestBacktestToWireResult_MapsRosterShape pins rosterbot-c21e: Report.Shape
// reached backtest --json from the day it was written but not the wire type, so
// the dashboard rendered a backtest run with no roster-shape section at all.
//
// It asserts against the marshalled JSON rather than the Go struct because the
// dashboard's run viewer is a generic JSON-to-DOM renderer that prints object
// keys verbatim as labels. The key names are therefore behaviour, not naming
// taste — and the one that matters most is that the two sides do NOT share a
// rate key: the pitcher rate measures probable-start compliance and reads ~100%
// at any roster depth, so presenting it beside a hitters `fielded_pct` under
// the same name is exactly the misreading rosterbot-8dl exists to close.
func TestBacktestToWireResult_MapsRosterShape(t *testing.T) {
	rep := backtest.Report{
		Start: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		Shape: &backtest.RosterShape{
			Days: 7, DaysWithSnapshot: 7,
			HitterSlots: 13, PitcherSlots: 6,
			GSCapMin: 12, GSCapMax: 12,
			// 1043.7 - 804.9 is 238.80000000000007 in float64: a subtraction
			// this mapper manufactures, on a surface that prints verbatim.
			Hitters: backtest.SideShape{
				OwnedPts: 1043.7, FieldedPts: 804.9,
				DeployableCount: 70, RosteredCount: 91,
			},
			// 2 of 13 is the real measured coverage CLAUDE.md cites: a rate on
			// that base is a near-single-player fact wearing a percentage, and
			// the counts are the only thing that lets a reader see it.
			Pitchers: backtest.SideShape{
				OwnedPts: 500, FieldedPts: 500,
				DeployableCount: 2, RosteredCount: 13,
			},
			SPRosterDaySum: 63,
		},
	}

	out := backtestToWireResult(rep)
	if out.Shape == nil {
		t.Fatalf("Shape not mapped onto the wire result")
	}

	doc := marshalToMap(t, out)
	shape := childObject(t, doc, "roster_shape")

	hitters := childObject(t, shape, "hitters")
	pitchers := childObject(t, shape, "pitchers")

	// The coverage counts must survive the slim wire shape on BOTH sides.
	if got := pitchers["deployable_player_days"]; got != float64(2) {
		t.Errorf("pitcher deployable_player_days = %v, want 2", got)
	}
	if got := pitchers["rostered_player_days"]; got != float64(13) {
		t.Errorf("pitcher rostered_player_days = %v, want 13", got)
	}
	if got := hitters["rostered_player_days"]; got != float64(91) {
		t.Errorf("hitter rostered_player_days = %v, want 91", got)
	}

	// The asymmetry itself, not a literal string: whatever the pitcher rate is
	// called, it must not be presented as the hitter rate's twin.
	for k := range pitchers {
		if strings.Contains(k, "fielded_pct") {
			t.Errorf("pitcher side carries %q: that rate measures probable-start compliance, "+
				"not the hitter side's fielded share (rosterbot-8dl)", k)
		}
	}
	if got := pitchers["probable_start_compliance_pct"]; got != float64(100) {
		t.Errorf("probable_start_compliance_pct = %v, want 100", got)
	}
	if got := hitters["fielded_pct_of_owned"]; got != 77.1 {
		t.Errorf("fielded_pct_of_owned = %v, want 77.1", got)
	}

	// Stranded rides along per side so the reader is not left subtracting, and
	// stays in separate objects so the two are not read as addends. The exact
	// 238.8 is the assertion: unrounded this is 238.80000000000007, which the
	// dashboard would print in full as precision the figure does not have.
	if got := hitters["stranded_pts"]; got != 238.8 {
		t.Errorf("hitter stranded_pts = %v, want 238.8", got)
	}

	// Rotation supply is the measure the pitcher rate structurally cannot be:
	// 63 SP-roster-days over 7 days is 9 starters, ~12.6 starts/wk on a
	// five-man rotation, 105% of a 12/wk cap.
	rot := childObject(t, shape, "rotation_supply")
	if got := rot["mean_sp_count"]; got != float64(9) {
		t.Errorf("mean_sp_count = %v, want 9", got)
	}
	if got := rot["supply_starts_per_week"]; got != 12.6 {
		t.Errorf("supply_starts_per_week = %v, want 12.6", got)
	}
	if got := rot["gs_cap_per_week"]; got != float64(12) {
		t.Errorf("gs_cap_per_week = %v, want 12", got)
	}
	if got := rot["supply_pct_of_cap"]; got != float64(105) {
		t.Errorf("supply_pct_of_cap = %v, want 105", got)
	}
}

// TestBacktestToWireResult_ShapeWithholdsUnmeasurableFigures pins the two
// omit-rather-than-zero rules, which a mapper using plain float64 fields would
// break silently and in the worst direction.
//
// A side with no deployable value renders as 0% — total failure — where the
// truth is that there was nothing to measure. And a rotation supply with no
// recorded game-start cap renders a starts-per-week figure whose denominator
// the reader would then invent.
func TestBacktestToWireResult_ShapeWithholdsUnmeasurableFigures(t *testing.T) {
	rep := backtest.Report{
		Shape: &backtest.RosterShape{
			Days: 7, DaysWithSnapshot: 7,
			HitterSlots: 13, PitcherSlots: 6,
			Hitters: backtest.SideShape{
				OwnedPts: 100, FieldedPts: 100,
				DeployableCount: 70, RosteredCount: 91,
			},
			// No pitcher was a probable start all window: nothing deployable,
			// so no rate exists to report.
			Pitchers:       backtest.SideShape{RosteredCount: 13},
			SPRosterDaySum: 63, // GS tracking off, so GSCapMax stays 0.
		},
	}

	doc := marshalToMap(t, backtestToWireResult(rep))
	shape := childObject(t, doc, "roster_shape")
	pitchers := childObject(t, shape, "pitchers")

	for k, v := range pitchers {
		if strings.HasSuffix(k, "_pct") {
			t.Errorf("pitcher side reports %q = %v with no deployable value; "+
				"absent and 0%% are opposite readings", k, v)
		}
	}
	// Coverage still prints: 0 of 13 deployable is the fact that explains the
	// missing rate, so dropping it would leave the absence unexplained.
	if got := pitchers["rostered_player_days"]; got != float64(13) {
		t.Errorf("pitcher rostered_player_days = %v, want 13", got)
	}
	if _, ok := shape["rotation_supply"]; ok {
		t.Errorf("rotation_supply present with no recorded GS cap: %v", shape["rotation_supply"])
	}
	if _, ok := shape["gs_cap_max_per_week"]; ok {
		t.Errorf("gs_cap_max_per_week present with GS tracking off; 0 reads as a cap of none")
	}
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func childObject(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("key %q absent; have %v", key, keys)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("key %q is %T, want an object", key, v)
	}
	return obj
}

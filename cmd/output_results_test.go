package cmd

import (
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

package cmd

import (
	"sort"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

const wireDate = "2006-01-02"

// backtestToWireResult maps a backtest.Report to the iOS wire shape: per-day
// actual/optimal/gap plus the projection-accuracy rollup when present.
func backtestToWireResult(rep backtest.Report) lineupapi.BacktestResult {
	out := lineupapi.BacktestResult{
		Start: rep.Start.UTC().Format(wireDate),
		End:   rep.End.UTC().Format(wireDate),
	}
	for _, d := range rep.Lineup {
		out.Days = append(out.Days, lineupapi.BacktestDayOut{
			Date:    d.Date.UTC().Format(wireDate),
			Actual:  d.ActualPts,
			Optimal: d.OptimalPts,
			Gap:     d.Gap,
		})
	}
	if s := rep.ProjectionSummary; s != nil {
		acc := &lineupapi.BacktestAccuracy{MAE: s.MAE, Bias: s.Bias, RMSE: s.RMSE, N: s.TotalPlayerDays}
		for _, p := range s.ByPosition {
			acc.ByPosition = append(acc.ByPosition, lineupapi.BacktestPositionOut{
				Bucket: p.Bucket, N: p.N, MAE: p.MAE, Bias: p.Bias,
			})
		}
		out.Accuracy = acc
	}
	return out
}

// gradeToWireResult summarizes what grade wrote: the sorted set of dates and the
// total graded-row count.
func gradeToWireResult(rowsByDate map[string]int) lineupapi.GradeResult {
	out := lineupapi.GradeResult{}
	for dt, n := range rowsByDate {
		out.Dates = append(out.Dates, dt)
		out.RowsWritten += n
	}
	sort.Strings(out.Dates)
	return out
}

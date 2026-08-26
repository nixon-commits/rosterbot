package cmd

import (
	"math"
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
			Actual:  round1(d.ActualPts),
			Optimal: round1(d.OptimalPts),
			Gap:     round1(d.Gap),
		})
	}
	if s := rep.ProjectionSummary; s != nil {
		acc := &lineupapi.BacktestAccuracy{MAE: round1(s.MAE), Bias: round1(s.Bias), RMSE: round1(s.RMSE), N: s.TotalPlayerDays}
		for _, p := range s.ByPosition {
			acc.ByPosition = append(acc.ByPosition, lineupapi.BacktestPositionOut{
				Bucket: p.Bucket, N: p.N, MAE: round1(p.MAE), Bias: round1(p.Bias),
			})
		}
		out.Accuracy = acc
	}
	if g := rep.Gate; g != nil {
		gate := &lineupapi.BacktestGateOut{
			Days:               g.Days,
			DaysWithSnapshot:   g.DaysWithSnapshot,
			DaysStale:          g.DaysStale,
			SuppressedStarts:   g.SuppressedStarts,
			SuppressedPtsGross: round1(g.SuppressedPts),
			ProtectedStarts:    g.ProtectedStarts,
			ProtectedPtsGross:  round1(g.ProtectedPts),
			FloorMin:           g.FloorMin,
			FloorMax:           g.FloorMax,
		}
		for _, d := range g.ByDate {
			gate.ByDate = append(gate.ByDate, lineupapi.BacktestGateDayOut{
				Date:              d.Date.UTC().Format(wireDate),
				SuppressedStarts:  d.Starts,
				PtsGross:          round1(d.Pts),
				ProtectedStarts:   d.Protected,
				ProtectedPtsGross: round1(d.ProtectedPts),
			})
		}
		out.Gate = gate
	}
	if sh := rep.Shape; sh != nil {
		shape := &lineupapi.BacktestShapeOut{
			Days:             sh.Days,
			DaysWithSnapshot: sh.DaysWithSnapshot,
			DaysStale:        sh.DaysStale,
			DaysPreSchema:    sh.DaysPreSchema,
			HitterSlots:      sh.HitterSlots,
			PitcherSlots:     sh.PitcherSlots,
			GSCapMinPerWeek:  sh.GSCapMin,
			GSCapMaxPerWeek:  sh.GSCapMax,
		}
		shape.Hitters.BacktestSideShapeOut = sideShapeToWire(sh.Hitters)
		shape.Hitters.FieldedPctOfOwned = fieldedPct(sh.Hitters)
		shape.Pitchers.BacktestSideShapeOut = sideShapeToWire(sh.Pitchers)
		shape.Pitchers.ProbableStartCompliancePct = fieldedPct(sh.Pitchers)
		shape.Rotation = rotationToWire(sh.Rotation())
		out.Shape = shape
	}
	return out
}

// sideShapeToWire copies the part of a side's shape that means the same thing
// for both roles. The fielded rate is deliberately NOT copied here: the two
// sides name it differently on purpose, so that the pitcher rate cannot be read
// as the hitter one's counterpart (lineupapi.BacktestShapeOut).
func sideShapeToWire(s backtest.SideShape) lineupapi.BacktestSideShapeOut {
	return lineupapi.BacktestSideShapeOut{
		OwnedPts:             round1(s.OwnedPts),
		FieldedPts:           round1(s.FieldedPts),
		StrandedPts:          round1(s.StrandedPts()),
		DeployablePlayerDays: s.DeployableCount,
		RosteredPlayerDays:   s.RosteredCount,
	}
}

// fieldedPct renders a side's fielded rate as a percentage, or nil when the
// side had no deployable value at all. The nil is load-bearing rather than
// defensive: SideShape.FieldedRate reports that case as ok=false precisely
// because 0% and "nothing to measure" are opposite readings, and the dashboard
// prints verbatim whatever it is handed.
func fieldedPct(s backtest.SideShape) *float64 {
	rate, ok := s.FieldedRate()
	if !ok {
		return nil
	}
	p := pctOf(rate)
	return &p
}

// rotationToWire carries the weekly rotation-supply view, and returns nil in
// exactly the cases formatRotationSupply prints nothing: no counted day, no
// SP-eligible pitcher carried, or no recorded game-start cap. Keeping those
// conditions identical is the point — a reader comparing the stdout report
// against the dashboard for one run must not find a figure in one and not the
// other, and a supply figure without its cap is a ratio the reader would
// complete themselves.
func rotationToWire(r backtest.RotationSupply) *lineupapi.BacktestRotationOut {
	mean, ok := r.MeanSPCount()
	if !ok || r.SPRosterDays == 0 {
		return nil
	}
	rate, ok := r.SupplyRate()
	if !ok {
		return nil
	}
	supply, _ := r.SupplyPerWeek()
	return &lineupapi.BacktestRotationOut{
		MeanSPCount:         round1(mean),
		SupplyStartsPerWeek: round1(supply),
		GSCapPerWeek:        r.GSCap,
		SupplyPctOfCap:      pctOf(rate),
	}
}

// pctOf converts a 0..1 rate to a percentage rounded to one decimal.
//
// EVERY float backtestToWireResult puts on the wire goes through round1, and
// that is a property of the whole mapper rather than of any one section. The
// dashboard's run viewer prints values verbatim, and the two worst offenders
// are manufactured right here rather than inherited: converting a ratio yields
// 77.12344999999999, and StrandedPts subtracts two four-digit sums of hundreds
// of per-day projections, which produced 238.80000000000007 from figures a
// reader would subtract to 238.8. Presenting either as-is offers float
// artifacts as precision that a projected-point total never had — the stdout
// report prints the same quantities at %.1f for the same reason.
//
// Applying it to the two sections that happened to manufacture the artifacts
// and not to their siblings would be worse than not rounding at all: one
// response body would carry a clean 77.1 beside a raw 78.30000000000001, and
// the difference would encode nothing except which lines a past commit
// touched. The rule is uniform so a reader never has to ask.
//
// The cost is that the dashboard and backtest --json disagree in the third
// decimal of the same quantity, since --json marshals backtest.Report directly
// and never passes through here. That is the smaller of the two divergences
// this wire type already carries deliberately (the key names differ outright).
// internal/backtest itself stays exact: other consumers need full precision,
// and rounding belongs at a presentation boundary, which this is.
func pctOf(rate float64) float64 { return round1(rate * 100) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// gradeToWireResult summarizes what grade wrote: the sorted set of dates and the
// total graded-row count.
func gradeToWireResult(rowsByDate map[string]int, windowNotes []string) lineupapi.GradeResult {
	out := lineupapi.GradeResult{WindowNotes: windowNotes}
	for dt, n := range rowsByDate {
		out.Dates = append(out.Dates, dt)
		out.RowsWritten += n
	}
	sort.Strings(out.Dates)
	return out
}

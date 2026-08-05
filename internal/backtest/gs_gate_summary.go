package backtest

import (
	"fmt"
	"strings"
	"time"
)

// GateSummary aggregates the weekly game-start gate's suppressions across a
// window of days.
//
// The gate runs for today's date only — future dates in a --dates range are
// optimized without it, since each day gets its own run — so a weekly figure
// has to accumulate across the daily projection snapshots.
type GateSummary struct {
	// Days is the size of the window asked for.
	Days int
	// DaysWithSnapshot is how many of those days actually had a snapshot on
	// disk. A missing day is not a zero-suppression day, and the difference is
	// what makes a window thinned by failed runs visible rather than reading as
	// a quiet week.
	DaysWithSnapshot int
	SuppressedStarts int
	// SuppressedPts is GROSS projected value declined, not a net weekly loss —
	// see optimizer.GSGateReport.SuppressedPts.
	SuppressedPts float64
	// ByDate holds only the days that had a snapshot AND at least one
	// suppression, in window order.
	ByDate []GateDay
}

// GateDay is one date's contribution to a GateSummary.
type GateDay struct {
	Date   time.Time
	Starts int
	Pts    float64
}

// SummarizeGSGate reads the projection snapshots for the given dates and totals
// the starts the GS gate declined. Days with no snapshot are counted in Days but
// not DaysWithSnapshot, and contribute nothing.
func SummarizeGSGate(dir string, dates []time.Time) GateSummary {
	sum := GateSummary{Days: len(dates)}
	for _, d := range dates {
		snap, ok := LoadSnapshot(dir, d)
		if !ok {
			continue
		}
		sum.DaysWithSnapshot++

		day := GateDay{Date: d}
		for _, p := range snap.Pitchers {
			if !p.GSSuppressed {
				continue
			}
			day.Starts++
			day.Pts += p.ProjPtsPerGame
		}
		if day.Starts == 0 {
			continue
		}
		sum.SuppressedStarts += day.Starts
		sum.SuppressedPts += day.Pts
		sum.ByDate = append(sum.ByDate, day)
	}
	return sum
}

// FormatGateSummary renders the GS-gate section of the backtest report.
func FormatGateSummary(s GateSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nGS GATE — starts the weekly game-start cap declined\n")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 52))
	if s.DaysWithSnapshot == 0 {
		fmt.Fprintf(&b, "No snapshots on disk for these %d days — nothing to report.\n", s.Days)
		return b.String()
	}
	fmt.Fprintf(&b, "%d start(s) suppressed over %d of %d days, %.1f gross projected pts.\n",
		s.SuppressedStarts, s.DaysWithSnapshot, s.Days, s.SuppressedPts)
	fmt.Fprintf(&b, "Gross, not a net loss: the budget went to a higher-ranked start.\n")
	fmt.Fprintf(&b, "A gate that fires often means the roster owns more SP than 6 P\n")
	fmt.Fprintf(&b, "slots and the weekly cap let it deploy.\n")
	for _, d := range s.ByDate {
		fmt.Fprintf(&b, "  %s  %d start(s)  %.1f pts\n", d.Date.Format("Mon Jan 2"), d.Starts, d.Pts)
	}
	return b.String()
}

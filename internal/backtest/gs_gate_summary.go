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
	// DaysWithSnapshot is how many of those days had a snapshot on disk that
	// was ALSO generated on the same Eastern-time calendar day it projects
	// (see sameETDate). --matchup pre-writes a snapshot for every remaining
	// day of the matchup week on every hourly run; a day whose own later run
	// never landed keeps that pre-write, which carries GSSuppressed=false for
	// every pitcher because the gate only ever runs for today's date. Counting
	// that day as measured would report a real gap as a quiet, fully-measured
	// week. A missing or stale day is not a zero-suppression day, and the
	// difference is what makes a window thinned by failed runs visible.
	DaysWithSnapshot int
	// DaysStale is how many days had a snapshot on disk whose GeneratedAt
	// predates the date it projects (a --matchup pre-write never overwritten
	// by that day's own run). Excluded from DaysWithSnapshot and from
	// suppression totals, same as a missing day.
	DaysStale        int
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
// not DaysWithSnapshot, and contribute nothing. Days whose snapshot is stale —
// generated on a different Eastern-time calendar day than the date it projects,
// per the same sameETDate rule RunProjectionAnalysis uses — are counted in
// DaysStale instead of DaysWithSnapshot and also contribute nothing, since a
// stale --matchup pre-write always carries GSSuppressed=false and would
// otherwise be misread as a fully-measured, suppression-free day.
func SummarizeGSGate(st SnapshotStore, dir string, dates []time.Time) GateSummary {
	sum := GateSummary{Days: len(dates)}
	for _, d := range dates {
		snap, ok := LoadSnapshot(st, dir, d)
		if !ok {
			continue
		}
		// A zero GeneratedAt predates the field and can't be judged for
		// staleness, so it's graded as before — matching RunProjectionAnalysis.
		if !snap.GeneratedAt.IsZero() && !sameETDate(snap.GeneratedAt, d) {
			sum.DaysStale++
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
		if s.DaysStale > 0 {
			fmt.Fprintf(&b, "%d snapshot(s) on disk for these %d days, all stale (never overwritten by that day's own run) — nothing to report.\n", s.DaysStale, s.Days)
		} else {
			fmt.Fprintf(&b, "No snapshots on disk for these %d days — nothing to report.\n", s.Days)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%d start(s) suppressed over %d of %d days, %.1f gross projected pts.\n",
		s.SuppressedStarts, s.DaysWithSnapshot, s.Days, s.SuppressedPts)
	if s.DaysStale > 0 {
		fmt.Fprintf(&b, "%d day(s) excluded as stale (--matchup pre-write never overwritten by that day's own run).\n", s.DaysStale)
	}
	fmt.Fprintf(&b, "Gross, not a net loss: the budget went to a higher-ranked start.\n")
	fmt.Fprintf(&b, "A gate that fires often means the roster owns more SP than 6 P\n")
	fmt.Fprintf(&b, "slots and the weekly cap let it deploy.\n")
	for _, d := range s.ByDate {
		fmt.Fprintf(&b, "  %s  %d start(s)  %.1f pts\n", d.Date.Format("Mon Jan 2"), d.Starts, d.Pts)
	}
	return b.String()
}

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
	Days int `json:"days"`
	// DaysWithSnapshot is how many of those days had a snapshot on disk that
	// was ALSO generated on the same Eastern-time calendar day it projects
	// (see sameETDate). --matchup pre-writes a snapshot for every remaining
	// day of the matchup week on every hourly run; a day whose own later run
	// never landed keeps that pre-write, which carries GSSuppressed=false for
	// every pitcher because the gate only ever runs for today's date. Counting
	// that day as measured would report a real gap as a quiet, fully-measured
	// week. A missing or stale day is not a zero-suppression day, and the
	// difference is what makes a window thinned by failed runs visible.
	DaysWithSnapshot int `json:"days_with_snapshot"`
	// DaysStale is how many days had a snapshot on disk whose GeneratedAt
	// predates the date it projects (a --matchup pre-write never overwritten
	// by that day's own run). Excluded from DaysWithSnapshot and from
	// suppression totals, same as a missing day.
	DaysStale        int `json:"days_stale,omitempty"`
	SuppressedStarts int `json:"suppressed_starts"`
	// SuppressedPts is GROSS projected value declined, not a net weekly loss —
	// see optimizer.GSGateReport.SuppressedPts. The json tag carries that
	// warning to a reader who only ever sees the wire form, where none of this
	// prose reaches them.
	SuppressedPts float64 `json:"suppressed_pts_gross"`
	// ProtectedStarts/ProtectedPts count the starts the gate declined to
	// suppress because the week would otherwise have finished under the
	// league's GS MINIMUM (rosterbot-dpm). Disjoint from the suppressed
	// figures, and GROSS in exactly the same sense: value that stayed
	// deployed, not value gained.
	ProtectedStarts int     `json:"protected_starts,omitempty"`
	ProtectedPts    float64 `json:"protected_pts_gross,omitempty"`
	// FloorDays is how many measured days recorded a non-zero GS minimum, and
	// FloorMin/FloorMax bound it over those days. Zero FloorDays means no
	// measured day had a floor configured — reported rather than rendered as
	// "floor 0", which would read as a floor of none rather than as an absent
	// one.
	FloorDays int `json:"floor_days,omitempty"`
	FloorMin  int `json:"floor_min,omitempty"`
	FloorMax  int `json:"floor_max,omitempty"`
	// ByDate holds only the days that had a snapshot AND at least one
	// suppression or protection, in window order.
	ByDate []GateDay `json:"by_date,omitempty"`
}

// GateDay is one date's contribution to a GateSummary.
type GateDay struct {
	Date         time.Time `json:"date"`
	Starts       int       `json:"starts"`
	Pts          float64   `json:"pts_gross"`
	Protected    int       `json:"protected,omitempty"`
	ProtectedPts float64   `json:"protected_pts_gross,omitempty"`
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

		if snap.GSFloor > 0 {
			sum.FloorDays++
			if sum.FloorMin == 0 || snap.GSFloor < sum.FloorMin {
				sum.FloorMin = snap.GSFloor
			}
			if snap.GSFloor > sum.FloorMax {
				sum.FloorMax = snap.GSFloor
			}
		}

		day := GateDay{Date: d}
		for _, p := range snap.Pitchers {
			if p.GSSuppressed {
				day.Starts++
				day.Pts += p.ProjPtsPerGame
			}
			if p.GSFloorProtected {
				day.Protected++
				day.ProtectedPts += p.ProjPtsPerGame
			}
		}
		// A protection-only day is a real event and gets a row: the gate acted,
		// it simply acted on the floor's behalf rather than the ceiling's.
		if day.Starts == 0 && day.Protected == 0 {
			continue
		}
		sum.SuppressedStarts += day.Starts
		sum.SuppressedPts += day.Pts
		sum.ProtectedStarts += day.Protected
		sum.ProtectedPts += day.ProtectedPts
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
	fmt.Fprint(&b, formatFloorLine(s))
	for _, d := range s.ByDate {
		fmt.Fprintf(&b, "  %s  %d suppressed  %.1f pts", d.Date.Format("Mon Jan 2"), d.Starts, d.Pts)
		if d.Protected > 0 {
			fmt.Fprintf(&b, "  ·  %d protected  %.1f pts", d.Protected, d.ProtectedPts)
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

// formatFloorLine reports the floor side of the gate.
//
// It prints whenever a floor was in force, not only when protection fired,
// because the ceiling line above is otherwise the only visible signal and it
// reads as healthy on exactly the weeks that finish on the minimum: measured
// across periods 18-20 the gate reported "0 start(s) suppressed" every week
// while the team landed on the floor at 10 of 12 twice (rosterbot-dpm). A
// summary that can only say something when the ceiling binds cannot describe
// the failure that actually recurs.
func formatFloorLine(s GateSummary) string {
	if s.FloorDays == 0 {
		return "Floor: no GS minimum configured on any measured day.\n"
	}
	bound := fmt.Sprintf("%d", s.FloorMax)
	if s.FloorMin != s.FloorMax {
		bound = fmt.Sprintf("%d–%d", s.FloorMin, s.FloorMax)
	}
	if s.ProtectedStarts == 0 {
		return fmt.Sprintf("Floor: GS minimum %s/wk in force; no start needed protecting.\n", bound)
	}
	return fmt.Sprintf("Floor: GS minimum %s/wk; %d start(s) kept off the chopping block to\nstay reachable, %.1f gross projected pts.\n",
		bound, s.ProtectedStarts, s.ProtectedPts)
}

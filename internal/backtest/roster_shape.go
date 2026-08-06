package backtest

import "time"

// SideShape holds one role's projected value over a window: what the roster
// could have fielded, and what it did field.
//
// Both terms are restricted to DEPLOYABLE value (see hitterIsDeployable /
// pitcherIsDeployable). That restriction is the whole content of the measure —
// an unrestricted denominator produces a number that looks like roster shape
// and is actually rotation cadence.
type SideShape struct {
	OwnedPts   float64
	FieldedPts float64
}

// StrandedPts is deployable projected value the roster owned and did not field.
//
// It is directly comparable between the two sides — same unit, same window —
// but the two must never be SUMMED, because they are not the same kind of loss.
// A benched hitter starts tomorrow; the pool rotates. A start declined above
// the weekly game-start cap is dead for the week, since GS budget is
// use-it-or-lose-it. This is the same asymmetry GSGateReport.SuppressedPts
// documents as GROSS-not-net.
func (s SideShape) StrandedPts() float64 { return s.OwnedPts - s.FieldedPts }

// FieldedRate is the share of deployable projected value the roster fielded.
// ok is false when there was no deployable value at all, which callers must
// render as "undefined" rather than as 0% — a window in which nothing could be
// fielded is not a window in which everything was stranded.
//
// This is the normalization the whole report rests on: each side is measured
// against its OWN owned value, never against the other side. A roster carrying
// exactly its slot count reads 100% on both sides whether the league fields 13
// hitters or 9, so the gap between the two rates cannot be recovered from the
// league's slot split.
func (s SideShape) FieldedRate() (float64, bool) {
	if s.OwnedPts <= 0 {
		return 0, false
	}
	return s.FieldedPts / s.OwnedPts, true
}

// statusIsRosterable reports whether a roster status describes a player who
// could have been fielded that day. Injured Reserve and Minors are excluded:
// their value is unavailable for reasons that are not the league's roster
// shape, and counting them would report an injury as a structural surplus.
//
// An empty status is a snapshot written before Status was persisted. It is
// excluded here, and SummarizeRosterShape detects those days explicitly rather
// than letting them fall through as zero-value days.
func statusIsRosterable(status string) bool {
	return status == "Active" || status == "Reserve"
}

// hitterIsDeployable reports whether a hitter's projection counts toward owned
// value on the snapshot's date. Hitters appear in essentially every game their
// team plays, so HasGame is the right availability test for them.
func hitterIsDeployable(p SnapshotPlayer) bool {
	return statusIsRosterable(p.Status) && p.HasGame
}

// pitcherIsDeployable reports whether a pitcher's projection counts toward
// owned value on the snapshot's date.
//
// HasGame is NOT the test for a starter. It means only that the pitcher's MLB
// team plays, and SnapshotPlayer.ProjPtsPerGame is the undiscounted projection
// (NonStarterSPDiscount is applied downstream of the snapshot, inside
// OptimizePitcherLineup's local slice). Counting an ace's full projection on
// his four rest days out of five would make every roster in the league report
// roughly the same rate — a measurement of rotation cadence, not roster shape.
//
// IsStarter||GSSuppressed reconstructs the PRE-gate probable-starter set, since
// applyGSGate flips IsStarter to false on the starts it declines. That is also
// what makes every start reported by FormatGateSummary appear in this side's
// stranded total.
//
// The branch keys on Role, which buildSnapshot sets to "SP" when PosShortNames
// contains SP — so a player with any SP eligibility takes the starter branch,
// matching how the gate itself treats them.
func pitcherIsDeployable(p SnapshotPlayer) bool {
	if !statusIsRosterable(p.Status) {
		return false
	}
	if p.Role == "SP" {
		return p.IsStarter || p.GSSuppressed
	}
	return p.HasGame
}

// RosterShape reports how much deployable projected value a roster fielded on
// each side of the ball over a window, against the league's slot counts and
// weekly game-start cap.
//
// It is the analytical companion to GateSummary: the gate measures which starts
// the cap declined, this measures the structural imbalance that keeps producing
// them. The two print together.
type RosterShape struct {
	// Days is the size of the window asked for.
	Days int
	// DaysWithSnapshot is how many of those days had a snapshot that was fresh
	// (same Eastern-time calendar day, per sameETDate) AND carried roster
	// status. Only these contribute to the totals.
	DaysWithSnapshot int
	// DaysStale is how many days had a snapshot whose GeneratedAt falls on a
	// different ET calendar day than the date it projects — a --matchup
	// pre-write never overwritten by that day's own run. Its roster state
	// belongs to a different day, so it is excluded.
	DaysStale int
	// DaysPreSchema is how many days had a fresh snapshot written before
	// SnapshotPlayer.Status existed. Excluded, and counted separately, because
	// an empty status fails the deployable filter for every player — leaving a
	// day with zero owned value that is otherwise indistinguishable from a day
	// measured as fully fielded.
	DaysPreSchema int

	HitterSlots, PitcherSlots int

	// GSCapMin/GSCapMax bound the weekly game-start cap over the counted days.
	// They range over NON-ZERO caps only: a day with GS tracking disabled
	// records 0, and letting that into the minimum would render "GS cap
	// 0–18/wk". Both zero means no counted day recorded a cap at all.
	GSCapMin, GSCapMax int

	Hitters, Pitchers SideShape
}

// SummarizeRosterShape reads the projection snapshots for the given dates and
// totals deployable versus fielded projected value on each side.
//
// It is a second, independent pass over the same snapshots SummarizeGSGate
// reads. They are kept separate so each owns its own failure policy and doc
// burden; the cost is one extra read of a handful of small JSON files.
//
// hitterSlots and pitcherSlots are carried through for rendering only — they
// are never divided by. Normalizing by slot count is exactly the reduction this
// measure exists to avoid (see SideShape.FieldedRate).
func SummarizeRosterShape(dir string, dates []time.Time, hitterSlots, pitcherSlots int) RosterShape {
	s := RosterShape{Days: len(dates), HitterSlots: hitterSlots, PitcherSlots: pitcherSlots}

	for _, d := range dates {
		snap, ok := LoadSnapshot(dir, d)
		if !ok {
			continue
		}
		// A zero GeneratedAt predates the field and can't be judged for
		// staleness, so it is treated as fresh — matching RunProjectionAnalysis
		// and SummarizeGSGate.
		if !snap.GeneratedAt.IsZero() && !sameETDate(snap.GeneratedAt, d) {
			s.DaysStale++
			continue
		}
		if isPreStatusSnapshot(snap) {
			s.DaysPreSchema++
			continue
		}
		s.DaysWithSnapshot++

		for _, h := range snap.Hitters {
			if !hitterIsDeployable(h) {
				continue
			}
			s.Hitters.OwnedPts += h.ProjPtsPerGame
			if h.WasStarted {
				s.Hitters.FieldedPts += h.ProjPtsPerGame
			}
		}
		for _, p := range snap.Pitchers {
			if !pitcherIsDeployable(p) {
				continue
			}
			s.Pitchers.OwnedPts += p.ProjPtsPerGame
			if p.WasStarted {
				s.Pitchers.FieldedPts += p.ProjPtsPerGame
			}
		}

		if snap.GSLimit > 0 {
			if s.GSCapMin == 0 || snap.GSLimit < s.GSCapMin {
				s.GSCapMin = snap.GSLimit
			}
			if snap.GSLimit > s.GSCapMax {
				s.GSCapMax = snap.GSLimit
			}
		}
	}
	return s
}

// isPreStatusSnapshot reports whether a snapshot was written before
// SnapshotPlayer.Status existed.
//
// The test is all-or-nothing rather than "any player missing a status" because
// that is the true shape of the transition: Status is copied straight off the
// roster for every player, so a real write sets it for all of them or the code
// predates the field entirely. A snapshot with no players at all is not marked
// pre-schema — it contributes nothing either way, and mislabelling it would
// misreport why the window is thin.
func isPreStatusSnapshot(s Snapshot) bool {
	seen := 0
	for _, p := range s.Hitters {
		if p.Status != "" {
			return false
		}
		seen++
	}
	for _, p := range s.Pitchers {
		if p.Status != "" {
			return false
		}
		seen++
	}
	return seen > 0
}

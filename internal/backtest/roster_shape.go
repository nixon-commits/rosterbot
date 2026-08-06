package backtest

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

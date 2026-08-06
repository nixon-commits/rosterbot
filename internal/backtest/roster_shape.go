package backtest

import (
	"fmt"
	"strings"
	"time"
)

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

	// DeployableCount is how many player-days entered OwnedPts — passed the
	// role's deployable predicate. RosteredCount is how many player-days
	// appeared in the counted snapshots at all, deployable or not. Their
	// ratio is the measure's COVERAGE, and it is load-bearing for
	// interpretation: a rate computed over a small slice of the roster can
	// reflect availability cadence rather than roster balance. Measured
	// against 2026-08-06 production data, the pitcher denominator (SP:
	// IsStarter||GSSuppressed; RP: HasGame) admitted 2 of 13 rostered
	// pitchers — a FieldedRate on that base is a near-single-player fact
	// wearing a percentage, and a reader has no way to see that without
	// these two counts printed alongside it.
	DeployableCount int
	RosteredCount   int
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
// applyGSGate flips IsStarter to false on the starts it declines. That puts
// every start FormatGateSummary reports into this side's OWNED total — it does
// NOT put them into stranded. Whether a gate-declined start lands in stranded
// depends on what happened next: applyGSGate discounts the pitcher to 0.05x
// but OptimizePitcherLineup still adds him to the slot-assignment pool if he
// has a game, so on a light slate the candidate pool fits inside the 6 P
// slots, the suppressed SP is slotted anyway, WasStarted comes back true, and
// his full projection lands in fielded, not stranded.
//
// The branch keys on Role, which buildSnapshot sets to "SP" when PosShortNames
// contains SP — so a player with any SP eligibility takes the starter branch,
// matching how the gate itself treats them.
//
// buildSnapshot derives Role from PosShortNames alone, but the optimizer's own
// SP test is broader: IsSPEligible(sp.Player.Positions) ||
// strings.Contains(PosShortNames, "SP") (internal/optimizer/pitcher_lineup.go).
// The two agree today only because every pitcher's Eligibility in real
// snapshots is ["017"], so IsSPEligible is always false. If Fantrax ever
// emits the "015" SP position ID, such a pitcher would get Role == "RP" here,
// fall to the HasGame branch below, and contribute his full undiscounted
// projection on every rest day — the exact rotation-cadence failure this
// function exists to prevent, and it would fail open (silently inflating
// owned/fielded) rather than erroring.
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
			s.Hitters.RosteredCount++
			if !hitterIsDeployable(h) {
				continue
			}
			s.Hitters.DeployableCount++
			s.Hitters.OwnedPts += h.ProjPtsPerGame
			if h.WasStarted {
				s.Hitters.FieldedPts += h.ProjPtsPerGame
			}
		}
		for _, p := range snap.Pitchers {
			s.Pitchers.RosteredCount++
			if !pitcherIsDeployable(p) {
				continue
			}
			s.Pitchers.DeployableCount++
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

// FormatRosterShape renders the roster-shape section of the backtest report.
//
// The prose is part of the measure, not decoration: a bare pair of percentages
// invites exactly the two misreadings the design rules out — that the gap is
// the league's slot ratio, and that the two stranded figures can be added into
// a weekly loss.
func FormatRosterShape(s RosterShape) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nROSTER SHAPE — value owned vs value the league lets you field\n")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 64))
	fmt.Fprintf(&b, "%d hitter slots · %d pitcher slots · %s\n",
		s.HitterSlots, s.PitcherSlots, formatGSCap(s))

	if s.DaysWithSnapshot == 0 {
		fmt.Fprintf(&b, "%s\n", noMeasurableDaysReason(s))
		return b.String()
	}

	fmt.Fprintf(&b, "Measured over %d of %d days.%s\n\n", s.DaysWithSnapshot, s.Days, excludedClause(s))
	fmt.Fprint(&b, formatSideLine("Hitters", s.Hitters))
	fmt.Fprint(&b, formatSideLine("Pitchers", s.Pitchers))
	fmt.Fprintf(&b, "\nCoverage: hitters %d of %d rostered player-days deployable · pitchers %d of %d.\n",
		s.Hitters.DeployableCount, s.Hitters.RosteredCount,
		s.Pitchers.DeployableCount, s.Pitchers.RosteredCount)
	fmt.Fprintf(&b, "A starter counts as deployable only on days he was a probable start, so the\n")
	fmt.Fprintf(&b, "pitcher denominator is narrow — read its rate against that count, not alone.\n")
	fmt.Fprintf(&b, "\nEach side is normalized against its own owned value, not against the\n")
	fmt.Fprintf(&b, "other, so the gap is not the %d:%d slot ratio — a roster carrying exactly\n",
		s.HitterSlots, s.PitcherSlots)
	fmt.Fprintf(&b, "its slots reads 100%% on both sides.\n")
	fmt.Fprintf(&b, "Hitter stranding rotates day to day; pitcher stranding above the cap is\n")
	fmt.Fprintf(&b, "dead for the week. The two are not summed.\n")
	return b.String()
}

func formatGSCap(s RosterShape) string {
	switch {
	case s.GSCapMax == 0:
		return "GS cap not tracked"
	case s.GSCapMin == s.GSCapMax:
		return fmt.Sprintf("GS cap %d/wk", s.GSCapMax)
	default:
		return fmt.Sprintf("GS cap %d–%d/wk", s.GSCapMin, s.GSCapMax)
	}
}

// formatSideLine renders one role's row, or says plainly that the side had no
// deployable value. It must never fall back to 0%: no value to field and all
// value stranded are opposite readings.
func formatSideLine(label string, side SideShape) string {
	rate, ok := side.FieldedRate()
	if !ok {
		return fmt.Sprintf("%-10s no deployable value in window\n", label)
	}
	return fmt.Sprintf("%-10s fielded %3.0f%% of owned projected value   (%.1f stranded)\n",
		label, rate*100, side.StrandedPts())
}

// excludedClause names days dropped from the sample, so a window thinned by
// failed runs or by pre-schema history is visible rather than silently
// shrinking the denominator.
func excludedClause(s RosterShape) string {
	var parts []string
	if s.DaysStale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", s.DaysStale))
	}
	if s.DaysPreSchema > 0 {
		parts = append(parts, fmt.Sprintf("%d predating roster-status capture", s.DaysPreSchema))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf(" Excluded: %s.", strings.Join(parts, ", "))
}

func noMeasurableDaysReason(s RosterShape) string {
	switch {
	case s.DaysStale > 0 && s.DaysPreSchema > 0:
		return fmt.Sprintf("No measurable days: %d stale, %d predate roster-status capture.",
			s.DaysStale, s.DaysPreSchema)
	case s.DaysPreSchema > 0:
		return fmt.Sprintf("All %d day(s) with snapshots predate roster-status capture — nothing to report.",
			s.DaysPreSchema)
	case s.DaysStale > 0:
		return fmt.Sprintf("All %d day(s) with snapshots are stale (never overwritten by that day's own run) — nothing to report.",
			s.DaysStale)
	default:
		return fmt.Sprintf("No snapshots on disk for these %d days — nothing to report.", s.Days)
	}
}

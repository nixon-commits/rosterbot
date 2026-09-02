package optimizer

import (
	"math"
	"sort"
	"strings"
	"time"
)

// GSBudget carries weekly game-start budget state for pitcher optimization.
// A nil *GSBudget means no GS limit is configured.
type GSBudget struct {
	Limit int       // max GS per matchup week (fetched live from Fantrax's per-period config)
	Floor int       // min GS per matchup week (0 = unset); live-fetched like Limit, never a constant
	Used  int       // GS already consumed this matchup week (past days)
	Today time.Time // the date being optimized

	WeekEnd  time.Time // last day of matchup week (inclusive)
	Forecast []DayForecast

	// TodayUnsettled counts our SPs confirmed as probable starters TODAY whose
	// starts Used does not yet reflect. It exists because today falls into a
	// seam: Forecast begins TOMORROW (buildGSForecast iterates today+1..weekEnd)
	// and Used only picks a day up once Fantrax settles that day's YTD GS
	// deltas, hours after the games are played. For the whole daytime window
	// today's starts are therefore counted nowhere, which made the floor
	// alert's shortfall spike at every day roll and decay every evening on
	// unchanged roster facts (rosterbot-ogtq).
	//
	// It counts only UNLOCKED confirmed starters, so it collapses to 0 as those
	// games begin — which is the same boundary Used fills in from the other
	// side. Fantrax locks a player when his game is in progress OR final, so a
	// start can briefly be locked-but-not-yet-settled and get counted by
	// neither. That residual error is bounded by a single evening's starts and
	// runs in the CONSERVATIVE direction (a shortfall read slightly high, never
	// slightly low), which is the right way to be wrong for an alert whose
	// design rationale is that a missed week is worse than a loud one.
	TodayUnsettled int
}

// DayForecast holds the projected SP starts for a single future day.
// ConfirmedStarters holds projected fantasy-points-per-game for each of our
// rostered SPs listed as probable starters for that day. Estimated is a
// fractional count of rotation-math-estimated starters used when probables
// haven't been announced yet (value unknown).
type DayForecast struct {
	Date              time.Time
	ConfirmedStarters []float64
	Estimated         float64
}

// GSGateReport records what the weekly game-start budget cost on one date: the
// starts the gate declined, plus the budget state that forced the decision.
//
// The gate has always known this; before rosterbot-i1c it discarded the answer
// and the display layer re-derived an approximation of it by inference.
type GSGateReport struct {
	Suppressed []SuppressedStart
	// Protected are today's starts the gate declined to suppress because the
	// week would otherwise be unable to reach the league's GS MINIMUM. It is
	// disjoint from Suppressed by construction.
	//
	// Protection is the only floor-side lever the optimizer actually has.
	// It cannot make a pitcher start — MLB names the probables — so it cannot
	// manufacture a start on a day when none of our arms has the ball. What it
	// can do is refuse to spend the ceiling-side gate against a week that is
	// already short. Measured on scoring period 20, both zero-start days had
	// our clubs playing and other pitchers starting, so no promotion rule
	// could have raised that week's count; only not-declining can (dpm).
	Protected []SuppressedStart
	// Budget state echoed so a consumer can report the cost and its cause
	// together without also carrying the *GSBudget.
	Limit, Used, Remaining int
	// Floor is the league's weekly GS minimum in force (0 = unset) and
	// FloorNeed is how many more starts the week still needs to reach it.
	// Both are populated on every gate run, including runs that suppress
	// nothing, so the summary can report floor state on a quiet day rather
	// than only when something fired.
	Floor, FloorNeed int
}

// SuppressedStart is one start the gate declined to spend budget on.
type SuppressedStart struct {
	PlayerID     string
	Name         string
	ProjectedPts float64 // blended pts/game at the moment of suppression
}

// SuppressedPts is the GROSS projected value of the starts the gate declined.
//
// It is NOT a net loss to the week, and must not be subtracted from a realised
// total. The budget is not destroyed by a suppression — it is spent on a
// higher-ranked start instead, which is the entire point of the ranked cut.
//
// What it measures is SP capacity the roster owns and the league will not let
// it deploy: 13 active hitter slots against 6 undifferentiated P slots plus a
// weekly game-start cap mean that above the cap an extra ace start produces
// exactly zero marginal points. A gate that fires regularly is the measurement
// of that surplus.
func (r GSGateReport) SuppressedPts() float64 {
	var total float64
	for _, s := range r.Suppressed {
		total += s.ProjectedPts
	}
	return total
}

// ProtectedPts is the GROSS projected value of the starts the gate declined to
// suppress on the floor's behalf. Same accounting caveat as SuppressedPts: it
// is not a gain to the week, it is value that stayed deployed.
func (r GSGateReport) ProtectedPts() float64 {
	var total float64
	for _, s := range r.Protected {
		total += s.ProjectedPts
	}
	return total
}

// Remaining returns how many GS are available from today onward.
func (b *GSBudget) Remaining() int {
	if b == nil || b.Limit == 0 {
		return math.MaxInt32
	}
	r := b.Limit - b.Used
	if r < 0 {
		return 0
	}
	return r
}

// NeedForFloor returns how many more game starts the week still needs to reach
// the league's weekly minimum. It is the floor-side counterpart to Remaining():
// Remaining says how many starts we MAY still spend, this says how many we must
// still make.
//
// Zero when no floor is configured, and zero once the floor is met — a met
// floor imposes no constraint, so the gate's ceiling-side behaviour is
// unchanged on every week that is comfortably clear of it.
func (b *GSBudget) NeedForFloor() int {
	if b == nil || b.Floor == 0 {
		return 0
	}
	need := b.Floor - b.Used
	if need < 0 {
		return 0
	}
	return need
}

// FutureDemand returns the projected count of SP starts on days strictly
// after today through the end of the matchup week.
//
// A day's two regimes ADD, they do not substitute: ConfirmedStarters covers the
// clubs that have named a starter and Estimated covers the clubs that have not,
// so they describe disjoint sets of our pitchers (rosterbot-1jvj). Preferring
// confirmed when non-empty discarded the estimate on every partially-announced
// day — and since MLB trickles probables out days ahead, that is nearly every
// day of the week.
func (b *GSBudget) FutureDemand() float64 {
	if b == nil {
		return 0
	}
	var total float64
	for _, f := range b.Forecast {
		if !f.Date.After(b.Today) {
			continue
		}
		total += float64(len(f.ConfirmedStarters)) + f.Estimated
	}
	return total
}

// applyGSGate suppresses IsStarter for today's SPs when the remaining weekly
// GS budget cannot cover every planned start. Rather than proportionally
// splitting by count across days, the gate ranks all known starter values
// (today + future confirmed) against each other, treating estimated future
// starters as placeholders at the roster-SP mean. Today's starters that fall
// outside the top `remaining` by value are suppressed, letting the existing
// NonStarterSPDiscount downstream shift them to bench.
//
// Locked players are never suppressed: a locked active-slot SP has already
// consumed its GS (reflected in budget.Used) and a locked bench SP can't
// be moved into an active slot, so either way gate suppression has no
// practical effect and only misleads the displayed pts/gate delta. Suppressed
// starters fall to NonStarterSPDiscount downstream.
//
// The returned GSGateReport names every start the gate declined. It is the
// authoritative account — downstream must not re-derive suppression by
// comparing probable-starter membership against IsStarter.
func applyGSGate(scored []ScoredPitcher, budget *GSBudget) ([]ScoredPitcher, GSGateReport) {
	if budget == nil || budget.Limit == 0 {
		return scored, GSGateReport{}
	}

	remaining := budget.Remaining()
	floorNeed := budget.NeedForFloor()
	report := GSGateReport{
		Limit: budget.Limit, Used: budget.Used, Remaining: remaining,
		Floor: budget.Floor, FloorNeed: floorNeed,
	}
	if remaining <= 0 {
		for i := range scored {
			// Locked players' GS is already decided (either consumed in an
			// active slot or permanently unconsumed on bench) — flipping
			// their IsStarter has no effect on the lineup and only misleads
			// the display and pts calculation.
			if scored[i].Player.Locked {
				continue
			}
			if scored[i].IsStarter {
				report.Suppressed = append(report.Suppressed, SuppressedStart{
					PlayerID:     scored[i].Player.ID,
					Name:         scored[i].Player.Name,
					ProjectedPts: scored[i].ExpectedPts,
				})
			}
			scored[i].IsStarter = false
		}
		return scored, report
	}

	type starterRef struct {
		idx int
		pts float64
	}
	// Only unlocked today starters are candidates for suppression. Locked
	// active SPs have already consumed their GS (counted in budget.Used);
	// locked bench SPs can't move into an active slot at all. Either way
	// they don't compete for remaining budget.
	var todayStarters []starterRef
	for i, sp := range scored {
		if sp.IsStarter && !sp.Player.Locked {
			todayStarters = append(todayStarters, starterRef{i, sp.ExpectedPts})
		}
	}
	if len(todayStarters) == 0 {
		return scored, report
	}

	// Confirmed and estimated ADD within a day — they describe disjoint sets of
	// our pitchers (named clubs vs unnamed ones), so an either/or read here
	// would drop the estimate on every partially-announced day and let the gate
	// under-count planned starts (rosterbot-1jvj). This must stay in step with
	// FutureDemand, which the operator display reports.
	var futureConfirmed []float64
	var futureEstimated float64
	for _, f := range budget.Forecast {
		if !f.Date.After(budget.Today) {
			continue
		}
		futureConfirmed = append(futureConfirmed, f.ConfirmedStarters...)
		futureEstimated += f.Estimated
	}

	totalKnown := len(todayStarters) + len(futureConfirmed)
	totalPlanned := float64(totalKnown) + futureEstimated

	const eps = 1e-9
	if float64(remaining)+eps >= totalPlanned {
		return scored, report
	}

	// Placeholder value for estimated (unknown-value) future starters: the mean
	// projected pts across rostered SPs with a usable projection. A pitcher
	// with an unknown projection (0 pts) is excluded so the placeholder
	// represents "a real SP we might start," not a scrub.
	var placeholder float64
	var spCount int
	var spSum float64
	for _, sp := range scored {
		if !strings.Contains(sp.Player.PosShortNames, "SP") {
			continue
		}
		if sp.ExpectedPts <= 0 {
			continue
		}
		spSum += sp.ExpectedPts
		spCount++
	}
	if spCount > 0 {
		placeholder = spSum / float64(spCount)
	}

	type rankEntry struct {
		pts         float64
		isToday     bool
		isEstimated bool // true only for placeholder (unconfirmed) future entries
		todayRef    int  // index into todayStarters; -1 for non-today entries
		tiebreak    string
	}

	// certaintyMargin: an estimated (unconfirmed) future claim must exceed
	// today's value by more than this fraction before it outranks a start
	// that's already happening — extends the exact-tie "prefer today" rule
	// below to near-ties. Confirmed future starts are just as certain as
	// today's and are deliberately excluded from this leniency.
	//
	// What it prices is the chance the estimated start never materializes at
	// all: budget is use-it-or-lose-it weekly, so a slot held for a start that
	// doesn't happen is burned outright, while a slot spent today is banked.
	// That asymmetry is the entire justification, and it is why the margin is
	// signed toward today rather than toward the option value of waiting.
	//
	// It is NOT a haircut for projection error. SP projections carry a large
	// MAE (10.4–13.8 FP/G against an SP mean near 12), but that dispersion is
	// symmetric: an unbiased noisy estimate is still the best estimate, and
	// discounting it would bias the ranking rather than de-risk it. A proposal
	// to re-derive this constant from measured MAE was rejected on those
	// grounds (rosterbot-idn).
	//
	// 0.10 is unmeasured — a plausible magnitude for P(no start materializes),
	// not a fitted value. Sweeping it against real season outcomes is
	// rosterbot-xsm; until then, treat it as a prior, not a result.
	const certaintyMargin = 0.10

	estCount := int(math.Ceil(futureEstimated))
	entries := make([]rankEntry, 0, totalKnown+estCount)
	for i, s := range todayStarters {
		entries = append(entries, rankEntry{
			pts:      s.pts,
			isToday:  true,
			todayRef: i,
			tiebreak: scored[s.idx].Player.ID,
		})
	}
	for _, p := range futureConfirmed {
		entries = append(entries, rankEntry{pts: p, isToday: false, todayRef: -1})
	}
	// Distribute the fractional estimate across ceil(futureEstimated) entries
	// so its total value mass equals placeholder*futureEstimated exactly,
	// instead of ceil-rounding a fractional demand (e.g. 0.2) into one
	// FULL-value entry that competes on equal footing with certain starts.
	//
	// The weight belongs in the PRICE, and it belongs there precisely BECAUSE
	// the cut below spends a whole slot. This reads like a units error — every
	// other entry is points-per-start, this one is points×probability — and it
	// has been reported as one (rosterbot-idn). It is not. The gate's question
	// is marginal: hold this slot, or spend it today? Holding is worth
	// placeholder × min(E,1) in expectation; spending is worth the starter's
	// value with certainty. Those are the two quantities that must be
	// comparable, so the weighted entry IS the correctly-priced one.
	//
	// Concretely, with 1 slot left, a certain 8-pt start today and 0.2 expected
	// future starts at a 12-pt placeholder: pricing the tail at 12 wins the cut
	// and benches the 8, holding a slot that materializes 20% of the time —
	// 2.4 expected against 8 certain, and weekly GS is use-it-or-lose-it, so
	// the other 80% is burned rather than banked. Pricing it at 12×0.2 = 2.4
	// loses the cut, which is the right call.
	// TestApplyGSGate_FractionalEstimateDoesNotOverSuppressToday pins that case;
	// TestApplyGSGate_EstimatedLadderPricesWholeUnitsFullAndTailMarginally pins
	// both rungs together.
	//
	// The weights are the exact marginal values, not an approximation of one:
	// the k-th held slot is worth placeholder × min(max(E-(k-1), 0), 1), which
	// is what this loop emits (1.0, 1.0, …, frac).
	remainingFrac := futureEstimated
	for i := 0; i < estCount; i++ {
		weight := 1.0
		if remainingFrac < 1.0 {
			weight = remainingFrac
		}
		entries = append(entries, rankEntry{pts: placeholder * weight, isToday: false, isEstimated: true, todayRef: -1})
		remainingFrac -= 1.0
	}

	sort.SliceStable(entries, func(i, j int) bool {
		ei, ej := entries[i], entries[j]
		if ei.isToday != ej.isToday && (ei.isEstimated || ej.isEstimated) {
			todayPts, futurePts := ei.pts, ej.pts
			todayIsI := ei.isToday
			if !todayIsI {
				todayPts, futurePts = ej.pts, ei.pts
			}
			if futurePts > todayPts*(1+certaintyMargin)+eps {
				return !todayIsI
			}
			return todayIsI
		}
		if math.Abs(ei.pts-ej.pts) > eps {
			return ei.pts > ej.pts
		}
		// On exact ties, prefer today (certain start) over future entries.
		if ei.isToday != ej.isToday {
			return ei.isToday
		}
		return ei.tiebreak < ej.tiebreak
	})

	keepToday := make(map[int]bool, len(todayStarters))
	for i := 0; i < remaining && i < len(entries); i++ {
		if entries[i].isToday {
			keepToday[entries[i].todayRef] = true
		}
	}

	// FLOOR SIDE. The cut above is purely ceiling-driven: it asks which starts
	// are worth the remaining budget. It never asks whether declining them
	// leaves the week short of the league's MINIMUM, and a team that finishes
	// under the floor takes a penalty regardless of how well it spent what it
	// did use.
	//
	// floorReserve is how many of today's starts must survive the cut for the
	// floor to stay reachable: what the week still needs, less the starts
	// already CONFIRMED for later in the week.
	//
	// Only futureConfirmed is subtracted, never futureEstimated. Estimated is
	// rotation math over clubs that have not named a starter — a ~1-in-5 claim
	// on a pitcher who may never take an active-slot turn (rosterbot-tmb9).
	// Spending a certain start today against a speculative one later is
	// exactly how a floor-bound week ends up under the floor, and weekly GS is
	// use-it-or-lose-it, so the unspent probability is burned rather than
	// banked. The floor is the one place in this gate that must not be staked
	// on an estimate.
	//
	// The clamps are what make the ceiling constraint provably safe. Capping at
	// `remaining` means the post-top-up keepToday can never exceed `remaining`:
	// the loop above put at most `remaining` entries in, and the top-up raises
	// the count to at most floorReserve <= remaining. So protection can never
	// push the week past Limit, which is not an assertion here but a
	// consequence of the bound — pinned by
	// TestApplyGSGate_FloorNeverProtectsPastLimit.
	var protectedRefs []int
	if floorNeed > 0 {
		floorReserve := floorNeed - len(futureConfirmed)
		if floorReserve > len(todayStarters) {
			floorReserve = len(todayStarters)
		}
		if floorReserve > remaining {
			floorReserve = remaining
		}
		// Top up in the same value order the cut used, so protection promotes
		// the most valuable declined start first and no re-sort is needed.
		for i := 0; i < len(entries) && len(keepToday) < floorReserve; i++ {
			if !entries[i].isToday || keepToday[entries[i].todayRef] {
				continue
			}
			keepToday[entries[i].todayRef] = true
			protectedRefs = append(protectedRefs, entries[i].todayRef)
		}
	}
	protected := make(map[int]bool, len(protectedRefs))
	for _, ref := range protectedRefs {
		protected[ref] = true
	}

	for i, s := range todayStarters {
		if protected[i] {
			report.Protected = append(report.Protected, SuppressedStart{
				PlayerID:     scored[s.idx].Player.ID,
				Name:         scored[s.idx].Player.Name,
				ProjectedPts: scored[s.idx].ExpectedPts,
			})
		}
		if !keepToday[i] {
			report.Suppressed = append(report.Suppressed, SuppressedStart{
				PlayerID:     scored[s.idx].Player.ID,
				Name:         scored[s.idx].Player.Name,
				ProjectedPts: scored[s.idx].ExpectedPts,
			})
			scored[s.idx].IsStarter = false
		}
	}
	return scored, report
}

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
	Limit    int       // max GS per matchup week (fetched live from Fantrax's per-period config)
	Used     int       // GS already consumed this matchup week (past days)
	Today    time.Time // the date being optimized
	WeekEnd  time.Time // last day of matchup week (inclusive)
	Forecast []DayForecast
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

// FutureDemand returns the projected count of SP starts on days strictly
// after today through the end of the matchup week. For each day, prefers the
// count of confirmed probables; falls back to the rotation-math estimate when
// no probables are announced.
func (b *GSBudget) FutureDemand() float64 {
	if b == nil {
		return 0
	}
	var total float64
	for _, f := range b.Forecast {
		if !f.Date.After(b.Today) {
			continue
		}
		if len(f.ConfirmedStarters) > 0 {
			total += float64(len(f.ConfirmedStarters))
		} else {
			total += f.Estimated
		}
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
func applyGSGate(scored []ScoredPitcher, budget *GSBudget) []ScoredPitcher {
	if budget == nil || budget.Limit == 0 {
		return scored
	}

	remaining := budget.Remaining()
	if remaining <= 0 {
		for i := range scored {
			// Locked players' GS is already decided (either consumed in an
			// active slot or permanently unconsumed on bench) — flipping
			// their IsStarter has no effect on the lineup and only misleads
			// the display and pts calculation.
			if scored[i].Player.Locked {
				continue
			}
			scored[i].IsStarter = false
		}
		return scored
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
		return scored
	}

	var futureConfirmed []float64
	var futureEstimated float64
	for _, f := range budget.Forecast {
		if !f.Date.After(budget.Today) {
			continue
		}
		if len(f.ConfirmedStarters) > 0 {
			futureConfirmed = append(futureConfirmed, f.ConfirmedStarters...)
		} else {
			futureEstimated += f.Estimated
		}
	}

	totalKnown := len(todayStarters) + len(futureConfirmed)
	totalPlanned := float64(totalKnown) + futureEstimated

	const eps = 1e-9
	if float64(remaining)+eps >= totalPlanned {
		return scored
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

	for i, s := range todayStarters {
		if !keepToday[i] {
			scored[s.idx].IsStarter = false
		}
	}
	return scored
}

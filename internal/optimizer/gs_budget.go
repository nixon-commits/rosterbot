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
	Limit    int       // max GS per matchup week (from GS_MAX)
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
// 0.10x non-starter discount downstream shift them to bench.
//
// Locked players are never suppressed: a locked active-slot SP has already
// consumed its GS (reflected in budget.Used) and a locked bench SP can't
// be moved into an active slot, so either way gate suppression has no
// practical effect and only misleads the displayed pts/gate delta.
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
		pts      float64
		isToday  bool
		todayRef int    // index into todayStarters; -1 for non-today entries
		tiebreak string // stable ordering on ties
	}

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
	for i := 0; i < estCount; i++ {
		entries = append(entries, rankEntry{pts: placeholder, isToday: false, todayRef: -1})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if math.Abs(entries[i].pts-entries[j].pts) > eps {
			return entries[i].pts > entries[j].pts
		}
		// On ties, prefer today (certain start) over future entries.
		if entries[i].isToday != entries[j].isToday {
			return entries[i].isToday
		}
		return entries[i].tiebreak < entries[j].tiebreak
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

package optimizer

import (
	"math"
	"sort"
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

// DayForecast holds the projected number of SP starts for a future day.
type DayForecast struct {
	Date      time.Time
	Confirmed int     // confirmed starters (from probable data)
	Estimated float64 // estimated starters (from rotation math, used when Confirmed==0)
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

// FutureDemand returns the projected number of SP starts on days strictly
// after today through the end of the matchup week.
func (b *GSBudget) FutureDemand() float64 {
	if b == nil {
		return 0
	}
	var total float64
	for _, f := range b.Forecast {
		if !f.Date.After(b.Today) {
			continue
		}
		if f.Confirmed > 0 {
			total += float64(f.Confirmed)
		} else {
			total += f.Estimated
		}
	}
	return total
}

// applyGSGate suppresses IsStarter for the lowest-value SPs when the GS
// budget is tight. It flips IsStarter to false so the existing 0.10x
// non-starter discount kicks in downstream.
//
// The gate is conservative: it only suppresses starters when
// remaining budget <= today's starters + future demand.
func applyGSGate(scored []ScoredPitcher, budget *GSBudget) []ScoredPitcher {
	if budget == nil || budget.Limit == 0 {
		return scored
	}

	remaining := budget.Remaining()
	if remaining <= 0 {
		// Budget fully spent: suppress all starters.
		for i := range scored {
			scored[i].IsStarter = false
		}
		return scored
	}

	// Count today's confirmed starters.
	var todayStarters int
	for _, sp := range scored {
		if sp.IsStarter {
			todayStarters++
		}
	}
	if todayStarters == 0 {
		return scored
	}

	futureDemand := budget.FutureDemand()

	// Allocate remaining GS proportionally across today and future days.
	// This ensures today always gets a fair share rather than hoarding all
	// GS for the future. The highest-value starters are kept (sorted below).
	const eps = 1e-9
	totalDemand := float64(todayStarters) + futureDemand
	slack := float64(remaining) - futureDemand
	var allowToday int
	if slack+eps >= float64(todayStarters) {
		// Enough budget for all today's starters plus future demand.
		return scored
	} else if totalDemand <= eps {
		return scored
	} else {
		// Proportional allocation: today gets its fair share of remaining GS.
		allowToday = int(math.Round(float64(remaining) * float64(todayStarters) / totalDemand))
		if allowToday > todayStarters {
			allowToday = todayStarters
		}
		if allowToday < 0 {
			allowToday = 0
		}
	}

	// Suppress the lowest-scoring starters beyond the allowed count.
	type starterIdx struct {
		idx int
		pts float64
		id  string
	}
	var starters []starterIdx
	for i, sp := range scored {
		if sp.IsStarter {
			starters = append(starters, starterIdx{i, sp.ExpectedPts, sp.Player.ID})
		}
	}

	// Sort descending by pts, then by player ID for stability.
	sort.Slice(starters, func(i, j int) bool {
		if starters[i].pts != starters[j].pts {
			return starters[i].pts > starters[j].pts
		}
		return starters[i].id < starters[j].id
	})

	// Keep the top allowToday starters, suppress the rest.
	for k := allowToday; k < len(starters); k++ {
		scored[starters[k].idx].IsStarter = false
	}
	return scored
}

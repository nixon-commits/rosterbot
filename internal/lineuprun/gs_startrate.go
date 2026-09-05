package lineuprun

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/projections"
)

// gsStartRateWindow is the trailing window, in days, over which a pitcher's own
// active-slot start rate is measured.
//
// 28 days is four rotation turns, and it is bounded above by an engineering
// fact rather than a statistical one: internal/schedule caches a PAST date's
// schedule for 30 days (pastScheduleTTL), so a longer window would re-fetch its
// oldest days from statsapi on every hourly run forever. The measurement says
// nothing is lost by staying inside that — replayed over 180 complete
// team-weeks the whole-week forecast MAE was 1.90 at a 21-day window, 1.89 at
// 28 and 1.90 at 45, i.e. flat within noise. Choose the one that is free.
const gsStartRateWindow = 28

// gsStartRatePrior is the strength of the shrinkage prior, in pseudo-
// opportunities already credited at the flat rate.
//
// It does two jobs with one expression. It bounds the estimator's variance on a
// short sample — a pitcher seen on one club-game-day would otherwise read 1.000
// or 0.000, neither of which is a rate — and it makes "no history at all"
// return EXACTLY 1/RotationSize rather than a special case that some later
// caller could forget. A pitcher with zero opportunities is not in the map at
// all (see computeStartRates), which is the same answer by a different route
// and is what startRateFor's fallback pins.
//
// 2 is deliberately weak. Swept against 180 replayed team-weeks the whole-week
// MAE was 1.83 at prior 0, 1.89 at 2, 2.05 at 4 and 2.54 at 10, against 5.06
// for the flat divisor — so the data prefers no shrinkage at all and the cost
// of a little is negligible. It is here for the single-observation case the
// season-long replay barely contains and a mid-week acquisition produces
// routinely.
const gsStartRatePrior = 2.0

// computeStartRates measures each rostered pitcher's own active-slot start rate
// over the trailing gsStartRateWindow days, keyed by normalized name.
//
// WHY THIS EXISTS. buildGSForecast used to price every rostered SP whose club
// plays at a flat 1-in-5, on the reasoning that a five-man rotation gives each
// member one turn in five. Measured against every frozen per-day Fantrax
// snapshot on disk (2026-03-25..08-09, all ten league teams), the per-pitcher
// rate is nothing like uniform and the flat divisor is not even centred: over
// the 160 pitcher-seasons with at least 20 club-game-day opportunities, mean
// 0.128, sd 0.057, p10 0.032, p90 0.190, max 0.215 — so 1/5 sits above the 94th
// percentile of the observed distribution and 26.2% of pitchers convert at
// under half of it. The forecast was biased, not merely noisy: predicting
// each week from its Monday, the flat divisor forecast 14.70 starts against
// 9.64 realised (bias +5.06, MAE 5.06) where this estimator forecasts 10.89
// (bias +1.25, MAE 1.89). Replaying the gate's own `remaining < demand` test
// and the floor alert's `Need` vs credited-supply test over 1,080 in-week
// decision points, 30.7% of gate decisions and 15.1% of floor decisions flip.
// The harness is internal/lineuprun/diag_startrate_test.go (build tag `diag`).
//
// WHAT IT IS NOT. It is not the Status == "Active" filter rosterbot-tmb9
// rejected. That one drops bench arms from the forecast entirely and would have
// missed four of the five pitchers who actually started on 2026-08-19; this
// keeps every one of them in the denominator and merely prices them apart.
//
// THE RATE IS ACTIVE-SLOT CONVERSION, not rotation membership, and the
// distinction is forced by the data rather than chosen. The per-period pitcher
// snapshot is fetched with StatsType=2 (fantasy-team stats), so its GS only
// ever advances for a start our roster captured in an active slot — which is
// also, exactly, what spends weekly GS budget (GetTeamGS counts the same
// deltas). The alternative denominator, real-world appearances from the
// StatsType=1 roster-stats GP delta, is unusable here: it counts relief
// outings, and measured over the same window an SP-eligible reliever read 0.333
// against an active-slot rate of 0.008.
//
// That does make the estimator mildly self-referential — a start the gate
// suppresses lowers the pitcher's own future weight. The feedback is NEGATIVE
// and therefore self-stabilising (a lower weight forecasts less demand, which
// suppresses less), and it is measured against the quantity the budget actually
// spends, which is the question the gate asks.
//
// KNOWN LIMITS, both in the benign direction. A pitcher traded between clubs
// mid-window has his whole history measured against his CURRENT club's
// schedule; clubs play about six days in seven regardless, so the denominator
// barely moves. And a genuinely good arm acquired days ago reads low until the
// window fills — but a pitcher about to start is normally already NAMED, which
// puts him in ConfirmedStarters at 1.0 where his rate is not consulted at all.
// Both errors under-forecast demand, which suppresses less; the flat divisor's
// error ran the other way and by four times as much.
func computeStartRates(
	ctx context.Context,
	ft gsFantraxClient,
	sched gsScheduleClient,
	teamID string,
	seasonStart, today time.Time,
	ttl time.Duration,
) (map[string]float64, error) {
	end := today.AddDate(0, 0, -1)
	start := today.AddDate(0, 0, -gsStartRateWindow)
	if start.Before(seasonStart) {
		start = seasonStart
	}
	if end.Before(start) {
		// Opening day and the day after it: no settled history exists yet, and
		// that is an ordinary state rather than a failure. Every SP falls back
		// to the flat rate.
		return nil, nil
	}

	days, err := ft.GetTeamPitcherDays(teamID, start, end, seasonStart, cacheDir, ttl)
	if err != nil {
		return nil, fmt.Errorf("pitcher day walk %s..%s: %w",
			start.Format("2006-01-02"), end.Format("2006-01-02"), err)
	}

	// The denominator is club-game-days, so the schedule is needed for every
	// day of the window. A single failed day ABORTS rather than being skipped:
	// skipping shrinks the denominator for everyone and therefore INFLATES
	// every rate, which is the direction that makes the gate suppress more —
	// the opposite of a safe degradation. Aborting falls back to the flat rate,
	// which is what shipped all season.
	playingOn := make(map[string]map[string]bool, gsStartRateWindow)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		playing, err := sched.TeamsPlayingOn(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("schedule unavailable for %s: %w", d.Format("2006-01-02"), err)
		}
		playingOn[d.Format("2006-01-02")] = playing
	}

	type tally struct{ opportunities, starts int }
	byName := map[string]*tally{}
	for _, pd := range days {
		// A day the pitcher's club did not play is not an opportunity he
		// declined; it is not an opportunity at all. Including it would price
		// every pitcher down by the league's off-day rate and say nothing about
		// any of them.
		if !playingOn[pd.Date.Format("2006-01-02")][pd.MLBTeam] {
			continue
		}
		key := projections.NormalizeName(pd.PitcherName)
		t := byName[key]
		if t == nil {
			t = &tally{}
			byName[key] = t
		}
		t.opportunities++
		if pd.Started {
			t.starts++
		}
	}

	rates := make(map[string]float64, len(byName))
	for key, t := range byName {
		if t.opportunities == 0 {
			continue
		}
		rates[key] = (float64(t.starts) + gsStartRatePrior/RotationSize) /
			(float64(t.opportunities) + gsStartRatePrior)
	}
	return rates, nil
}

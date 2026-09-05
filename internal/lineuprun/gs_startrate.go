package lineuprun

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// gsStartRateWindow is the trailing window, in days, over which a pitcher's own
// active-slot start rate is measured.
//
// 28 days is four rotation turns, and it is bounded above by an engineering
// fact rather than a statistical one: internal/schedule caches a PAST date's
// schedule for 30 days (pastScheduleTTL), so a longer window would re-fetch its
// oldest days from statsapi on every hourly run forever.
//
// AN EARLIER VERSION OF THIS COMMENT CLAIMED THE MEASUREMENT WAS FLAT ACROSS
// WINDOW LENGTHS. It is not. Replayed over 180 complete team-weeks with the
// corrected tally below, whole-week forecast MAE is monotone in the window:
//
//	window   bias    MAE
//	14      +0.69   1.94
//	21      +0.96   2.22
//	28      +1.13   2.39
//	45      +1.49   2.70
//
// (flat 1/5, for scale: +3.60 / 3.70.) Shorter is better, and materially so —
// the 14-vs-28 gap is ten times any of the prior/guard/cap differences swept
// below. The window is deliberately NOT re-chosen here. Two reasons: a 14-day
// window prices a pitcher on under three rotation turns, and the guard that
// makes a short sample safe is the very thing this change introduces, so
// sweeping both at once would leave neither attributable; and the replay runs
// in the open announcement regime (see TestDiagStartRateDecisionReplay), where
// the estimate carries the whole week rather than the unannounced remainder it
// carries in production, so a responsiveness advantage may be partly an
// artefact of the harness. The trend is recorded here rather than acted on,
// which is the honest state of it.
const gsStartRateWindow = 28

// gsStartRatePrior is the strength of the shrinkage prior, in pseudo-
// opportunities already credited at the flat rate.
//
// It does two jobs with one expression. It bounds the estimator's variance on a
// short sample — a pitcher seen on one club-game-day would otherwise read 1.000
// or 0.000, neither of which is a rate — and it makes "no history at all"
// return EXACTLY 1/RotationSize rather than a special case that some later
// caller could forget. A pitcher with zero opportunities is not in the map at
// all (see tallyStartRates), which is the same answer by a different route and
// is what the forecast's own fallback pins.
//
// 2 is deliberately weak, and it is now the SECOND line of defence rather than
// the only one: gsStartRateMinOpportunities refuses to price a pitcher at all
// below one rotation turn of history, which is where an unshrunk estimator does
// its real damage. Swept over the same 180 team-weeks with that guard and the
// cap in place, prior 2 reads bias +1.13 / MAE 2.39 and prior 5 reads +1.40 /
// 2.42 — the heavier prior is worse on both, because shrinking toward 0.20
// pulls every measured pitcher up toward a rate 93.8% of them do not reach.
const gsStartRatePrior = 2.0

// gsStartRateMinOpportunities is the fewest club-game-days of history on which
// a pitcher may be priced from his own record. Below it he falls back to the
// flat 1/RotationSize, exactly as if he had no history at all.
//
// It exists because a shrinkage prior of 2 is not a sample-size guard. Without
// it the estimator issued rates no rotation can produce: over the 180-week
// replay, 35 of the 1560 prices it handed the forecast exceeded 0.25 — one turn
// in four club-games — against a season-long observed maximum, over full
// samples, of 0.216. A pitcher seen once on a day he started read 0.467; a
// rotation regular acquired five days ago who had not yet taken the ball read
// 0.057, under a third of flat. Neither number is a measurement of anything.
//
// 5 is one rotation turn: the smallest window in which a genuine every-fifth-
// day starter is expected to have started once, and therefore the smallest
// window in which "he has not started" is evidence rather than absence of it.
// The sweep agrees without settling it on its own — whole-week MAE is 2.39 at
// minOpps 0, 3 and 5 alike and 2.40 at 8 — so the guard is chosen on the
// physical argument and on what it removes: impossible prices fall 35 → 27 →
// 19 → 5 across those four settings, and 113 of the 1560 prices disappear
// between 0 and 5 because they rested on under a turn of history.
//
//	prior  minOpps  cap    bias    MAE   meanPred  >0.25
//	2      0        0.25  +1.02   2.39    12.04    0/1560 (35 uncapped)
//	2      3        0.25  +1.05   2.39    12.07    0/1512 (27 uncapped)
//	2      5        0.25  +1.13   2.39    12.14    0/1447 (19 uncapped)
//	2      8        0.25  +1.14   2.40    12.16    0/1382 ( 5 uncapped)
//	5      5        0.25  +1.40   2.42    12.42    0/1447 ( 4 uncapped)
const gsStartRateMinOpportunities = 5

// gsStartRateCap is the highest rate this estimator will report: one turn in
// four club-games.
//
// The obvious cap is 1/RotationSize, and the measurement rejects it. The
// denominator here is CLUB-GAME-DAYS, so a pitcher taking every fifth turn
// reads exactly 0.200 — and over the full season three pitchers with real
// samples read above it (Aaron Civale 0.216 on 37 opportunities, Gerrit Cole
// 0.215 on 65, Eury Perez 0.214 on 98). Rotations are not rigid: an off-day
// lets a club skip its fifth starter, and the arms that benefit are exactly
// the aces the gate most needs priced correctly. A 0.20 cap clips 8.3% of all
// prices the replay issues (120 of 1447) and buys 0.06 starts of weekly bias
// and nothing at all in MAE.
//
// 0.25 is the tightest bound no real rotation crosses — a four-man rotation,
// which no modern club runs for long — so anything above it is a short sample
// or a snapshot artefact rather than a pitcher. It removes 19 of those 1447
// prices at no measurable calibration cost (MAE 2.39 capped against 2.38
// uncapped, bias +1.13 either way).
//
// The cap and the minimum-opportunity guard overlap on purpose. The guard
// removes most of the wild rates (35 prices above 0.25 without it, 19 with);
// the cap bounds whatever survives. Both fail in the same direction — toward
// the flat rate that shipped all season — which is the only direction this
// estimator is allowed to be wrong in.
const gsStartRateCap = 0.25

// startRateParams are the estimator's three tunable constants, taken as a value
// so a diagnostic can sweep them against the SHIPPED tally rather than against
// a reimplementation of it.
//
// Production never constructs one of these by hand: shippedStartRateParams is
// the only source, and it reads the constants above.
type startRateParams struct {
	Prior            float64
	MinOpportunities int
	// Cap is the maximum rate reported. Zero means uncapped, which exists for
	// the sweep and never for production.
	Cap float64
}

func shippedStartRateParams() startRateParams {
	return startRateParams{
		Prior:            gsStartRatePrior,
		MinOpportunities: gsStartRateMinOpportunities,
		Cap:              gsStartRateCap,
	}
}

// startRateResult is what the tally knows about the window.
//
// Rate holds only pitchers that cleared MinOpportunities; Opportunities holds
// every pitcher with at least one, priced or not. Keeping both is what lets the
// coverage line say WHY a pitcher fell back to the flat rate — "seen but too
// briefly" and "never seen at all" are different facts, and only one of them is
// about a pitcher who is actually on the roster today.
type startRateResult struct {
	Rate          map[string]float64
	Opportunities map[string]int
}

// tallyStartRates is the pure kernel of the start-rate estimator: the whole
// decision about what counts as an opportunity, and what a tally is worth once
// counted.
//
// It is separated from computeStartRates' fetching for one reason. rosterbot-
// goht measured its estimator in a diagnostic that re-implemented this tally
// and shipped a different one: the harness restricted the denominator to
// SP-eligible, non-IL, non-minors days and refused to credit a first
// appearance, while production counted every rostered pitcher-day whose club
// played. On the same set of pitchers the two disagreed materially — n=160
// mean 0.129 p90 0.192 under the harness's filters against n=225 mean 0.081
// p90 0.178 under production's, with 7.5% of consulted pitcher-days carrying
// more than two IL or minor-league days in the denominator (Gerrit Cole read
// 0.086 on 26 "opportunities" of which 24 were days he could not have pitched
// for us, against 0.600 on the two that were real). Every number in the
// comments around this file is now produced by THIS function: the diagnostic
// feeds frozen snapshots through fantrax.PitcherDayWalk and then through here,
// so it can no longer measure an estimator nobody ships.
//
// The four filters, and why each one is a filter rather than a zero:
//
//   - The club did not play. Not an opportunity he declined; not an
//     opportunity at all. Including it prices every pitcher down by the
//     league's off-day rate and says nothing about any of them.
//   - IL or the minors (StatusID "3"/"9"). He could not have taken an
//     active-slot start on that day whatever his club did, so the day carries
//     no information about his rate — and unlike an off-day it is not shared
//     across the roster, so it silently prices exactly the pitchers coming
//     back from injury lowest.
//   - Not SP-eligible that day. The forecast only ever consults the rate for a
//     pitcher rosterSPNames put in the unannounced bucket, so a day on which he
//     was not eligible to be in it is out of scope by construction.
//   - A first appearance. The row's Started flag is derived from a capped
//     delta against no baseline, so it can report a start that belongs to
//     another team's season while a genuine start is indistinguishable from a
//     bench day. The row is dropped in BOTH directions rather than kept for
//     the half of it that is readable.
//
// playingOn is date ("2006-01-02") → club abbreviation → played.
func tallyStartRates(days []fantrax.PitcherDay, playingOn map[string]map[string]bool, p startRateParams) startRateResult {
	type tally struct{ opportunities, starts int }
	byName := map[string]*tally{}
	for _, pd := range days {
		if pd.FirstAppearance {
			continue
		}
		if pd.StatusID == fantrax.StatusIL || pd.StatusID == fantrax.StatusMinors {
			continue
		}
		if !strings.Contains(pd.PosShortNames, "SP") {
			continue
		}
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

	res := startRateResult{
		Rate:          make(map[string]float64, len(byName)),
		Opportunities: make(map[string]int, len(byName)),
	}
	for key, t := range byName {
		if t.opportunities == 0 {
			continue
		}
		res.Opportunities[key] = t.opportunities
		if t.opportunities < p.MinOpportunities {
			continue
		}
		r := (float64(t.starts) + p.Prior/RotationSize) / (float64(t.opportunities) + p.Prior)
		if p.Cap > 0 && r > p.Cap {
			r = p.Cap
		}
		res.Rate[key] = r
	}
	return res
}

// computeStartRates measures each rostered pitcher's own active-slot start rate
// over the trailing gsStartRateWindow days, keyed by normalized name.
//
// WHY THIS EXISTS. buildGSForecast used to price every rostered SP whose club
// plays at a flat 1-in-5, on the reasoning that a five-man rotation gives each
// member one turn in five. Measured against every frozen per-day Fantrax
// snapshot on disk (2026-03-25..08-16, all ten league teams), the per-pitcher
// rate is nothing like uniform and the flat divisor is not even centred: over
// the 160 pitcher-seasons with at least 20 club-game-day opportunities, mean
// 0.129, sd 0.057, p10 0.032, p25 0.093, median 0.141, p90 0.191, max 0.216 —
// so 1/5 sits above every pitcher measured and 26.2% convert at under half of
// it. The forecast was biased, not merely noisy: predicting each of 180
// Monday-anchored team-weeks from its own Monday, the flat divisor forecast
// 14.62 starts against 11.02 realised (bias +3.60, MAE 3.70) where this
// estimator forecasts 12.14 (bias +1.13, MAE 2.39).
//
// The harness is internal/lineuprun/diag_startrate_test.go (build tag `diag`);
// it drives fantrax.PitcherDayWalk and tallyStartRates, so what it reports is
// what this function computes.
//
// WHAT IT IS NOT. It is not the Status == "Active" filter rosterbot-tmb9
// rejected. That one drops bench arms from the forecast entirely and would have
// missed four of the five pitchers who actually started on 2026-08-19; this
// keeps every one of them in the denominator and merely prices them apart. The
// IL/minors exclusion above is a different thing: a bench arm can start
// tomorrow, an IL arm cannot start at all, and only the second is a day on
// which no decision was available to anyone.
//
// THE RATE IS ACTIVE-SLOT CONVERSION, not rotation membership, and the
// distinction is forced by the data rather than chosen. The per-period pitcher
// snapshot is fetched with StatsType=2 (fantasy-team stats), so its GS only
// ever advances for a start our roster captured in an active slot — which is
// also, exactly, what spends weekly GS budget (GetTeamGS counts the same
// deltas). The alternative denominator, real-world appearances from the
// StatsType=1 roster-stats GP delta, is unusable here: it counts relief
// outings, and in rosterbot-goht's own measurement over the same window an
// SP-eligible reliever read 0.333 against an active-slot rate of 0.008. (That
// pair is quoted from that measurement rather than re-derived — the appearance
// denominator is no longer computed anywhere, having never been a candidate.)
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
// barely moves. And a genuinely good arm acquired days ago is not priced at all
// until he clears gsStartRateMinOpportunities — he sits at the flat rate, which
// is the same answer the pre-weighting code gave. Both errors under-forecast
// demand, which suppresses less; the flat divisor's error ran the other way and
// by three times as much.
//
// ROUND TRIPS. One roster-stats read and one pitcher-GS read per day of the
// window plus a baseline day, so 2×(gsStartRateWindow+1) = 58 snapshot reads,
// alongside gsStartRateWindow = 28 schedule reads — once per ComputeGSBudget,
// i.e. once per Run. Every one of them is cached: the snapshots at
// PastPeriodTTL (a settled period is immutable) and the schedules at
// internal/schedule's 30-day past-date TTL, which is what fixes the window at
// 28. Warm, an hourly run pays one new day. Cold — a fresh container with an
// empty cache — it is 86 upstream reads, of which the 58 Fantrax ones are
// throttled 200 ms apart, so worst case is roughly 12 s added to a single run.
// `shadow` calls Run once per RoS system, and Options.StartRates is what stops
// that repeating this walk four times over identical inputs.
func computeStartRates(
	ctx context.Context,
	ft gsFantraxClient,
	sched gsScheduleClient,
	teamID string,
	seasonStart, today time.Time,
	ttl time.Duration,
) (startRateResult, error) {
	var zero startRateResult
	end := today.AddDate(0, 0, -1)
	start := today.AddDate(0, 0, -gsStartRateWindow)
	if start.Before(seasonStart) {
		start = seasonStart
	}
	if end.Before(start) {
		// Opening day and the day after it: no settled history exists yet, and
		// that is an ordinary state rather than a failure. Every SP falls back
		// to the flat rate.
		return zero, nil
	}

	days, err := ft.GetTeamPitcherDaysWithStatus(teamID, start, end, seasonStart, cacheDir, ttl)
	if err != nil {
		return zero, fmt.Errorf("pitcher day walk %s..%s: %w",
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
			return zero, fmt.Errorf("schedule unavailable for %s: %w", d.Format("2006-01-02"), err)
		}
		playingOn[d.Format("2006-01-02")] = playing
	}

	return tallyStartRates(days, playingOn, shippedStartRateParams()), nil
}

// startRateCoverage summarises the estimator's reach over the SPs actually on
// the roster today, for the unconditional coverage line.
//
// The three fall-back reasons are kept apart because the line's whole job is to
// distinguish "priced on real history" from "priced on one day", and a bare
// priced/total ratio cannot: before the minimum-opportunity guard existed, a
// pitcher seen once counted as priced. Median opportunities is reported for the
// same reason — it is the one number that says how much history the priced half
// actually rests on.
type startRateCoverage struct {
	Priced     int
	BelowMin   int
	NoHistory  int
	MedianOpps int
}

func summariseStartRates(res startRateResult, spNames map[string]fantrax.Player) startRateCoverage {
	var c startRateCoverage
	var opps []int
	for key := range spNames {
		if _, ok := res.Rate[key]; ok {
			c.Priced++
			opps = append(opps, res.Opportunities[key])
			continue
		}
		if _, ok := res.Opportunities[key]; ok {
			c.BelowMin++
			continue
		}
		c.NoHistory++
	}
	if len(opps) > 0 {
		sort.Ints(opps)
		c.MedianOpps = opps[len(opps)/2]
	}
	return c
}

// StartRateCache shares one trailing-history walk across several Run calls over
// identical inputs.
//
// It exists for `shadow`, which calls Run once per rest-of-season projection
// system — four passes over the same team, the same date and the same settled
// history. The walk costs 58 snapshot reads plus 28 schedule reads and depends
// on nothing that varies between those passes, so repeating it four times buys
// nothing. Warm that is four times the cache traffic; cold it is four times the
// upstream, including four times the 200 ms Fantrax throttle.
//
// A caller-owned value rather than package-level state, so two concurrent Runs
// in one process cannot silently share a table measured for a different team.
// The zero value is not usable — use NewStartRateCache — and a nil
// *StartRateCache is a valid "do not share", which is what the ordinary
// single-Run path passes.
type StartRateCache struct {
	mu     sync.Mutex
	filled bool
	res    startRateResult
	err    error
}

// NewStartRateCache returns an empty cache to hand to several Run calls.
func NewStartRateCache() *StartRateCache { return &StartRateCache{} }

// get returns the shared table, measuring it once on first use.
//
// The measurement's ERROR is memoized alongside its result, deliberately. The
// failure this guards is a walk over settled snapshots that just failed for
// every pass at once; retrying it three more times inside one `shadow` run
// would pay the whole cost again to reach the same answer, and would report the
// warning four times. A nil receiver measures every time, which is the
// behaviour of the ordinary single-Run path.
func (c *StartRateCache) get(measure func() (startRateResult, error)) (startRateResult, error) {
	if c == nil {
		return measure()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.filled {
		c.res, c.err = measure()
		c.filled = true
	}
	return c.res, c.err
}

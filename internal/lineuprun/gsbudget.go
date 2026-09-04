package lineuprun

import (
	"fmt"
	"sort"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/playername"
)

// gsScheduleClient is the MLB-schedule surface the GS-budget phase needs.
// *schedule.Client satisfies it; a map-backed fake satisfies it in tests, which
// is what makes this logic exercisable without network (rosterbot-6rv).
type gsScheduleClient interface {
	TeamsPlayingOn(date time.Time) (map[string]bool, error)
	ProbableStarters(date time.Time) (map[string]string, error)
}

// gsFantraxClient is the Fantrax surface the GS-budget fetch cascade needs.
// Three methods, no *fantrax.Client — which is what lets every disable path
// below be tested without credentials (rosterbot-32a).
type gsFantraxClient interface {
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
	GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
	GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
	GetTeamPitcherDays(teamID string, start, end, seasonStart time.Time, cacheDir string, cacheTTL time.Duration) ([]fantrax.PitcherDay, error)
}

// RotationSize is the standard starting rotation depth used to estimate how
// many starts a team will get on a day with no published probables.
const RotationSize = 5.0

// GSInputs is everything ComputeGSBudget needs that it does not fetch itself.
// Periods/PeriodsErr come from the LoadInputs phase rather than being refetched
// here — same authoritative source either way, one request per run instead of
// two.
type GSInputs struct {
	TeamID             string
	Today, SeasonStart time.Time

	Periods    []fantrax.ScoringPeriod
	PeriodsErr error

	PitcherRoster   []fantrax.Player
	NumPitcherSlots int

	// NoCache mirrors the persistent --no-cache flag. It reaches only the
	// start-rate history read, whose per-period snapshots are the one thing
	// this phase fetches that is cacheable at all.
	NoCache bool

	// ProjPts values a pitcher for the forecast's value ranking. Injected so
	// the phase never has to build a projection source.
	ProjPts func(fantrax.Player) float64
}

// GSAlert is a Pushover the phase decided is warranted but did not send.
// Returning the notification as a decision rather than performing it is what
// keeps the phase testable — a test asserts the alert was requested without
// standing up an HTTP server or setting credentials (rosterbot-32a criterion 3).
type GSAlert struct {
	Title   string
	Message string
}

// GSDecision is the outcome of the GS-budget phase.
//
// A nil Budget means the gate is disabled for this run. That is deliberately
// not an error: every failure mode here disables the gate rather than falling
// back to a stale limit, because a wrong budget silently mis-shapes pitcher
// usage all week, which is worse than no budget at all.
type GSDecision struct {
	Budget *optimizer.GSBudget
	Logs   []string
	Alert  *GSAlert

	// Period and Season identify the matchup week the budget describes. They
	// are returned rather than re-resolved by the caller because this phase
	// already did the lookup, and a second FindCurrentPeriod over the same list
	// is one more place for the two answers to disagree.
	//
	// Both are zero when the cascade short-circuited before the period lookup,
	// which is why the floor alert keys off Budget != nil rather than off them.
	Period fantrax.WeeklyPeriod
	Season int
}

func (d *GSDecision) logf(format string, args ...any) {
	d.Logs = append(d.Logs, fmt.Sprintf(format, args...))
}

// ComputeGSBudget resolves this run's weekly game-start budget, or reports why
// it cannot. It owns the whole fetch cascade — matchup-week bounds, the per-day
// past-GS walk, the weekly period lookup, and the live GS max — that used to
// sit inline in Run as a ~45-line else-if chain (rosterbot-32a).
//
// The branch ordering is load-bearing and preserved exactly: each step's result
// is the input to the next, so the first failure short-circuits the rest.
//
// There is no static fallback for the GS max, by design. Fantrax rescales the
// limit whenever a period spans more than one calendar week (season opener,
// All-Star break), so a hardcoded number is wrong precisely in the weeks that
// matter most. A live-fetch failure therefore requests a Pushover — degrading
// pitcher usage silently is the failure mode worth being noisy about — and
// disables the gate. A configured max of nil is different: Fantrax simply is
// not tracking GS for that period, so there is nothing to gate and no alert.
func ComputeGSBudget(ft gsFantraxClient, sched gsScheduleClient, in GSInputs) GSDecision {
	var d GSDecision

	// A zero season start means the season-range lookup failed upstream; there
	// is no week to bound the budget to. Silent, as it was inline — the failed
	// lookup already logged its own warning.
	if in.SeasonStart.IsZero() {
		return d
	}

	weekStart, weekEnd, err := ft.GetMatchupWeekBounds(in.Today, in.SeasonStart)
	if err != nil {
		d.logf("WARNING: could not determine matchup week (%v) — GS limit disabled", err)
		return d
	}
	if weekStart.IsZero() {
		d.logf("WARNING: no matchup week found for today — GS limit disabled")
		return d
	}

	// Used GS comes from the gs_check active-slot delta walk, for every elapsed
	// day of the week INCLUDING today. The walk fetches per-day roster snapshots
	// and counts only active-slot YTD GS deltas — the same source of truth
	// gs-check uses for league-wide violation detection.
	//
	// The probables list is unreliable as a GS proxy: it counts current-roster
	// SPs who were probable while sitting on bench (overcount) and misses SPs
	// dropped after starting in an active slot (undercount).
	//
	// Today used to be counted separately, from the live roster — an SP who was
	// active, on a locked team, and today's probable. That answers "is he active
	// NOW" when the question is "did he start TODAY", and the two diverge every
	// evening: once a pitcher's game is over a later hourly run rotates him back
	// to Reserve for tomorrow, and his already-spent start drops out of the
	// count. Production read 5/12 at 00:00Z on 2026-08-20 and 3/12 an hour
	// later, against a true 5 (rosterbot-cg8l). A start cannot be un-made, so
	// the number has to come from that day's frozen snapshot, not from a roster
	// that keeps moving.
	usedGS, _, gsErr := ft.GetTeamGS(in.TeamID, "",
		fantrax.ScoringPeriod{StartDate: weekStart, EndDate: in.Today},
		in.SeasonStart, in.Today, 0, false)
	if gsErr != nil {
		d.logf("WARNING: per-day GS walk failed (%v) — GS limit disabled", gsErr)
		return d
	}

	if in.PeriodsErr != nil {
		d.logf("WARNING: could not fetch scoring periods (%v) — GS limit disabled", in.PeriodsErr)
		return d
	}
	sp := fantrax.FindCurrentPeriod(in.Periods, in.Today)
	if sp == nil {
		d.logf("WARNING: could not resolve scoring period for today — GS limit disabled")
		return d
	}
	d.Period, d.Season = sp.Number, in.SeasonStart.Year()

	liveMin, liveMax, gerr := ft.GetGSLimits(in.TeamID, sp.Number)
	if gerr != nil {
		msg := fmt.Sprintf("optimize: live GS limit fetch failed for period %d (%v) — GS limit disabled", sp.Number, gerr)
		d.logf("WARNING: %s", msg)
		d.Alert = &GSAlert{Title: "optimize: GS limit fetch failed", Message: msg}
		return d
	}
	if liveMax == nil {
		d.logf("No GS max configured by Fantrax for period %d — GS limit disabled", sp.Number)
		return d
	}

	gsLimit := *liveMax
	// The floor is captured but, unlike the ceiling, a missing one does NOT
	// disable the gate: a league with no configured minimum simply has no
	// floor to protect, whereas a missing maximum leaves the gate with nothing
	// to gate against. Zero is therefore an ordinary value here, not a failure.
	//
	// Like Limit it comes from the live per-period fetch and never from a
	// constant — Fantrax rescales both bounds for merged multi-week periods
	// (period 16 carried min 15, not 10), which is why the static GS_MIN/GS_MAX
	// env vars were removed (rosterbot-513, rosterbot-r10).
	gsFloor := 0
	if liveMin != nil {
		gsFloor = *liveMin
	}
	if gsFloor > 0 {
		d.logf("GS limit: %d per week (floor %d) (%s to %s)",
			gsLimit, gsFloor, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))
	} else {
		d.logf("GS limit: %d per week (%s to %s)",
			gsLimit, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))
	}

	spNames := rosterSPNames(in.PitcherRoster)

	// Per-pitcher start rates. The read is SOFT, and the asymmetry against the
	// schedule seam below is the point rather than an inconsistency: a lost
	// schedule moves the forecast in an UNKNOWN direction (confirmed starts
	// worth 1.0 each drop out while settled clubs get promoted into the
	// estimate at ~0.2 each), whereas a lost history has exactly one effect —
	// every SP reverts to the flat rate, which is the behaviour that shipped
	// all season. Degrading to the status quo ante is never worse than the
	// status quo ante, so it must not disable a gate that would otherwise run.
	rates, rateErr := computeStartRates(ft, sched, in.TeamID, in.SeasonStart, in.Today,
		cacheTTL(in.NoCache, fantrax.PastPeriodTTL))
	if rateErr != nil {
		d.logf("WARNING: start-rate history unavailable (%v) — every SP priced at the flat %.2f",
			rateErr, 1/RotationSize)
	}
	priced := 0
	for key := range spNames {
		if _, ok := rates[key]; ok {
			priced++
		}
	}
	// Printed unconditionally, including the all-flat case, on the same
	// reasoning as the "gs floor check:" and "il-start check:" lines. A
	// weighting that silently stopped matching any pitcher would forecast
	// exactly what the flat divisor forecasts, and nothing else in the output
	// would say so.
	d.logf("GS start rates: %d of %d rostered SPs priced from their own %d-day active-slot history, %d at the flat %.2f",
		priced, len(spNames), gsStartRateWindow, len(spNames)-priced, 1/RotationSize)

	// The forecast is the last step of the cascade and obeys the same rule as
	// every step before it: no gate beats a gate running on a number nobody can
	// vouch for. Unlike the GS-limit fetch this raises no Pushover — a statsapi
	// blip is transient and self-healing on the next hourly run, where a
	// Fantrax config read that stops working is not.
	forecast, fcErr := buildGSForecast(sched, spNames, in.NumPitcherSlots, in.Today, weekEnd, in.ProjPts, rates)
	if fcErr != nil {
		d.logf("WARNING: %v — GS limit disabled", fcErr)
		return d
	}

	// Today's own starts belong to neither the walk nor the forecast for most
	// of the day; see GSBudget.TodayUnsettled. A failure here degrades to no
	// credit rather than disabling the gate: zero reproduces the pre-fix
	// arithmetic exactly, which reads a shortfall louder than the truth and
	// never quieter, and this correction only ever feeds a report.
	todayUnsettled, tuErr := todayUnsettledStarts(sched, spNames, in.NumPitcherSlots, in.Today)
	if tuErr != nil {
		d.logf("WARNING: today's probables unavailable (%v) — floor shortfall may read high", tuErr)
	}

	d.Budget = &optimizer.GSBudget{
		Limit:          gsLimit,
		Floor:          gsFloor,
		Used:           usedGS,
		Today:          in.Today,
		WeekEnd:        weekEnd,
		Forecast:       forecast,
		TodayUnsettled: todayUnsettled,
	}
	if need := d.Budget.NeedForFloor(); need > 0 {
		d.logf("GS budget: %d/%d used (floor %d, %d more needed), %.1f projected future starts",
			usedGS, gsLimit, gsFloor, need, d.Budget.FutureDemand())
	} else {
		d.logf("GS budget: %d/%d used, %.1f projected future starts",
			usedGS, gsLimit, d.Budget.FutureDemand())
	}
	return d
}

// buildGSForecast projects how many game starts the roster will demand on each
// remaining day of the matchup week — today+1 through weekEnd, since today's
// starts are already counted as used.
//
// The two regimes are PER CLUB, not per day, and that is the whole correctness
// story (rosterbot-1jvj). Each rostered SP lands in exactly one bucket:
//   - named as its club's probable → a confirmed start, contributing its
//     projected points so the gate can rank across the week by value rather
//     than by count;
//   - club plays but has not named anyone yet → rotation-math demand, since a
//     start is coming from that club and we do not yet know whose. Each such
//     pitcher contributes his OWN measured active-slot start rate rather than a
//     flat 1-in-5 (see computeStartRates and rosterbot-goht); with no history
//     he contributes exactly 1/RotationSize, so the pre-weighting arithmetic is
//     the default rather than a special case;
//   - club plays and named someone else → nothing, the day is settled for us;
//   - club does not play → nothing.
//
// Splitting per day instead — gating the estimate on len(probs) > 0 — made the
// fallback unreachable in production. MLB trickles probables out up to a week
// ahead, so the league-wide map is essentially never empty: measured against
// statsapi on 2026-08-19, the four remaining days of the week had 14, 6, 4 and
// 2 announced probables against 18-30 clubs playing. A single announcement for
// ANY club, ours or not, zeroed the entire day, and FutureDemand() read 0.0 all
// week — which in turn meant applyGSGate saw no competition for the remaining
// budget and never suppressed anything.
//
// Which clubs have named a starter comes from the probables map's VALUES. That
// is per-club information carried in the same statsapi payload (a club appears
// there iff one of its games has a non-null probablePitcher); len(probs) is a
// league-wide count and says nothing about any particular club.
//
// projPts is injected rather than taking a projection source directly, so the
// forecast can be tested without building one.
// A schedule-seam failure is FATAL and returns an error rather than a degraded
// forecast. Both lookups used to discard their errors outright, which made an
// upstream outage indistinguishable from a genuinely quiet week — the forecast
// read zero and nothing said why.
//
// Reporting a partial forecast instead was the tempting middle ground, on the
// reasoning that an under-count only makes the gate suppress less. That does
// not survive the probables case: losing the map drops confirmed starts (1.0
// each) while promoting every already-settled club into the estimate (0.2
// each), so the error runs in an unknown direction. ComputeGSBudget's rule
// exists so nobody has to adjudicate that per failure mode — a wrong budget
// silently mis-shapes pitcher usage for the rest of the week, and no budget
// does not. The cost is bounded by cadence rather than by severity: the lineup
// job runs hourly, so a statsapi blip costs one run's gate.
func buildGSForecast(
	sched gsScheduleClient,
	spNames map[string]fantrax.Player,
	numPSlots int,
	today, weekEnd time.Time,
	projPts func(fantrax.Player) float64,
	startRate map[string]float64,
) ([]optimizer.DayForecast, error) {
	var forecast []optimizer.DayForecast
	for d := today.AddDate(0, 0, 1); !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
		ds := d.Format("2006-01-02")

		// Both reads abort the whole forecast on the first failure. There is
		// nothing to learn from the remaining days once the week's total is
		// already disqualified, and no reason to spend more round-trips on a
		// seam that just failed.
		playing, err := sched.TeamsPlayingOn(d)
		if err != nil {
			return nil, fmt.Errorf("schedule unavailable for %s: %w", ds, err)
		}
		probs, err := sched.ProbableStarters(d)
		if err != nil {
			return nil, fmt.Errorf("probable starters unavailable for %s: %w", ds, err)
		}

		// Clubs that have already named a starter for this day.
		announced := make(map[string]bool, len(probs))
		for _, team := range probs {
			announced[team] = true
		}

		df := optimizer.DayForecast{Date: d}
		// The unannounced bucket is split in two, and the split is what keeps
		// the no-history path EXACT. Pitchers with no measured rate are
		// counted and divided once, reproducing the old unannouncedSPs/
		// RotationSize expression bit for bit; only pitchers with a measured
		// rate are summed. Summing 1/RotationSize n times instead would drift
		// in the last bits (3 × 0.2 = 0.6000000000000001), which is a change
		// nobody asked for on every roster that has no history at all.
		var flatSPs float64
		var measured []float64
		for key, p := range spNames {
			if playername.MatchProbable(p.Name, p.MLBTeam, probs) == playername.ConfirmedStarter {
				df.ConfirmedStarters = append(df.ConfirmedStarters, projPts(p))
				continue
			}
			if playing[p.MLBTeam] && !announced[p.MLBTeam] {
				if r, ok := startRate[key]; ok {
					measured = append(measured, r)
					continue
				}
				flatSPs++
			}
		}
		// Sorted before summing for the same reason ConfirmedStarters is
		// sorted below: spNames is a map, so the append order is Go's
		// randomized iteration order, and floating-point addition is not
		// associative. Without this, two runs on identical inputs could
		// produce Estimated values differing in the last bits — which this
		// package owes not to do (see the Idempotency section of CLAUDE.md).
		sort.Float64s(measured)
		unannouncedDemand := flatSPs / RotationSize
		for _, r := range measured {
			unannouncedDemand += r
		}

		// Sorted unconditionally, not only when the cap binds: spNames is a
		// map, so the append order above is Go's randomized iteration order and
		// the slice would otherwise differ between two runs on identical
		// inputs. Nothing downstream reads the order today, but this package
		// owes idempotency and a randomized slice is a standing invitation to
		// break it.
		sort.Slice(df.ConfirmedStarters, func(i, j int) bool {
			return df.ConfirmedStarters[i] > df.ConfirmedStarters[j]
		})
		// Cap at active P slots, keeping the highest-value probables.
		if len(df.ConfirmedStarters) > numPSlots {
			df.ConfirmedStarters = df.ConfirmedStarters[:numPSlots]
		}

		// Cap STARTS at the slot count, not pitchers-whose-team-plays.
		// numPSlots bounds how many starts can accrue in a day; the rotation
		// divisor converts pitchers into expected starts, so the cap has to
		// come after it (rosterbot-abd). Capping first mixes units and
		// understates every day where more rostered SPs have a game than there
		// are pitcher slots — with 10 rostered SPs and 6 slots that is nearly
		// every day.
		//
		// The cap is on the day's COMBINED total: confirmed starts have already
		// claimed slots, so the estimate may only fill what they left. Capping
		// each regime at numPSlots separately would forecast up to 2x the
		// starts the roster can physically field.
		df.Estimated = max(min(unannouncedDemand,
			float64(numPSlots-len(df.ConfirmedStarters))), 0)

		forecast = append(forecast, df)
	}
	return forecast, nil
}

// todayUnsettledStarts counts our rostered SPs named as probable starters TODAY
// whose starts Fantrax has not yet credited to the week's used count.
//
// The lock is what separates the two regimes, and it is the only free signal
// that does: Fantrax locks a player at first pitch, which is the same boundary
// its YTD GS settlement crosses from the other side. An unlocked probable has
// not thrown, so he cannot be in Used; a locked one has, so he is Used's to
// count. The narrow window where a start is locked but not yet settled is
// counted by neither, which reads the shortfall slightly HIGH — the same
// direction as the bug this replaces, and the safe direction for an alert.
//
// Two more guards keep this in step with what Used can actually reflect
// (rosterbot-ogtq lens A/B). GetTeamGS credits only ACTIVE-SLOT GS deltas
// (internal/fantrax/pitcher_starts.go:84-85) — a benched pitcher's real-world
// start never lands in Used no matter what MLB's probables say, so a
// confirmed starter must also be occupying an active slot (p.Status ==
// "Active") to count here. And the count is capped at numPSlots, mirroring
// buildGSForecast's own cap ("Cap at active P slots, keeping the
// highest-value probables"): a team can only start as many pitchers as it has
// active P slots, so nothing this function returns can exceed that whether or
// not the active-status filter above already implies the same bound.
//
// Deliberately not folded into buildGSForecast. Adding today to Forecast would
// be invisible to every current consumer (all four guard on Date.After(Today)),
// but it would make a today row appear in the archived gs_forecast snapshots
// and quietly change what backtest grades.
func todayUnsettledStarts(sched gsScheduleClient, spNames map[string]fantrax.Player, numPSlots int, today time.Time) (int, error) {
	probs, err := sched.ProbableStarters(today)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range spNames {
		if p.Locked || p.Status != "Active" {
			continue
		}
		if playername.MatchProbable(p.Name, p.MLBTeam, probs) == playername.ConfirmedStarter {
			n++
		}
	}
	return min(n, numPSlots), nil
}

package lineuprun

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
	"github.com/nixon-commits/rosterbot/internal/projections"
)

// gsScheduleClient is the MLB-schedule surface the GS-budget phase needs.
// *schedule.Client satisfies it; a map-backed fake satisfies it in tests, which
// is what makes this logic exercisable without network (rosterbot-6rv).
type gsScheduleClient interface {
	TeamsPlayingOn(date time.Time) (map[string]bool, error)
	ProbableStarters(date time.Time) (map[string]string, error)
	LockedTeams(date time.Time) (map[string]bool, error)
}

// gsFantraxClient is the Fantrax surface the GS-budget fetch cascade needs.
// Three methods, no *fantrax.Client — which is what lets every disable path
// below be tested without credentials (rosterbot-32a).
type gsFantraxClient interface {
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
	GetTeamGS(teamID, teamName string, sp fantrax.ScoringPeriod, seasonStart, today time.Time, gsMax int, verbose bool) (int, []fantrax.PitcherStart, error)
	GetGSLimits(teamID string, period fantrax.WeeklyPeriod) (min, max *int, err error)
}

// rotationSize is the standard starting rotation depth used to estimate how
// many starts a team will get on a day with no published probables.
const rotationSize = 5.0

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

	// Past GS uses the gs_check active-slot delta walk. The probables list is
	// unreliable as a GS proxy: it counts current-roster SPs who were probable
	// while sitting on bench (overcount) and misses SPs dropped after starting
	// in an active slot (undercount). The walk fetches per-day roster snapshots
	// and counts only active-slot YTD GS deltas — the same source of truth
	// gs-check uses for league-wide violation detection.
	pastGS, _, gsErr := ft.GetTeamGS(in.TeamID, "",
		fantrax.ScoringPeriod{StartDate: weekStart, EndDate: in.Today.AddDate(0, 0, -1)},
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

	_, liveMax, gerr := ft.GetGSLimits(in.TeamID, sp.Number)
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
	d.logf("GS limit: %d per week (%s to %s)",
		gsLimit, weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

	spNames := rosterSPNames(in.PitcherRoster)
	usedGS := pastGS
	forecast := buildGSForecast(sched, spNames, in.NumPitcherSlots, in.Today, weekEnd, in.ProjPts)

	// Today's already-locked starts count as used, not forecast demand.
	// Both lookups must succeed — a partial view would undercount.
	lockedTeams, lockErr := sched.LockedTeams(in.Today)
	todayProbs, probsErr := sched.ProbableStarters(in.Today)
	if lockErr == nil && probsErr == nil {
		usedGS += countTodayStarts(in.PitcherRoster, lockedTeams, todayProbs)
	}

	d.Budget = &optimizer.GSBudget{
		Limit:    gsLimit,
		Used:     usedGS,
		Today:    in.Today,
		WeekEnd:  weekEnd,
		Forecast: forecast,
	}
	d.logf("GS budget: %d/%d used, %.1f projected future starts",
		usedGS, gsLimit, d.Budget.FutureDemand())
	return d
}

// countTodayStarts returns how many of today's game starts the roster has
// already consumed. Pure — no I/O.
//
// A rostered SP counts only when it is active (not benched, injured, or in the
// minors), its MLB team is locked, AND it is that team's published probable
// starter today. Each condition rules out a specific miscount:
//   - team-locked alone would count an active SP-eligible reliever, or an SP
//     who simply isn't pitching, purely because the team's game started;
//   - probable alone would count a start before the game locks, double-counting
//     it against the forecast;
//   - the team equality check rejects a stale probables entry for a player who
//     has since changed clubs.
//
// Probables for completed games remain in the API for the rest of the day, so
// this captures both in-progress and finished starts.
func countTodayStarts(pitcherRoster []fantrax.Player, lockedTeams map[string]bool, todayProbs map[string]string) int {
	used := 0
	for _, p := range pitcherRoster {
		if p.Status != "Active" || p.InMinors || p.IsInjured {
			continue
		}
		if !lockedTeams[p.MLBTeam] {
			continue
		}
		if !strings.Contains(p.PosShortNames, "SP") {
			continue
		}
		if team, ok := todayProbs[projections.NormalizeName(p.Name)]; ok && team == p.MLBTeam {
			used++
		}
	}
	return used
}

// buildGSForecast projects how many game starts the roster will demand on each
// remaining day of the matchup week — today+1 through weekEnd, since today's
// starts are already counted as used.
//
// Two regimes per day, mirroring what the upstream actually knows:
//   - probables published: each rostered SP confirmed to start contributes its
//     projected points, so the gate can rank across the week by value rather
//     than by count. Capped at numPSlots (only active-slot SPs accrue a start),
//     keeping the highest-value ones.
//   - no probables yet: estimate by rotation math — rostered SPs whose team
//     plays, capped at numPSlots, divided by a 5-man rotation.
//
// projPts is injected rather than taking a projection source directly, so the
// forecast can be tested without building one.
func buildGSForecast(
	sched gsScheduleClient,
	spNames map[string]fantrax.Player,
	numPSlots int,
	today, weekEnd time.Time,
	projPts func(fantrax.Player) float64,
) []optimizer.DayForecast {
	var forecast []optimizer.DayForecast
	for d := today.AddDate(0, 0, 1); !d.After(weekEnd); d = d.AddDate(0, 0, 1) {
		playing, _ := sched.TeamsPlayingOn(d)
		probs, _ := sched.ProbableStarters(d)

		df := optimizer.DayForecast{Date: d}
		if len(probs) > 0 {
			for normName, team := range probs {
				p, ours := spNames[normName]
				if !ours || p.MLBTeam != team {
					continue
				}
				df.ConfirmedStarters = append(df.ConfirmedStarters, projPts(p))
			}
			// Cap at active P slots, keeping the highest-value probables.
			if len(df.ConfirmedStarters) > numPSlots {
				sort.Slice(df.ConfirmedStarters, func(i, j int) bool {
					return df.ConfirmedStarters[i] > df.ConfirmedStarters[j]
				})
				df.ConfirmedStarters = df.ConfirmedStarters[:numPSlots]
			}
		} else {
			var spPlaying float64
			for _, p := range spNames {
				if playing[p.MLBTeam] {
					spPlaying++
				}
			}
			if spPlaying > float64(numPSlots) {
				spPlaying = float64(numPSlots)
			}
			df.Estimated = spPlaying / rotationSize
		}
		forecast = append(forecast, df)
	}
	return forecast
}

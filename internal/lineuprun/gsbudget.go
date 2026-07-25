package lineuprun

import (
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

// rotationSize is the standard starting rotation depth used to estimate how
// many starts a team will get on a day with no published probables.
const rotationSize = 5.0

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

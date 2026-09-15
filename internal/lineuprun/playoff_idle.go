package lineuprun

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
)

// playoffIdleClient is the lookup surface playoffIdle needs.
type playoffIdleClient interface {
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
	GetPlayoffBracket() (*auth_client.PlayoffBracket, error)
}

// playoffIdle reports whether day falls inside a playoff round in which the
// team has no scoring matchup — a bye, an elimination, or a team never
// seeded — and, when it does, the one line the run prints. Such a day has
// nothing to optimize: the lineup scores for nobody.
//
// The DECISION is fantrax.ClassifyScoringDay's, shared with the lineup-gap
// grading in cmd/grade.go and the backtest report so the two cannot drift
// (rosterbot-zg1r): the merged matchup list carries a row for every team
// paired in a round and a pending row for every team advanced into a round
// Fantrax has not drawn yet (fantrax.playoffMatchups), so "no week" there is
// the whole evidence. The WORDING comes from the bracket
// (fantrax.PlayoffStatusFor); when the bracket cannot be read the stop still
// happens with the generic line, because the wording is decoration on a
// decision already made.
//
// The stop is deliberately scoped to Playoff periods, which is the only
// verdict that means "idle" here. A regular-season day is scoring by
// construction (a missing week there is a Fantrax fault ResolveDates keeps
// loud), and a date outside every period is the season-end stop's to name,
// not this one's. An error means the lookup could not answer, which the
// caller treats as "not idle" — an unknown bracket status is not a reason to
// skip a live day.
func playoffIdle(ft playoffIdleClient, teamID string, day, seasonStart time.Time) (idle bool, line string, err error) {
	periods, _, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return false, "", fmt.Errorf("scoring periods: %w", err)
	}
	sd, err := fantrax.ClassifyScoringDay(periods, ft, seasonStart, day)
	if err != nil {
		return false, "", err
	}
	if sd.Scoring || sd.Period == nil || !sd.Period.Playoff {
		return false, "", nil
	}
	return true, playoffIdleLine(ft, teamID, sd.Period), nil
}

// playoffIdleLine names the reason a team has no scoring matchup in the
// round, from the bracket when it can be read.
func playoffIdleLine(ft playoffIdleClient, teamID string, p *fantrax.ScoringPeriod) string {
	generic := fmt.Sprintf("No scoring matchup for this team in %s (%s to %s): bye or eliminated. Nothing to optimize.",
		p.Caption, p.StartDate.Format("2006-01-02"), p.EndDate.Format("2006-01-02"))
	b, err := ft.GetPlayoffBracket()
	if err != nil {
		return generic
	}
	st := fantrax.PlayoffStatusFor(b, teamID, int(p.Number))
	switch st.Standing {
	case fantrax.PlayoffEliminated:
		return fmt.Sprintf("Eliminated in Playoffs - Round %d (lost to %s %.0f-%.0f). Nothing to optimize.",
			st.Round, st.Opponent, st.ScoreFor, st.ScoreAgainst)
	case fantrax.PlayoffBye:
		return fmt.Sprintf("On a bye in %s (%s to %s). Nothing to optimize.",
			p.Caption, p.StartDate.Format("2006-01-02"), p.EndDate.Format("2006-01-02"))
	case fantrax.PlayoffNotSeeded:
		if b != nil && len(b.Rounds) > 0 {
			return fmt.Sprintf("Not in the playoff bracket (%s is under way). Nothing to optimize.", p.Caption)
		}
	}
	return generic
}

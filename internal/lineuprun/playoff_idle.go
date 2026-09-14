package lineuprun

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// playoffIdleClient is the two-lookup surface playoffIdle needs.
type playoffIdleClient interface {
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
	GetMatchupWeekBounds(date, seasonStart time.Time) (weekStart, weekEnd time.Time, err error)
}

// playoffIdle reports whether day falls inside a playoff round in which the
// team has no scoring matchup — a bye, or a team that is out of the bracket.
// Such a day has nothing to optimize: the lineup scores for nobody.
//
// The check is deliberately scoped to Playoff periods. In the regular season
// every team has a matchup every week, so a missing week there is a Fantrax
// fault that must stay loud (ResolveDates keeps returning it as an error);
// only inside the bracket is "no matchup" an ordinary answer. It returns the
// round so the caller can name it, and an error only when neither lookup can
// answer, which the caller treats as "not idle" — an unknown bracket status
// is not a reason to skip a live day.
func playoffIdle(ft playoffIdleClient, day, seasonStart time.Time) (*fantrax.ScoringPeriod, bool, error) {
	periods, _, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return nil, false, fmt.Errorf("scoring periods: %w", err)
	}
	p := fantrax.FindCurrentPeriod(periods, day)
	if p == nil || !p.Playoff {
		return p, false, nil
	}
	ws, _, err := ft.GetMatchupWeekBounds(day, seasonStart)
	if err != nil {
		return p, false, fmt.Errorf("matchup week: %w", err)
	}
	return p, ws.IsZero(), nil
}

// playoffIdleLine is the one line a run prints when it stops on playoffIdle.
func playoffIdleLine(p *fantrax.ScoringPeriod) string {
	return fmt.Sprintf("No scoring matchup for this team in %s (%s to %s): bye or eliminated. Nothing to optimize.",
		p.Caption, p.StartDate.Format("2006-01-02"), p.EndDate.Format("2006-01-02"))
}

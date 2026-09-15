package fantrax

import (
	"fmt"
	"time"
)

// ScoringDay is whether a team's lineup scored for anything on a calendar
// date — the one question the lineup stop and the lineup-gap grading both
// ask, answered in one place (rosterbot-zg1r). A day the lineup scored for
// nobody has nothing to optimize and nothing to grade: the gap between the
// lineup fielded and the hindsight-optimal one measures a decision that
// counted for nothing, and mixing such days into the gap series shifts a
// baseline calibrated on scoring weeks only.
type ScoringDay struct {
	// Scoring is true when the team had a scoring matchup on the date.
	Scoring bool
	// Period is the weekly period containing the date, nil when none does
	// (before the opener, or after the bracket's final round).
	Period *ScoringPeriod
	// Reason says why the day is not scoring; empty for a scoring day.
	Reason string
}

// ClassifyScoringDay reads the verdict for one date off the weekly period
// list and, only where it matters, the team's matchup weeks.
//
// A regular-season day is scoring by construction and the matchup list is
// not consulted: every team has a matchup every week, so a missing row
// there is a Fantrax fault, and the callers that resolve windows keep it
// loud (ResolveDates, ResolveRange) rather than let this quietly drop a day.
// Inside the bracket "no matchup week" is an ordinary answer — a bye, an
// elimination, a team never seeded — and that is the only place the lookup
// decides anything. A date no weekly period contains is outside the season.
//
// A lookup error is returned, never turned into a verdict: an unknown
// bracket status is not evidence either way, and each caller decides what
// an unknown costs it.
func ClassifyScoringDay(periods []ScoringPeriod, wb WeekBounder, seasonStart, date time.Time) (ScoringDay, error) {
	p := FindCurrentPeriod(periods, date)
	if p == nil {
		return ScoringDay{Reason: fmt.Sprintf("%s is outside the league's scoring periods", date.Format("2006-01-02"))}, nil
	}
	if !p.Playoff {
		return ScoringDay{Scoring: true, Period: p}, nil
	}
	ws, _, err := wb.GetMatchupWeekBounds(date, seasonStart)
	if err != nil {
		return ScoringDay{}, fmt.Errorf("matchup week for %s: %w", date.Format("2006-01-02"), err)
	}
	if ws.IsZero() {
		return ScoringDay{Period: p, Reason: fmt.Sprintf("no scoring matchup for this team in %s (%s to %s): bye or eliminated",
			p.Caption, p.StartDate.Format("2006-01-02"), p.EndDate.Format("2006-01-02"))}, nil
	}
	return ScoringDay{Scoring: true, Period: p}, nil
}

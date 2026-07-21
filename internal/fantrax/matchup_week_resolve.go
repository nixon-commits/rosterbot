package fantrax

import (
	"fmt"
	"time"
)

// WeekBounder resolves a calendar date to the matchup week containing it.
// Satisfied by *Client; declared as an interface so the resolution policy below
// is testable without a live Fantrax session.
type WeekBounder interface {
	GetMatchupWeekBounds(date, seasonStart time.Time) (time.Time, time.Time, error)
}

// LastCompletedMatchupWeek returns the bounds of the most recent matchup week
// that is fully over as of today. It looks up yesterday's week and, if today
// still falls inside that week (i.e. the week is mid-flight), steps back one
// day before its start to land in the previous week.
//
// A week whose final day *is* today counts as still running here — the daily
// FPts for that day are not settled. Callers that can positively confirm the
// day's games are done (recap checks MLB game finality) should make that check
// themselves before falling back to this helper.
//
// Both cmd/backtest.go and cmd/recap.go carried byte-identical copies of this
// walk before it was consolidated here (rosterbot-brt); the same kind of
// independently-drifting date arithmetic produced the rosterbot-uv6/z3b period
// bugs, so it lives in one place beside the matchup-week bounds it depends on.
func LastCompletedMatchupWeek(wb WeekBounder, seasonStart, today time.Time) (time.Time, time.Time, error) {
	yesterday := today.AddDate(0, 0, -1)

	ws, we, err := wb.GetMatchupWeekBounds(yesterday, seasonStart)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if ws.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("no matchup week found for %s", yesterday.Format("2006-01-02"))
	}

	// Today inside this week → it is not finished; back up to the prior week.
	if !today.After(we) {
		prior := ws.AddDate(0, 0, -1)
		ws, we, err = wb.GetMatchupWeekBounds(prior, seasonStart)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if ws.IsZero() {
			return time.Time{}, time.Time{}, fmt.Errorf("no prior matchup week found before %s", prior.Format("2006-01-02"))
		}
	}
	return ws, we, nil
}

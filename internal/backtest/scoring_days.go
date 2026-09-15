package backtest

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// ExcludedDay is a day withheld from lineup grading, and why.
type ExcludedDay struct {
	Date   time.Time `json:"date"`
	Reason string    `json:"reason"`
}

// SplitScoringDays partitions a window's days into the ones the team's
// lineup scored for and the ones it did not, by the verdict classify hands
// back for each date (fantrax.ClassifyScoringDay in production).
//
// The lineup gap on a non-scoring day measures a decision that counted for
// nothing — a bye week, the rounds after an elimination, the days past the
// bracket's final — and the gap series' healthy band is calibrated on
// scoring weeks only (rosterbot-zg1r). Projection grading is NOT filtered
// through this: a projection is judged against real MLB actuals, which keep
// arriving whether or not the fantasy lineup counts.
//
// One unanswerable day fails the whole split, and a failed split hands back
// nothing: half a verdict would grade some days on the rule and the rest on
// a guess, and every caller has a safer answer to "unknown" than that (the
// gap writer withholds the run's rows, which the next run's floor re-grades;
// the report says it could not classify and grades unfiltered, loudly).
func SplitScoringDays(days []fantrax.DayRoster, classify func(time.Time) (fantrax.ScoringDay, error)) (scoring []fantrax.DayRoster, excluded []ExcludedDay, err error) {
	for _, day := range days {
		sd, cerr := classify(day.Date)
		if cerr != nil {
			return nil, nil, cerr
		}
		if sd.Scoring {
			scoring = append(scoring, day)
			continue
		}
		excluded = append(excluded, ExcludedDay{Date: day.Date, Reason: sd.Reason})
	}
	return scoring, excluded, nil
}

// ScoringClassifier binds fantrax.ClassifyScoringDay to a period list and a
// week lookup, so the two commands that split a window build the closure
// the same way.
func ScoringClassifier(periods []fantrax.ScoringPeriod, wb fantrax.WeekBounder, seasonStart time.Time) func(time.Time) (fantrax.ScoringDay, error) {
	return func(date time.Time) (fantrax.ScoringDay, error) {
		return fantrax.ClassifyScoringDay(periods, wb, seasonStart, date)
	}
}

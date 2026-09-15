package backtest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// classifyOutFrom is a classifier that calls every day from `out` onward
// non-scoring with a fixed reason — the shape of an elimination.
func classifyOutFrom(out time.Time, reason string) func(time.Time) (fantrax.ScoringDay, error) {
	return func(day time.Time) (fantrax.ScoringDay, error) {
		if day.Before(out) {
			return fantrax.ScoringDay{Scoring: true}, nil
		}
		return fantrax.ScoringDay{Reason: reason}, nil
	}
}

func TestSplitScoringDays_PartitionsByVerdict(t *testing.T) {
	days := []fantrax.DayRoster{{Date: d("2026-09-12")}, {Date: d("2026-09-13")}, {Date: d("2026-09-14")}}
	reason := "no scoring matchup for this team in Playoffs - Round 2 (2026-09-14 to 2026-09-20): bye or eliminated"

	scoring, excluded, err := SplitScoringDays(days, classifyOutFrom(d("2026-09-14"), reason))
	if err != nil {
		t.Fatalf("SplitScoringDays: %v", err)
	}
	if len(scoring) != 2 || !scoring[0].Date.Equal(d("2026-09-12")) || !scoring[1].Date.Equal(d("2026-09-13")) {
		t.Errorf("scoring days = %v, want 09-12 and 09-13 in order", dates(scoring))
	}
	if len(excluded) != 1 || !excluded[0].Date.Equal(d("2026-09-14")) || excluded[0].Reason != reason {
		t.Errorf("excluded = %+v, want 09-14 with the classifier's reason", excluded)
	}
}

// An all-scoring window comes back whole and with nothing excluded — the
// regular-season case, which must be unchanged to the digit.
func TestSplitScoringDays_AllScoringIsUnchanged(t *testing.T) {
	days := []fantrax.DayRoster{{Date: d("2026-08-24")}, {Date: d("2026-08-25")}}
	scoring, excluded, err := SplitScoringDays(days, classifyOutFrom(d("2026-09-14"), "out"))
	if err != nil {
		t.Fatalf("SplitScoringDays: %v", err)
	}
	if len(scoring) != 2 || len(excluded) != 0 {
		t.Errorf("scoring=%d excluded=%d, want 2/0", len(scoring), len(excluded))
	}
}

// A classifier that cannot answer for one day fails the whole split. Half a
// verdict would grade some days on a rule and the rest on a guess, and the
// callers each have a safer response to "unknown" than that.
func TestSplitScoringDays_LookupErrorFailsTheSplit(t *testing.T) {
	days := []fantrax.DayRoster{{Date: d("2026-09-13")}, {Date: d("2026-09-14")}}
	classify := func(day time.Time) (fantrax.ScoringDay, error) {
		if day.Equal(d("2026-09-14")) {
			return fantrax.ScoringDay{}, errors.New("fantrax 524")
		}
		return fantrax.ScoringDay{Scoring: true}, nil
	}
	scoring, excluded, err := SplitScoringDays(days, classify)
	if err == nil {
		t.Fatal("want the lookup error, got a split")
	}
	if scoring != nil || excluded != nil {
		t.Errorf("a failed split must hand back nothing, got scoring=%v excluded=%v", dates(scoring), excluded)
	}
}

func dates(days []fantrax.DayRoster) []string {
	out := make([]string, 0, len(days))
	for _, day := range days {
		out = append(out, day.Date.Format("2006-01-02"))
	}
	return out
}

// The exclusion is visible, not silent: the report names each withheld day
// and its reason, the same way it already names stale and missing
// projection days.
func TestFormatReport_NamesLineupDaysExcluded(t *testing.T) {
	r := Report{
		Start:  d("2026-09-07"),
		End:    d("2026-09-14"),
		Lineup: []LineupDayResult{{Date: d("2026-09-13"), ActualPts: 60, OptimalPts: 79, Gap: -19}},
		LineupExcluded: []ExcludedDay{{Date: d("2026-09-14"),
			Reason: "no scoring matchup for this team in Playoffs - Round 2 (2026-09-14 to 2026-09-20): bye or eliminated"}},
	}
	out := FormatReport(r)
	if !strings.Contains(out, "Excluded from lineup grading: 1 day(s)") {
		t.Errorf("report lacks the exclusion count:\n%s", out)
	}
	if !strings.Contains(out, "2026-09-14: no scoring matchup for this team in Playoffs - Round 2") {
		t.Errorf("report lacks the excluded day and its reason:\n%s", out)
	}
}

// And on a healthy window the block is absent — asserting presence on the
// failing path alone is half a test, since nothing then stops the line
// becoming unconditional.
func TestFormatReport_NoLineupExclusionBlockOnAHealthyWindow(t *testing.T) {
	r := Report{Start: d("2026-08-24"), End: d("2026-08-30"),
		Lineup: []LineupDayResult{{Date: d("2026-08-24"), ActualPts: 60, OptimalPts: 79, Gap: -19}}}
	if out := FormatReport(r); strings.Contains(out, "Excluded from lineup grading") {
		t.Errorf("healthy window must not print an exclusion block:\n%s", out)
	}
}

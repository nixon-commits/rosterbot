package fantrax

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The three verdicts ClassifyScoringDay can reach, and which lookups each one
// trusts. A regular-season day is scoring without asking the matchup list at
// all — every team has a matchup every week, and a missing row there is a
// Fantrax fault the callers keep loud rather than a reason to drop a day. Only
// inside the bracket is "no matchup week" an ordinary answer.
func TestClassifyScoringDay(t *testing.T) {
	seasonStart := day("2026-03-26")
	regular := ScoringPeriod{Number: 22, Caption: "Scoring Period 22",
		StartDate: day("2026-08-31"), EndDate: day("2026-09-06")}
	round1 := ScoringPeriod{Number: 23, Caption: "Playoffs - Round 1", Playoff: true,
		StartDate: day("2026-09-07"), EndDate: day("2026-09-13")}
	round2 := ScoringPeriod{Number: 24, Caption: "Playoffs - Round 2", Playoff: true,
		StartDate: day("2026-09-14"), EndDate: day("2026-09-20")}
	periods := []ScoringPeriod{regular, round1, round2}
	paired := []dateRange{{day("2026-09-07"), day("2026-09-13")}} // Round 1 only: out after it

	cases := []struct {
		name        string
		date        time.Time
		wantScoring bool
		wantPeriod  *ScoringPeriod
		wantReason  string // substring; empty for a scoring day
		wantLookups int    // matchup-week lookups the verdict was allowed to make
	}{
		{"regular season is scoring without a matchup lookup", day("2026-09-02"), true, &regular, "", 0},
		{"playoff round with a matchup week is scoring", day("2026-09-10"), true, &round1, "", 1},
		{"playoff round without a matchup week is not", day("2026-09-15"), false, &round2, "Playoffs - Round 2", 1},
		{"after the final round is outside the season", day("2026-09-28"), false, nil, "outside", 0},
		{"before the opener is outside the season", day("2026-03-01"), false, nil, "outside", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wb := &fakeBounder{weeks: paired}
			got, err := ClassifyScoringDay(periods, wb, seasonStart, tc.date)
			if err != nil {
				t.Fatalf("ClassifyScoringDay: %v", err)
			}
			if got.Scoring != tc.wantScoring {
				t.Errorf("Scoring = %v, want %v (reason %q)", got.Scoring, tc.wantScoring, got.Reason)
			}
			switch {
			case tc.wantPeriod == nil && got.Period != nil:
				t.Errorf("Period = %+v, want nil", *got.Period)
			case tc.wantPeriod != nil && (got.Period == nil || got.Period.Number != tc.wantPeriod.Number):
				t.Errorf("Period = %+v, want number %d", got.Period, tc.wantPeriod.Number)
			}
			if tc.wantReason == "" && got.Reason != "" {
				t.Errorf("a scoring day carries no reason, got %q", got.Reason)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("Reason = %q, want it to mention %q", got.Reason, tc.wantReason)
			}
			if len(wb.calls) != tc.wantLookups {
				t.Errorf("matchup-week lookups = %d, want %d", len(wb.calls), tc.wantLookups)
			}
		})
	}
}

// A lookup that cannot answer is an error, never a verdict: an unknown
// bracket status is not evidence that the day scored for nobody, and the
// callers each decide what an unknown costs them (the lineup path optimizes
// anyway, the gap writer withholds the day).
func TestClassifyScoringDay_LookupFailureIsAnError(t *testing.T) {
	round2 := ScoringPeriod{Number: 24, Caption: "Playoffs - Round 2", Playoff: true,
		StartDate: day("2026-09-14"), EndDate: day("2026-09-20")}
	wb := &fakeBounder{err: errors.New("fantrax 524")}
	_, err := ClassifyScoringDay([]ScoringPeriod{round2}, wb, day("2026-03-26"), day("2026-09-15"))
	if err == nil {
		t.Fatal("want the lookup error surfaced, got a verdict")
	}
}

package lineuprun

import (
	"strings"
	"testing"
	"time"
)

// Out-of-season runs reach this phase through the explicit --dates path, where
// ResolveDates does no matchup lookup and so never short-circuits. The gate is
// correctly off — there are no games left to gate — but before this it looked
// identical to the fail-open cascade the WARNING lines describe. Production
// logged "no matchup week found for today — GS limit disabled" on every run
// after 2026-09-07, with both the weekly cap and the GS floor silently
// dropped and the run still exiting 0.
func TestComputeGSBudget_OutOfSeasonIsQuiet(t *testing.T) {
	cases := []struct {
		name  string
		today time.Time
	}{
		{"after the season's final day", day(2026, 9, 7)},
		{"before the opener", day(2026, 3, 20)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ft := healthyGS()
			in := gsInputs()
			in.Today = tc.today
			in.SeasonStart = day(2026, 3, 26)
			in.SeasonEnd = day(2026, 9, 6)

			got := ComputeGSBudget(t.Context(), ft, &fakeSchedule{}, in)

			if got.Budget != nil {
				t.Errorf("Budget = %+v, want nil — nothing to gate out of season", got.Budget)
			}
			if logs := logsJoined(got); strings.Contains(logs, "WARNING") {
				t.Errorf("an out-of-season run must not warn, logs:\n%s", logs)
			}
			if got.Alert != nil {
				t.Errorf("an out-of-season run must not request a Pushover: %+v", got.Alert)
			}
			notices := strings.Join(got.Notices, "\n")
			if !strings.Contains(notices, "GS limit not applicable") {
				t.Errorf("notices = %q, want a line stating why the gate is off", notices)
			}
			// Notices, not Logs: prog.Logf is verbose-only, and a gate that
			// turns off invisibly in production is the failure mode this
			// package treats as a bug. See GSDecision.Notices.
			if len(got.Notices) == 0 {
				t.Error("the explanation must ride Notices so it survives a non-verbose run")
			}
			// Run renders a nil budget as a WARNING unless it is told the gate
			// was inapplicable rather than broken. Without this the quiet path
			// still printed "GS budget: unavailable — limit disabled".
			if !got.Inapplicable {
				t.Error("Inapplicable = false, want true so Run does not render this as a warning")
			}
		})
	}
}

// The season boundary is only knowable when the caller supplied it. A zero
// SeasonEnd means the range was never fetched, and guessing "out of season"
// from a missing value would disable the gate for the wrong reason.
func TestComputeGSBudget_UnknownSeasonEndDoesNotSuppressTheWarning(t *testing.T) {
	ft := healthyGS()
	ft.weekStart = time.Time{}
	in := gsInputs()
	in.Today = day(2026, 9, 7)
	in.SeasonEnd = time.Time{}

	got := ComputeGSBudget(t.Context(), ft, &fakeSchedule{}, in)

	if !strings.Contains(logsJoined(got), "no matchup week found for today") {
		t.Errorf("an unknown season end must fall through to the warning, logs:\n%s", logsJoined(got))
	}
}

// Opening day through the first published matchup row is IN season, and a
// disabled gate there costs real points. It keeps the loud path.
func TestComputeGSBudget_NoMatchupWeekInSeasonStillWarns(t *testing.T) {
	ft := healthyGS()
	ft.weekStart = time.Time{}
	in := gsInputs()
	in.Today = day(2026, 3, 27)
	in.SeasonStart = day(2026, 3, 26)
	in.SeasonEnd = day(2026, 9, 6)

	got := ComputeGSBudget(t.Context(), ft, &fakeSchedule{}, in)

	if !strings.Contains(logsJoined(got), "no matchup week found for today") {
		t.Errorf("an in-season matchup gap must still warn, logs:\n%s", logsJoined(got))
	}
}

// Inapplicable marks "there was nothing to gate", never "the gate broke". Every
// genuine cascade failure must leave it false so Run keeps warning about them.
func TestComputeGSBudget_FailurePathsAreNotInapplicable(t *testing.T) {
	ft := healthyGS()
	ft.weekStart = time.Time{}
	in := gsInputs()
	in.SeasonEnd = day(2026, 9, 6)

	got := ComputeGSBudget(t.Context(), ft, &fakeSchedule{}, in)

	if got.Budget != nil {
		t.Fatalf("expected the gate disabled, got %+v", got.Budget)
	}
	if got.Inapplicable {
		t.Error("Inapplicable = true for a mid-season lookup failure, want false")
	}
}

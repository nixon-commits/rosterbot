package lineuprun

import (
	"testing"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
)

// A game start, once spent, cannot come back. The used-count therefore may not
// fall between two runs on the same day (rosterbot-cg8l).
//
// Production read 1/12 through the afternoon of 2026-08-19, 3/12 at 23:00Z,
// 5/12 at 00:00Z — the correct peak — and then 3/12 again an hour later, where
// it stayed. The next morning's walk over the same completed days reported 5.
// The swing was entirely countTodayStarts, which gated on live roster Status:
// once a pitcher's game finished, a later hourly run rotated him back to
// Reserve for tomorrow and his already-consumed start silently dropped out.
//
// The error direction is over-permissive — the gate believed 9 starts remained
// when 7 did — which is benign at the GS floor and inverts against the max.
func TestComputeGSBudget_SpentStartsSurviveAPitcherBeingBenched(t *testing.T) {
	sched := &fakeSchedule{
		locked:    map[string]map[string]bool{"2026-07-25": {"LAD": true}},
		probables: map[string]map[string]string{"2026-07-25": {"ace arm": "LAD"}},
	}
	ft := healthyGS()

	morning := ComputeGSBudget(ft, sched, gsInputs())
	if morning.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(morning))
	}

	// An hour later. Ace Arm has finished his start, so the optimizer has
	// rotated him back to Reserve to make room for tomorrow. Nothing about the
	// start he already made has changed.
	benched := gsInputs()
	benched.PitcherRoster[0].Status = "Reserve"
	evening := ComputeGSBudget(ft, sched, benched)
	if evening.Budget == nil {
		t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(evening))
	}

	if evening.Budget.Used < morning.Budget.Used {
		t.Errorf("Used went backwards: %d then %d — a spent game start cannot be un-spent",
			morning.Budget.Used, evening.Budget.Used)
	}
	if evening.Budget.Used != 5 {
		t.Errorf("Used = %d, want 5 (4 earlier in the week + Ace Arm's start today)",
			evening.Budget.Used)
	}
}

// The mechanism behind the test above: today's consumed starts come from the
// same per-day active-slot snapshot walk that the rest of the week does, so the
// walk must be asked to cover today. Nothing else in the phase can see a start
// that the live roster has already rotated away from.
func TestComputeGSBudget_WalkCoversThroughToday(t *testing.T) {
	ft := healthyGS()

	ComputeGSBudget(ft, &fakeSchedule{}, gsInputs())

	today := day(2026, 7, 25)
	if !ft.gsFor.EndDate.Equal(today) {
		t.Errorf("walk asked for days through %s, want %s (today) — today's starts are invisible otherwise",
			ft.gsFor.EndDate.Format("2006-01-02"), today.Format("2006-01-02"))
	}
	if !ft.gsFor.StartDate.Equal(day(2026, 7, 20)) {
		t.Errorf("walk StartDate = %v, want the matchup week start", ft.gsFor.StartDate)
	}
}

// A start still counts when the pitcher is benched, injured or optioned later
// in the day — none of which un-makes the start. This is the property the old
// live-Status filter could not express, stated directly rather than through the
// two-run sequence above.
func TestComputeGSBudget_UsedIgnoresLiveRosterStatus(t *testing.T) {
	sched := &fakeSchedule{
		locked:    map[string]map[string]bool{"2026-07-25": {"LAD": true}},
		probables: map[string]map[string]string{"2026-07-25": {"ace arm": "LAD"}},
	}

	for _, tc := range []struct {
		name  string
		apply func(*fantrax.Player)
	}{
		{"rotated to reserve", func(p *fantrax.Player) { p.Status = "Reserve" }},
		{"placed on the IL", func(p *fantrax.Player) { p.IsInjured = true }},
		{"optioned to the minors", func(p *fantrax.Player) { p.InMinors = true }},
		{"dropped from the roster", func(p *fantrax.Player) { *p = fantrax.Player{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := gsInputs()
			tc.apply(&in.PitcherRoster[0])

			got := ComputeGSBudget(healthyGS(), sched, in)

			if got.Budget == nil {
				t.Fatalf("expected a budget, got disabled. logs:\n%s", logsJoined(got))
			}
			if got.Budget.Used != 5 {
				t.Errorf("Used = %d, want 5 — the start was already made", got.Budget.Used)
			}
		})
	}
}

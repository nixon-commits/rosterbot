package lineuprun

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// The Wednesday both reference weeks are measured from. Every fixture below
// runs Thursday..Sunday so "four days left" is the shared starting point.
var gsFloorToday = time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)

func gsDay(offset int) time.Time { return gsFloorToday.AddDate(0, 0, offset) }

// budget assembles a *GSBudget from a compact per-day description so the
// fixtures below read as the week's shape rather than as struct literals.
func gsFloorBudget(floor, limit, used int, days ...optimizer.DayForecast) *optimizer.GSBudget {
	return &optimizer.GSBudget{
		Limit: limit, Floor: floor, Used: used,
		Today:    gsFloorToday,
		WeekEnd:  gsDay(4),
		Forecast: days,
	}
}

func fc(offset int, confirmed []float64, estimated float64) optimizer.DayForecast {
	return optimizer.DayForecast{Date: gsDay(offset), ConfirmedStarters: confirmed, Estimated: estimated}
}

// Period 21 (2026-08-24..08-30), read at the 08-26T14:00Z run: 5/12 used
// against a floor of 10, with roughly 7.4 starts still forecast. It finished
// comfortably clear, and it is the bead's named acceptance criterion that a
// week like this must never page — an alert that fires on a healthy week is
// muted, and a muted alert is worth less than the gap it was filling.
func TestEvaluateGSFloor_DoesNotFireOnAWeekThatReachesTheFloor(t *testing.T) {
	b := gsFloorBudget(10, 12, 5,
		fc(1, []float64{14.2}, 1.4),
		fc(2, nil, 1.6),
		fc(3, []float64{11.8}, 1.2),
		fc(4, nil, 1.2),
	)

	f := evaluateGSFloor(b)

	if f.Fires {
		t.Fatalf("fired on a week with %.1f credited against a need of %d — this is the "+
			"week the bead names as the must-not-fire case", f.Supply, f.Need)
	}
	if f.Need != 5 {
		t.Errorf("Need = %d, want 5 (floor 10 - used 5)", f.Need)
	}
}

// Period 20 (2026-08-17..08-23) finished exactly ON its minimum, 10/10, with
// two days on which no rostered arm took the ball. That is the week the alert
// exists for, and the one gs-check could only describe the morning after.
func TestEvaluateGSFloor_FiresOnAWeekTrackingUnderTheFloor(t *testing.T) {
	b := gsFloorBudget(10, 12, 4,
		fc(1, nil, 0), // clubs played, all named someone else
		fc(2, []float64{9.5}, 0),
		fc(3, nil, 1.0),
		fc(4, nil, 1.0),
	)

	f := evaluateGSFloor(b)

	if !f.Fires {
		t.Fatalf("did not fire: need %d, credited %.2f over %d days — this week finished on its floor",
			f.Need, f.Supply, f.DaysLeft)
	}
	if len(f.EmptyDays) != 1 || !f.EmptyDays[0].Equal(gsDay(1)) {
		t.Errorf("EmptyDays = %v, want exactly %v — naming the day is the actionable half",
			f.EmptyDays, gsDay(1))
	}
}

// The credit is the one knob that decides whether this alert is useful or
// muted, so it gets a case that fails if it moves to either edge. Crediting the
// estimate in FULL (1.0) leaves this week looking covered; crediting it at 0.8
// reveals the shortfall. A test that only exercised a lopsided week would pass
// at every credit value and pin nothing.
func TestEvaluateGSFloor_EstimateCreditIsWhatSeparatesTheseWeeks(t *testing.T) {
	b := gsFloorBudget(10, 12, 5,
		fc(1, []float64{12.0}, 1.3),
		fc(2, nil, 1.2),
		fc(3, nil, 1.2),
		fc(4, nil, 1.2),
	)
	// confirmed 1 + estimated 4.9. At credit 1.0 supply is 5.9 against a need
	// of 5 and nothing fires; at 0.8 it is 4.92 and the week is short.
	f := evaluateGSFloor(b)

	if !f.Fires {
		t.Fatalf("did not fire at credit %.2f (supply %.2f vs need %d); at full credit "+
			"supply would be 5.90 and this week would look covered",
			gsFloorEstimateCredit, f.Supply, f.Need)
	}
	if raw := 1.0 + 4.9; f.Supply >= raw {
		t.Errorf("Supply = %.2f, but an undiscounted week is %.2f — the estimate is not being discounted at all",
			f.Supply, raw)
	}
}

// Confirmed starts are NOT discounted: a club that has named one of our
// pitchers is as certain as today is. Discounting them too would flatten the
// very signal the trigger reads as the week's estimate collapses.
func TestEvaluateGSFloor_ConfirmedStartsAreCreditedInFull(t *testing.T) {
	b := gsFloorBudget(10, 12, 5,
		fc(1, []float64{12.0, 9.0}, 0),
		fc(2, []float64{8.0}, 0),
		fc(3, nil, 0),
		fc(4, nil, 0),
	)

	f := evaluateGSFloor(b)

	if f.Supply != 3 {
		t.Fatalf("Supply = %.3f, want exactly 3 — three confirmed starts must be credited in full", f.Supply)
	}
	if !f.Fires {
		t.Error("need 5 against 3 confirmed is a real shortfall and should fire")
	}
}

// Today is deliberately excluded from supply: its starts land in Used the
// moment Fantrax settles them, and nothing at this seam distinguishes a settled
// start from a pending one — the ambiguity that made the old live-roster count
// diverge every evening (rosterbot-cg8l). A forecast entry dated today must
// therefore change nothing.
func TestEvaluateGSFloor_TodayIsNeverCountedAsSupply(t *testing.T) {
	base := []optimizer.DayForecast{
		fc(1, nil, 0),
		fc(2, nil, 0.5),
		fc(3, nil, 0.5),
		fc(4, nil, 0.5),
	}
	without := evaluateGSFloor(gsFloorBudget(10, 12, 4, base...))
	withToday := evaluateGSFloor(gsFloorBudget(10, 12, 4,
		append([]optimizer.DayForecast{fc(0, []float64{20.0, 18.0, 16.0}, 2.0)}, base...)...))

	if withToday.Supply != without.Supply {
		t.Fatalf("today's forecast entry moved Supply %.2f -> %.2f; today must never be credited",
			without.Supply, withToday.Supply)
	}
	if withToday.DaysLeft != without.DaysLeft {
		t.Errorf("today counted as a remaining day: %d vs %d", withToday.DaysLeft, without.DaysLeft)
	}
}

// An alert on the final evening names a problem that can no longer be acted on.
// The only lever is a roster claim, and a claim needs to clear waivers and then
// have the pitcher actually take the ball.
func TestEvaluateGSFloor_DoesNotFireWithNoTimeLeftToAct(t *testing.T) {
	b := gsFloorBudget(10, 12, 4, fc(1, nil, 0))
	b.WeekEnd = gsDay(1)

	f := evaluateGSFloor(b)

	if f.DaysLeft >= gsFloorMinDaysLeft {
		t.Fatalf("fixture has %d days left, needs to be under the %d-day guard to test it",
			f.DaysLeft, gsFloorMinDaysLeft)
	}
	if f.Fires {
		t.Errorf("fired with %d fc(s) left: nothing can be claimed and started in that window", f.DaysLeft)
	}
	if f.Shortfall <= 0 {
		t.Error("fixture must carry a real shortfall, otherwise the guard is not what suppressed it")
	}
}

// A met floor and an absent floor both yield Need == 0, and neither is worth a
// word. Firing on either is how the channel gets muted.
func TestEvaluateGSFloor_QuietWhenTheFloorIsMetOrUnset(t *testing.T) {
	empty := []optimizer.DayForecast{fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 0), fc(4, nil, 0)}

	if f := evaluateGSFloor(gsFloorBudget(10, 12, 10, empty...)); f.Fires {
		t.Error("fired on a week that has already reached its floor")
	}
	if f := evaluateGSFloor(gsFloorBudget(0, 12, 0, empty...)); f.Fires {
		t.Error("fired with no GS minimum configured — there is no floor to be short of")
	}
	if f := evaluateGSFloor(nil); f.Fires {
		t.Error("fired on a nil budget: the gate is disabled and no floor reading was taken")
	}
}

// The Need > 0 clause of the trigger looks redundant — with a well-formed
// forecast Supply is never negative, so a zero Need can never produce a
// positive shortfall — and mutation testing confirmed that removing it changes
// nothing on any ordinary week. It is kept, and pinned here, because
// evaluateGSFloor takes an arbitrary *GSBudget rather than only the one
// buildGSForecast produces: that constructor clamps Estimated at zero, nothing
// in the type does, and a negative estimate flips the shortfall positive. The
// clause is what stops a malformed forecast raising a floor alert for a league
// that has no floor at all.
func TestEvaluateGSFloor_ANegativeEstimateCannotConjureAShortfall(t *testing.T) {
	b := gsFloorBudget(0, 12, 0,
		fc(1, nil, -3),
		fc(2, nil, -3),
	)

	f := evaluateGSFloor(b)

	if f.Supply >= 0 {
		t.Fatalf("fixture is meant to produce a negative Supply, got %.2f", f.Supply)
	}
	if f.Shortfall <= 0 {
		t.Fatalf("fixture is meant to produce a positive shortfall, got %.2f", f.Shortfall)
	}
	if f.Fires {
		t.Error("raised a floor alert for a league with no configured minimum, off a negative estimate")
	}
}

// A day carrying a fractional estimate is not empty — a turn may still come
// from it — so it must not be reported as one. Reporting it as empty would send
// the manager shopping for a day that is already half covered.
func TestEvaluateGSFloor_ThinDaysAreNotEmptyDays(t *testing.T) {
	f := evaluateGSFloor(gsFloorBudget(10, 12, 4,
		fc(1, nil, 0),
		fc(2, nil, 0.4),
		fc(3, nil, 0.4),
		fc(4, nil, 0.4),
	))

	if len(f.EmptyDays) != 1 || !f.EmptyDays[0].Equal(gsDay(1)) {
		t.Errorf("EmptyDays = %v, want only the genuinely empty day %v", f.EmptyDays, gsDay(1))
	}
	if len(f.ThinDays) != 3 {
		t.Errorf("ThinDays = %v, want the three fractional days", f.ThinDays)
	}
}

// --- reportGSFloor: the check -> send -> mark contract ---

type gsFloorHarness struct {
	out     *bytes.Buffer
	markers *fakeMarkers
	sent    []string
	sendErr error
}

func (h *gsFloorHarness) run(b *optimizer.GSBudget, dryRun bool) {
	reportGSFloor(gsFloorInputs{
		Budget: b, Period: 21, Season: 2026,
		Markers: h.markers,
		Notify: func(msg string) error {
			if h.sendErr != nil {
				return h.sendErr
			}
			h.sent = append(h.sent, msg)
			return nil
		},
		DryRun: dryRun,
		Out:    h.out,
	})
}

func newGSFloorHarness() *gsFloorHarness {
	return &gsFloorHarness{out: &bytes.Buffer{}, markers: newFakeMarkers()}
}

// The week that fires, twice. A standing shortfall must announce itself once,
// not once per hourly run — every scheduled run is a fresh container, so
// in-process state cannot carry this and the durable marker is the whole
// mechanism.
func TestReportGSFloor_AlertsOncePerPeriod(t *testing.T) {
	h := newGSFloorHarness()
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))

	h.run(short, false)
	h.run(short, false)

	if len(h.sent) != 1 {
		t.Fatalf("sent %d alerts across two runs, want exactly 1", len(h.sent))
	}
	if h.markers.publCalls != 1 {
		t.Errorf("marker written %d times, want 1", h.markers.publCalls)
	}
	if !strings.Contains(h.sent[0], "Thu Aug 27") || !strings.Contains(h.sent[0], "Fri Aug 28") {
		t.Errorf("message must name the empty days, got: %s", h.sent[0])
	}
}

// A failed send must never be marked. Marking it would convert one dropped
// push into permanent silence for the rest of the period — the precise
// inversion of this repo's rule that a failure degrades to noise, never to
// silence.
func TestReportGSFloor_AFailedSendIsNotMarked(t *testing.T) {
	h := newGSFloorHarness()
	h.sendErr = errors.New("dispatcher down")
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))

	h.run(short, false)

	if h.markers.publCalls != 0 {
		t.Fatal("marked an alert that was never delivered: the next run would stay silent")
	}

	// Recovery: once the dispatcher is back, the same run sends.
	h.sendErr = nil
	h.run(short, false)
	if len(h.sent) != 1 {
		t.Errorf("sent %d after recovery, want 1", len(h.sent))
	}
}

// A marker store that cannot be written degrades to a duplicate alert next
// hour. That is the correct direction and the alert itself must still go out.
func TestReportGSFloor_MarkerWriteFailureStillDelivers(t *testing.T) {
	h := newGSFloorHarness()
	h.markers.publErr = errors.New("s3 down")
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))

	h.run(short, false)

	if len(h.sent) != 1 {
		t.Fatalf("sent %d, want 1 — a marker failure must not suppress the alert", len(h.sent))
	}
	if !strings.Contains(h.out.String(), "a later run will alert again") {
		t.Error("a marker write failure must say so, so the duplicate is explained rather than mysterious")
	}
}

// Nil markers disable dedup, not the alert: with no record of what was sent,
// repeating is recoverable and going quiet is not. This is the local-dev path.
func TestReportGSFloor_NilMarkersAlertEveryRun(t *testing.T) {
	h := newGSFloorHarness()
	h.markers = nil
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))

	reportGSFloor(gsFloorInputs{
		Budget: short, Period: 21, Season: 2026, Markers: nil,
		Notify: func(msg string) error { h.sent = append(h.sent, msg); return nil },
		Out:    h.out,
	})
	reportGSFloor(gsFloorInputs{
		Budget: short, Period: 21, Season: 2026, Markers: nil,
		Notify: func(msg string) error { h.sent = append(h.sent, msg); return nil },
		Out:    h.out,
	})

	if len(h.sent) != 2 {
		t.Errorf("sent %d with no marker store, want 2 (repeat rather than stay quiet)", len(h.sent))
	}
}

// A dry run must neither send nor mark. Marking would mute the real alert the
// next live run would otherwise raise — and `shadow` drives this whole pipeline
// four times per day in dry-run.
func TestReportGSFloor_DryRunNeitherSendsNorMarks(t *testing.T) {
	h := newGSFloorHarness()
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))

	h.run(short, true)

	if len(h.sent) != 0 || h.markers.publCalls != 0 {
		t.Fatalf("dry run sent %d and marked %d, want 0 and 0", len(h.sent), h.markers.publCalls)
	}

	// And the dry run must not have muted the live one.
	h.run(short, false)
	if len(h.sent) != 1 {
		t.Errorf("live run after a dry run sent %d, want 1", len(h.sent))
	}
}

// The coverage line prints on a quiet week too. This check's normal output is
// silence, so without the inputs a comfortable week and a structurally blind
// trigger read identically — which is exactly how the GS forecast reported 0.0
// all season without anyone noticing (rosterbot-1jvj).
func TestReportGSFloor_CoverageLinePrintsOnAQuietWeek(t *testing.T) {
	h := newGSFloorHarness()
	fine := gsFloorBudget(10, 12, 5,
		fc(1, []float64{14.2}, 1.4), fc(2, nil, 1.6), fc(3, []float64{11.8}, 1.2), fc(4, nil, 1.2))

	h.run(fine, false)

	got := h.out.String()
	if len(h.sent) != 0 {
		t.Fatalf("alerted on a comfortable week: %v", h.sent)
	}
	if !strings.Contains(got, "gs floor check:") {
		t.Fatalf("no coverage line on a quiet week; a silent check cannot be told from a blind one:\n%s", got)
	}
	for _, want := range []string{"5/12 used", "floor 10", "remaining day"} {
		if !strings.Contains(got, want) {
			t.Errorf("coverage line missing %q:\n%s", want, got)
		}
	}
}

// An absent minimum renders as "none configured", never as "floor 0". A floor
// of zero and no floor at all are different facts and only one is a league
// setting — the rule FormatGateSummary already follows.
func TestReportGSFloor_NoMinimumSaysSoRatherThanZero(t *testing.T) {
	h := newGSFloorHarness()

	h.run(gsFloorBudget(0, 12, 3, fc(1, nil, 0), fc(2, nil, 0)), false)

	got := h.out.String()
	if !strings.Contains(got, "no GS minimum configured") {
		t.Errorf("want an explicit 'none configured', got:\n%s", got)
	}
	if strings.Contains(got, "floor 0") {
		t.Errorf("rendered an absent floor as a zero one:\n%s", got)
	}
}

// A nil budget means the gate is disabled and ComputeGSBudget already logged
// why. Restating it here in the coverage line's voice would imply a floor
// reading that was never taken.
func TestReportGSFloor_SaysNothingWhenTheGateIsDisabled(t *testing.T) {
	h := newGSFloorHarness()

	h.run(nil, false)

	if got := h.out.String(); got != "" {
		t.Errorf("wrote %q for a disabled gate; the cascade's own WARNING is the record", got)
	}
}

// Weekly period numbers restart every season, so a season-less key would let
// next season's week 21 find this season's marker and stay silent for the whole
// week — the correction rosterbot-qoa made to the past-period cache keys.
func TestGSFloorMarkerKey_IsScopedToTheSeason(t *testing.T) {
	if a, b := gsFloorMarkerKey(2026, 21), gsFloorMarkerKey(2027, 21); a == b {
		t.Fatalf("period 21 collides across seasons: %q == %q", a, b)
	}
	if a, b := gsFloorMarkerKey(2026, 21), gsFloorMarkerKey(2026, 22); a == b {
		t.Fatalf("two periods in one season collide: %q == %q", a, b)
	}
}

// The activity feed badges an entry from the message text before it consults
// the Kind, and Kind "lineup" — which this alert shares with the ordinary
// lineup notification — otherwise classifies as a success. A week heading under
// the floor must never be filed as one, so the glyph that drives the
// classification is pinned here rather than left to whoever next edits the
// wording.
func TestGSFloorMessage_IsNotBadgedAsASuccess(t *testing.T) {
	msg := gsFloorMessage(evaluateGSFloor(gsFloorBudget(10, 12, 4,
		fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))))

	if got := lineupapi.ClassifyStatus("lineup", "GS Floor Risk", msg); got == "success" {
		t.Errorf("the feed badges this alert as %q; a week projected under the floor is not a success.\n"+
			"ClassifyStatus keys on a leading ⚠ before it reads the Kind — the glyph is what carries this.\nmessage: %s",
			got, msg)
	}
}

// realPeriod21Monday is the ACTUAL forecast the production Lineup job recorded
// for Monday 2026-08-24, read from that run's projection snapshot
// (.backtest/user=…/snapshots/2026-08-24.json, gs_floor 10, gs_limit 12 — the
// first week whose snapshots carry gs_floor/gs_forecast at all).
//
// Period 21 is the bead's designated MUST-NOT-FIRE week: it finished roughly
// three starts clear of its minimum. An earlier version of this trigger fired
// on it anyway, every Monday, because supply excludes today while Used is still
// zero — 8.92 credited against a Need of the full floor. Per-period dedup means
// that false alert would have spent the only alert the week could ever raise.
func realPeriod21Monday(used int) *optimizer.GSBudget {
	return &optimizer.GSBudget{
		Limit: 12, Floor: 10, Used: used,
		Today:   gsFloorToday.AddDate(0, 0, -2), // Mon 2026-08-24
		WeekEnd: gsFloorToday.AddDate(0, 0, 4),  // Sun 2026-08-30
		Forecast: []optimizer.DayForecast{
			{Date: gsFloorToday.AddDate(0, 0, -1), ConfirmedStarters: []float64{11.0}, Estimated: 0.4},
			{Date: gsFloorToday, ConfirmedStarters: []float64{11.0}, Estimated: 0.4},
			{Date: gsFloorToday.AddDate(0, 0, 1), ConfirmedStarters: []float64{11.0}, Estimated: 0.4},
			{Date: gsFloorToday.AddDate(0, 0, 2), Estimated: 2.0},
			{Date: gsFloorToday.AddDate(0, 0, 3), Estimated: 2.2},
			{Date: gsFloorToday.AddDate(0, 0, 4), Estimated: 2.0},
		},
	}
}

func TestEvaluateGSFloor_RealPeriod21MondayStaysQuiet(t *testing.T) {
	// Used stays 0 through every daytime run: the Lineup cron is hourly
	// 14:00-03:00 UTC and Monday's games do not settle until that evening.
	for _, used := range []int{0, 1, 2, 3} {
		f := evaluateGSFloor(realPeriod21Monday(used))
		if f.Fires {
			t.Errorf("used=%d: fired on real Period 21 Monday (need %d, supply %.2f, %d days left) — "+
				"this week finished ~3 clear of its floor and is the must-not-fire case",
				used, f.Need, f.Supply, f.DaysLeft)
		}
	}

	// And the numbers are the recorded ones, so a fixture drifting away from
	// production fails here rather than quietly becoming a synthetic week.
	f := evaluateGSFloor(realPeriod21Monday(0))
	if f.DaysLeft != 6 {
		t.Errorf("DaysLeft = %d, want 6 (Tue..Sun)", f.DaysLeft)
	}
	if got := 3 + gsFloorEstimateCredit*7.4; f.Supply < got-0.01 || f.Supply > got+0.01 {
		t.Errorf("Supply = %.3f, want %.3f (confirmed 3 + %.1f x estimated 7.4)",
			f.Supply, got, gsFloorEstimateCredit)
	}
}

// Both ends of the firing window, pinned as a table. The reviewer's mutation
// audit found the DaysLeft >= boundary untested — >= 2 could be swapped for > 2
// and the whole suite stayed green — so each boundary day is asserted on both
// sides with an otherwise identical, genuinely short week.
func TestEvaluateGSFloor_FiresOnlyInsideTheDayWindow(t *testing.T) {
	// A week needing six more starts with essentially nothing coming: short by
	// any measure, so only the day window can decide the outcome.
	shortWeek := func(days int) *optimizer.GSBudget {
		fcs := make([]optimizer.DayForecast, 0, days)
		for i := 1; i <= days; i++ {
			fcs = append(fcs, optimizer.DayForecast{Date: gsDay(i), Estimated: 0.1})
		}
		return &optimizer.GSBudget{
			Limit: 12, Floor: 10, Used: 4,
			Today: gsFloorToday, WeekEnd: gsDay(days), Forecast: fcs,
		}
	}

	for _, tc := range []struct {
		daysLeft  int
		wantFires bool
		why       string
	}{
		{1, false, "below gsFloorMinDaysLeft: nothing can be claimed and started"},
		{2, true, "exactly gsFloorMinDaysLeft — the last day the alert is still actionable"},
		{3, true, "inside the window"},
		{4, true, "exactly gsFloorMaxDaysLeft — the first day the projection is informative"},
		{5, false, "above gsFloorMaxDaysLeft: real Period 21 read -0.12 here, inside firing noise"},
		{6, false, "week start: Used is still zero while today's starts are excluded from supply"},
	} {
		f := evaluateGSFloor(shortWeek(tc.daysLeft))
		if f.DaysLeft != tc.daysLeft {
			t.Fatalf("fixture built %d days, got DaysLeft=%d", tc.daysLeft, f.DaysLeft)
		}
		if f.Shortfall <= 0 {
			t.Fatalf("daysLeft=%d: fixture must be genuinely short, got shortfall %.2f", tc.daysLeft, f.Shortfall)
		}
		if f.Fires != tc.wantFires {
			t.Errorf("daysLeft=%d: Fires = %v, want %v — %s", tc.daysLeft, f.Fires, tc.wantFires, tc.why)
		}
	}
}

// The guard must not have bought quiet by making the alert unable to speak.
// Same roster shape and same mid-week horizon as the real Period 21 Wednesday,
// but with a week that is genuinely behind: it has to fire.
func TestEvaluateGSFloor_StillFiresOnAGenuinelyShortMidweek(t *testing.T) {
	// Real 2026-08-26 forecast: confirmed 4, estimated 4.2, four days left.
	realWednesday := func(used int) *optimizer.GSBudget {
		return &optimizer.GSBudget{
			Limit: 12, Floor: 10, Used: used,
			Today: gsFloorToday, WeekEnd: gsDay(4),
			Forecast: []optimizer.DayForecast{
				{Date: gsDay(1), ConfirmedStarters: []float64{11}, Estimated: 0.2},
				{Date: gsDay(2), ConfirmedStarters: []float64{11}, Estimated: 1.2},
				{Date: gsDay(3), Estimated: 1.2},
				{Date: gsDay(4), ConfirmedStarters: []float64{11, 10}, Estimated: 1.6},
			},
		}
	}

	// The real week: five banked by Wednesday. Comfortable, stays quiet.
	if f := evaluateGSFloor(realWednesday(5)); f.Fires {
		t.Errorf("fired on the real Period 21 Wednesday (need %d, supply %.2f)", f.Need, f.Supply)
	}
	// The counterfactual: same days and same rotation, two banked instead of
	// five. That week really is heading under and must be announced.
	if f := evaluateGSFloor(realWednesday(2)); !f.Fires {
		t.Errorf("stayed quiet on a genuinely short week (need %d, supply %.2f, %d days left) — "+
			"the day window has made the trigger unable to fire at all",
			f.Need, f.Supply, f.DaysLeft)
	}
}

// Period 22 (2026-08-31..09-06) read at the 2026-09-02T14:01Z run, the alert's
// first-ever live fire. Fantrax had settled 2 starts (Mon + Tue) into Used, and
// two more of our SPs — Eury Perez and Noah Cameron — were confirmed probables
// for TODAY. Those two are in neither term: the forecast begins tomorrow
// (buildGSForecast iterates today+1..weekEnd) and Used will not carry them
// until Fantrax settles them that evening.
//
// Production reported "about 2.7 short" and paged on it. The honest figure was
// 0.7: need 8 - today's 2 = 6, against 5.3 credited. Since the alert is
// deduped to one send per matchup week, that overstatement was the only thing
// the week ever said (rosterbot-ogtq).
func TestEvaluateGSFloor_TodaysUnsettledStartsAreCreditedAgainstNeed(t *testing.T) {
	b := gsFloorBudget(10, 12, 2,
		fc(1, nil, 0.8),
		fc(2, nil, 1.8),
		fc(3, nil, 2.0),
		fc(4, nil, 2.0),
	)
	b.TodayUnsettled = 2

	f := evaluateGSFloor(b)

	if f.Need != 6 {
		t.Errorf("Need = %d, want 6 (floor 10 - used 2 - today's 2 confirmed)", f.Need)
	}
	if got := f.Shortfall; got < 0.7 || got > 0.75 {
		t.Errorf("Shortfall = %.2f, want ~0.72 — production reported 2.7 by dropping today entirely", got)
	}
	if !f.Fires {
		t.Error("the week is genuinely short and inside the day window; it must still fire")
	}
}

// The same week eight hours later, after Fantrax settled today's two starts
// into Used. TodayUnsettled collapses to 0 as those pitchers lock, so Need
// falls by the same 2 that leaves the correction. The shortfall must be
// IDENTICAL across that boundary.
//
// This is the regression that matters: before the fix the reported shortfall
// stepped +1.5 across every day roll (03:01Z read +1.2, 11:30Z read +2.7 on
// unchanged roster facts), so the once-per-week alert fired at the top of a
// sawtooth rather than on the week's real shape.
func TestEvaluateGSFloor_ShortfallIsFlatAcrossSettlement(t *testing.T) {
	days := []optimizer.DayForecast{
		fc(1, nil, 0.8), fc(2, nil, 1.8), fc(3, nil, 2.0), fc(4, nil, 2.0),
	}

	morning := gsFloorBudget(10, 12, 2, days...)
	morning.TodayUnsettled = 2

	evening := gsFloorBudget(10, 12, 4, days...)
	evening.TodayUnsettled = 0

	gotM, gotE := evaluateGSFloor(morning), evaluateGSFloor(evening)

	if gotM.Shortfall != gotE.Shortfall {
		t.Errorf("shortfall moved %.2f -> %.2f across settlement on unchanged roster facts",
			gotM.Shortfall, gotE.Shortfall)
	}
	if gotM.Need != gotE.Need {
		t.Errorf("Need moved %d -> %d across settlement", gotM.Need, gotE.Need)
	}
}

// A correction can close the gap but never invert it: more confirmed starts
// today than the floor still needs must leave Need at 0 and stay silent, the
// same contract NeedForFloor already honours for an over-met floor.
func TestEvaluateGSFloor_TodayCreditCannotDriveNeedNegative(t *testing.T) {
	b := gsFloorBudget(10, 12, 9, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 0), fc(4, nil, 0))
	b.TodayUnsettled = 5

	f := evaluateGSFloor(b)

	if f.Need != 0 {
		t.Errorf("Need = %d, want 0 — one short of the floor with 5 confirmed today is met, not -4", f.Need)
	}
	if f.Fires {
		t.Error("fired on a week the floor correction shows is already met")
	}
}

// The message states its own arithmetic, so every term it nets off has to be
// visible in it. Once today's confirmed starts are credited against Need, a
// message reporting only "used" and "more expected" no longer adds up to the
// shortfall it quotes: Period 22's real numbers would read "2 used, 5.3 more
// expected ... about 0.7 short" and invite the reader to compute 2.7.
func TestGSFloorMessage_AccountsForTodaysStartsInItsArithmetic(t *testing.T) {
	b := gsFloorBudget(10, 12, 2,
		fc(1, nil, 0.8), fc(2, nil, 1.8), fc(3, nil, 2.0), fc(4, nil, 2.0))
	b.TodayUnsettled = 2

	msg := gsFloorMessage(evaluateGSFloor(b))

	if !strings.Contains(msg, "2 starting today") {
		t.Errorf("message hides the term that makes it add up (used 2 + today 2 + supply 5.3 vs floor 10):\n%s", msg)
	}
	if !strings.Contains(msg, "0.7 short") {
		t.Errorf("message should quote the corrected shortfall:\n%s", msg)
	}
}

// A week with nothing starting today must read exactly as it did before the
// correction existed — no dangling "0 starting today" clause.
func TestGSFloorMessage_OmitsTodayWhenThereIsNothingToSay(t *testing.T) {
	msg := gsFloorMessage(evaluateGSFloor(gsFloorBudget(10, 12, 4,
		fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0), fc(4, nil, 1.0))))

	if strings.Contains(msg, "starting today") {
		t.Errorf("no starts today, so the clause must not appear:\n%s", msg)
	}
}

// The coverage line is what the daily audit greps to reconstruct a week, and a
// Need that silently nets off today reads as a week that simply used more.
// Show the correction so the line stays reconcilable against Used and Floor.
func TestReportGSFloor_CoverageLineShowsTodaysCredit(t *testing.T) {
	h := newGSFloorHarness()
	b := gsFloorBudget(10, 12, 2,
		fc(1, nil, 0.8), fc(2, nil, 1.8), fc(3, nil, 2.0), fc(4, nil, 2.0))
	b.TodayUnsettled = 2

	h.run(b, false)

	got := h.out.String()
	if !strings.Contains(got, "6 needed after 2 confirmed today") {
		t.Errorf("coverage line does not show why Need fell from 8 to 6:\n%s", got)
	}
}

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

// The Thursday both reference weeks are measured from. Every fixture below
// runs Friday..Sunday so "three days left" — gsFloorMaxDaysLeft, the first day
// the 150-week replay found the projection informative — is the shared starting
// point.
var gsFloorToday = time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

func gsDay(offset int) time.Time { return gsFloorToday.AddDate(0, 0, offset) }

// budget assembles a *GSBudget from a compact per-day description so the
// fixtures below read as the week's shape rather than as struct literals.
func gsFloorBudget(floor, limit, used int, days ...optimizer.DayForecast) *optimizer.GSBudget {
	return &optimizer.GSBudget{
		Limit: limit, Floor: floor, Used: used,
		Today:    gsFloorToday,
		WeekEnd:  gsDay(3),
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

// The estimate is credited IN FULL, and this is the case that would fail if it
// were discounted again. The week's own rotation-math expectation covers its
// need with a little to spare; at the old 0.8 credit it does not, and the
// trigger would page on a week that is fine.
//
// That double discount is exactly what the sweep removed: the 0.8 was a
// hand-rolled correction for a flat estimator measured to over-forecast by
// +4.56 starts a week, and the estimator now corrects itself (+0.76). Keeping
// both fired on 87 of 150 replayed weeks at precision 0.253 (see
// gsFloorEstimateCredit).
func TestEvaluateGSFloor_CreditsTheEstimateInFull(t *testing.T) {
	b := gsFloorBudget(10, 12, 5,
		fc(1, []float64{12.0}, 1.4),
		fc(2, nil, 1.4),
		fc(3, nil, 1.4),
	)
	// confirmed 1 + estimated 4.2. At full credit supply is 5.2 against a need
	// of 5 and the week is covered; at 0.8 it is 4.36 and would page.
	f := evaluateGSFloor(b)

	if f.Fires {
		t.Fatalf("fired at credit %.2f (supply %.2f vs need %d); this week's own expectation covers it",
			gsFloorEstimateCredit, f.Supply, f.Need)
	}
	if discounted := 1.0 + 0.8*4.2; f.Supply <= discounted+1e-9 {
		t.Errorf("Supply = %.2f, no better than the old 0.8-discounted %.2f — the estimate is still being discounted",
			f.Supply, discounted)
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
	empty := []optimizer.DayForecast{fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 0)}

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
	))

	if len(f.EmptyDays) != 1 || !f.EmptyDays[0].Equal(gsDay(1)) {
		t.Errorf("EmptyDays = %v, want only the genuinely empty day %v", f.EmptyDays, gsDay(1))
	}
	if len(f.ThinDays) != 2 {
		t.Errorf("ThinDays = %v, want the two fractional days", f.ThinDays)
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

// gsFloorWeekAt builds a week with exactly daysLeft days remaining and nothing
// meaningful forecast on any of them, so the only things that can decide the
// outcome are the day window and Used. It is the delivery-side twin of the
// fixture in TestEvaluateGSFloor_FiresOnlyInsideTheDayWindow: the same shape,
// but driven through reportGSFloor so the assertions are about what was
// DELIVERED rather than about what the trigger concluded.
//
// Those are different facts, and Period 22 is the week that proved it — the
// trigger fired seven times and the manager was told nothing.
func gsFloorWeekAt(daysLeft, used int) *optimizer.GSBudget {
	fcs := make([]optimizer.DayForecast, 0, daysLeft)
	for i := 1; i <= daysLeft; i++ {
		fcs = append(fcs, optimizer.DayForecast{Date: gsDay(i), Estimated: 0.1})
	}
	return &optimizer.GSBudget{
		Limit: 12, Floor: 10, Used: used,
		Today: gsFloorToday, WeekEnd: gsDay(daysLeft), Forecast: fcs,
	}
}

// Two runs on one day are one decision point, and must collapse to one page.
// The hourly lineup cron runs up to fourteen times a day; every scheduled run
// is a fresh container, so in-process state cannot carry this and the durable
// marker is the whole mechanism.
func TestReportGSFloor_RepeatRunsOnOneDayAlertOnce(t *testing.T) {
	h := newGSFloorHarness()
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))

	h.run(short, false)
	h.run(short, false)

	if len(h.sent) != 1 {
		t.Fatalf("sent %d alerts across two runs, want exactly 1", len(h.sent))
	}
	if h.markers.publCalls != 1 {
		t.Errorf("marker written %d times, want 1", h.markers.publCalls)
	}
	if !strings.Contains(h.sent[0], "Fri Aug 28") || !strings.Contains(h.sent[0], "Sat Aug 29") {
		t.Errorf("message must name the empty days, got: %s", h.sent[0])
	}
}

// Each remaining day is a genuinely new decision point, and the alert gets one
// page per bucket — at most two a week, because the [gsFloorMinDaysLeft,
// gsFloorMaxDaysLeft] window bounds DaysLeft to {3,2}. Four runs across two
// days must therefore deliver two alerts, not one and not four.
//
// The upper bound matters as much as the lower one. A raw-body compare
// (alertmarker.SendOnChange) would also have unstuck this, but the shortfall
// wobbled by +0.2/+0.7/+0.8 WITHIN Period 22's window, so nearly every hourly
// run would have re-sent — trading permanent silence for a flood, which mutes
// the channel just as thoroughly.
func TestReportGSFloor_AlertsOncePerRemainingDay(t *testing.T) {
	h := newGSFloorHarness()

	// The week as it actually runs down: two runs on each of Friday, Saturday.
	for _, daysLeft := range []int{3, 3, 2, 2} {
		h.run(gsFloorWeekAt(daysLeft, 4), false)
	}

	if len(h.sent) != 2 {
		t.Fatalf("sent %d alerts over daysLeft 3,3,2,2 — want 2, one per remaining-day bucket", len(h.sent))
	}
	if h.markers.publCalls != 2 {
		t.Errorf("marker written %d times, want 2", h.markers.publCalls)
	}

	// Distinct keys are the mechanism, not an incidental detail: if the two
	// sends came from one key being re-consulted, the dedup is simply broken in
	// the other direction.
	distinct := map[string]bool{}
	for _, k := range h.markers.getKeys {
		distinct[k] = true
	}
	if len(distinct) != 2 {
		t.Errorf("consulted %d distinct marker keys %v, want 2", len(distinct), h.markers.getKeys)
	}
}

// The Period 22 regression, and the reason this bug is worth a P1.
//
// Period 22 (2026-08-31..09-06) is the first week of the season in which the GS
// floor actually bound. Its marker burned at 2026-09-02T14:01:15Z carrying the
// rosterbot-ogtq inflated magnitude — "about 2.7 short", a figure PR #193
// corrected to 0.0 two hours later — and with the marker spent, the week was
// mute for the rest of its run.
//
// It then genuinely deteriorated, and the trigger re-fired seven times on the
// live optimize path (09-03 21:01Z/22:01Z/23:01Z, 09-04 00:01Z/03:01Z/14:01Z/
// 15:01Z) at DaysLeft 3 and 2 — both inside the re-bracketed window, while a
// waiver claim could still have added a Sunday probable. Every one of those
// sends was swallowed: zero objects under notifications/user=*/ carry the title
// "GS Floor Risk" after 09-02. The week closed at exactly the floor, 10 against
// 10, with the estimate cushion at 0.00 — one scratch from tripping gscheck's
// ViolationMin, and nobody was ever told.
func TestReportGSFloor_AnEarlyFireDoesNotSuppressALaterOne(t *testing.T) {
	h := newGSFloorHarness()

	h.run(gsFloorWeekAt(3, 4), false) // the Thursday fire that burns the marker
	h.run(gsFloorWeekAt(2, 4), false) // the Friday fire Period 22 never delivered

	if len(h.sent) != 2 {
		t.Fatalf("sent %d alerts, want 2: a fire at DaysLeft 3 must not mute DaysLeft 2", len(h.sent))
	}
}

// The fix is delivery-side and must not have widened the trigger. DaysLeft 1 is
// below gsFloorMinDaysLeft and 4 is above gsFloorMaxDaysLeft, and neither may
// reach the notifier however short the week reads — so this asserts on the
// marker store too: an out-of-window run must not even consult it, because a
// key written there would mute the in-window alert that follows.
func TestReportGSFloor_DeliversNothingOutsideTheDayWindow(t *testing.T) {
	for _, daysLeft := range []int{1, 4} {
		h := newGSFloorHarness()
		h.run(gsFloorWeekAt(daysLeft, 4), false)

		if len(h.sent) != 0 {
			t.Errorf("daysLeft=%d: delivered %d alerts, want 0", daysLeft, len(h.sent))
		}
		if len(h.markers.getKeys) != 0 || h.markers.publCalls != 0 {
			t.Errorf("daysLeft=%d: touched the marker store (%d gets, %d publishes), want none",
				daysLeft, len(h.markers.getKeys), h.markers.publCalls)
		}
	}
}

// A met floor is not a near miss. Keying on DaysLeft doubles the number of
// chances the alert has to speak, so the Need > 0 guard now has twice the
// opportunity to be wrong — pin it across the whole window rather than at one
// convenient day.
func TestReportGSFloor_AMetFloorNeverAlertsAtAnyDaysLeft(t *testing.T) {
	for daysLeft := 1; daysLeft <= 4; daysLeft++ {
		h := newGSFloorHarness()
		h.run(gsFloorWeekAt(daysLeft, 10), false) // Used 10 against a floor of 10

		if len(h.sent) != 0 {
			t.Errorf("daysLeft=%d: delivered %d alerts on a met floor, want 0", daysLeft, len(h.sent))
		}
	}
}

// Period 22's final day, and the case this change must NOT "fix".
//
// Read at 2026-09-05T13:00Z by four independent sources agreeing to the digit:
// 6 settled + 2 confirmed today + 2 confirmed Sunday = 10 against a floor of
// 10. Silence here is correct and doubly determined — DaysLeft is 1, below
// gsFloorMinDaysLeft, and the shortfall is exactly 0.0. Per-day keying gives
// the alert more chances to speak, so the day it must stay quiet is worth
// pinning explicitly.
func TestReportGSFloor_Period22FinalDayStaysSilent(t *testing.T) {
	h := newGSFloorHarness()
	h.run(&optimizer.GSBudget{
		Limit: 12, Floor: 10, Used: 6,
		TodayUnsettled: 2, // the WSH and STL starters, confirmed for Saturday
		Today:          gsFloorToday,
		WeekEnd:        gsDay(1),
		Forecast: []optimizer.DayForecast{
			// Sunday: Eduardo Rodriguez (ARI) and Gage Jump (ATH).
			{Date: gsDay(1), ConfirmedStarters: []float64{11, 11}},
		},
	}, false)

	if len(h.sent) != 0 {
		t.Fatalf("delivered %d alerts on a week that finished exactly at the floor, want 0", len(h.sent))
	}
}

// A failed send must never be marked. Marking it would convert one dropped
// push into permanent silence for the rest of the period — the precise
// inversion of this repo's rule that a failure degrades to noise, never to
// silence.
func TestReportGSFloor_AFailedSendIsNotMarked(t *testing.T) {
	h := newGSFloorHarness()
	h.sendErr = errors.New("dispatcher down")
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))

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
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))

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
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))

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
	short := gsFloorBudget(10, 12, 4, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))

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
		fc(1, []float64{14.2}, 1.4), fc(2, nil, 1.6), fc(3, []float64{11.8}, 1.2))

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
	if a, b := gsFloorMarkerKey(2026, 21, 3), gsFloorMarkerKey(2027, 21, 3); a == b {
		t.Fatalf("period 21 collides across seasons: %q == %q", a, b)
	}
	if a, b := gsFloorMarkerKey(2026, 21, 3), gsFloorMarkerKey(2026, 22, 3); a == b {
		t.Fatalf("two periods in one season collide: %q == %q", a, b)
	}
}

// The key Period 22 needed and did not have. Two runs in one matchup week at
// different remaining-day counts must not share a marker, or the first one
// consumes the channel for the rest of the week — which is exactly what
// happened between 09-03 and 09-04.
func TestGSFloorMarkerKey_SeparatesRemainingDays(t *testing.T) {
	seen := map[string]int{}
	for _, daysLeft := range []int{2, 3} {
		seen[gsFloorMarkerKey(2026, 22, daysLeft)]++
	}
	if len(seen) != 2 {
		t.Fatalf("the two in-window days share %d keys, want 2 distinct: %v", len(seen), seen)
	}

	// Same day, same key — the other half of the contract. Without this, the
	// fix for permanent silence becomes a page every hour.
	if a, b := gsFloorMarkerKey(2026, 22, 3), gsFloorMarkerKey(2026, 22, 3); a != b {
		t.Fatalf("the same remaining day produced two keys: %q != %q", a, b)
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
		fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))))

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
		Today:   gsFloorToday.AddDate(0, 0, -3), // Mon 2026-08-24
		WeekEnd: gsFloorToday.AddDate(0, 0, 3),  // Sun 2026-08-30
		Forecast: []optimizer.DayForecast{
			{Date: gsFloorToday.AddDate(0, 0, -2), ConfirmedStarters: []float64{11.0}, Estimated: 0.4},
			{Date: gsFloorToday.AddDate(0, 0, -1), ConfirmedStarters: []float64{11.0}, Estimated: 0.4},
			{Date: gsFloorToday, ConfirmedStarters: []float64{11.0}, Estimated: 0.4},
			{Date: gsFloorToday.AddDate(0, 0, 1), Estimated: 2.0},
			{Date: gsFloorToday.AddDate(0, 0, 2), Estimated: 2.2},
			{Date: gsFloorToday.AddDate(0, 0, 3), Estimated: 2.0},
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
		{3, true, "exactly gsFloorMaxDaysLeft — the first day the projection is informative"},
		{4, false, "above gsFloorMaxDaysLeft: the replay buys 6 more alerts here and 1 more true positive"},
		{5, false, "above gsFloorMaxDaysLeft: 12 more alerts, 3 more true positives, worse precision"},
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
	// The real Period 21 forecast for its last three days: confirmed 3,
	// estimated 4.0.
	realThursday := func(used int) *optimizer.GSBudget {
		return &optimizer.GSBudget{
			Limit: 12, Floor: 10, Used: used,
			Today: gsFloorToday, WeekEnd: gsDay(3),
			Forecast: []optimizer.DayForecast{
				{Date: gsDay(1), ConfirmedStarters: []float64{11}, Estimated: 1.2},
				{Date: gsDay(2), Estimated: 1.2},
				{Date: gsDay(3), ConfirmedStarters: []float64{11, 10}, Estimated: 1.6},
			},
		}
	}

	// The real week: seven banked by Thursday. Comfortable, stays quiet.
	if f := evaluateGSFloor(realThursday(7)); f.Fires {
		t.Errorf("fired on the real Period 21 Thursday (need %d, supply %.2f)", f.Need, f.Supply)
	}
	// The counterfactual: same days and same rotation, two banked instead of
	// seven. That week really is heading under and must be announced.
	if f := evaluateGSFloor(realThursday(2)); !f.Fires {
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
//
// The forecast the run recorded came from the FLAT estimator (0.8, 1.8, 2.0,
// 2.0). The shipped estimator produces 74.5% of that, measured over the same
// 150-week replay, so the fixture carries the converted figures — the shape of
// the recorded week in the units the trigger now reads.
func TestEvaluateGSFloor_TodaysUnsettledStartsAreCreditedAgainstNeed(t *testing.T) {
	b := gsFloorBudget(10, 12, 2, period22Days()...)
	b.TodayUnsettled = 2

	f := evaluateGSFloor(b)

	if f.Need != 6 {
		t.Errorf("Need = %d, want 6 (floor 10 - used 2 - today's 2 confirmed)", f.Need)
	}
	if got := f.Shortfall; got < 1.04 || got > 1.12 {
		t.Errorf("Shortfall = %.2f, want ~1.08 — production reported 2.7 by dropping today entirely", got)
	}
	// It does NOT fire, and that is the day window rather than the arithmetic:
	// this run was the Wednesday, four days out, and gsFloorMaxDaysLeft is 3.
	// The same week alerts on the Thursday instead, which is what the sweep
	// says is the first informative day.
	if f.Fires {
		t.Errorf("fired %d days out; gsFloorMaxDaysLeft is %d", f.DaysLeft, gsFloorMaxDaysLeft)
	}
	inWindow := gsFloorBudget(10, 12, 2, period22Days()[1:]...)
	inWindow.TodayUnsettled = 2
	if g := evaluateGSFloor(inWindow); !g.Fires {
		t.Errorf("the same short week one day later stayed quiet (need %d, supply %.2f, %d days left)",
			g.Need, g.Supply, g.DaysLeft)
	}
}

// period22Days is the forecast the 2026-09-02T14:01Z run recorded, converted
// from the flat estimator that produced it into the units the shipped one
// reports (x0.745, the measured ratio over 900 in-week decision points).
func period22Days() []optimizer.DayForecast {
	return []optimizer.DayForecast{
		fc(1, nil, 0.60),
		fc(2, nil, 1.34),
		fc(3, nil, 1.49),
		fc(4, nil, 1.49),
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
	days := period22Days()

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
	b := gsFloorBudget(10, 12, 9, fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 0))
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
// shortfall it quotes: Period 22's real numbers would read "2 used, 4.9 more
// expected ... about 1.1 short" and invite the reader to compute 3.1.
func TestGSFloorMessage_AccountsForTodaysStartsInItsArithmetic(t *testing.T) {
	b := gsFloorBudget(10, 12, 2, period22Days()...)
	b.TodayUnsettled = 2

	msg := gsFloorMessage(evaluateGSFloor(b))

	if !strings.Contains(msg, "2 starting today") {
		t.Errorf("message hides the term that makes it add up (used 2 + today 2 + supply 4.9 vs floor 10):\n%s", msg)
	}
	if !strings.Contains(msg, "1.1 short") {
		t.Errorf("message should quote the corrected shortfall:\n%s", msg)
	}
}

// A week with nothing starting today must read exactly as it did before the
// correction existed — no dangling "0 starting today" clause.
func TestGSFloorMessage_OmitsTodayWhenThereIsNothingToSay(t *testing.T) {
	msg := gsFloorMessage(evaluateGSFloor(gsFloorBudget(10, 12, 4,
		fc(1, nil, 0), fc(2, nil, 0), fc(3, nil, 1.0))))

	if strings.Contains(msg, "starting today") {
		t.Errorf("no starts today, so the clause must not appear:\n%s", msg)
	}
}

// The coverage line is what the daily audit greps to reconstruct a week, and a
// Need that silently nets off today reads as a week that simply used more.
// Show the correction so the line stays reconcilable against Used and Floor.
func TestReportGSFloor_CoverageLineShowsTodaysCredit(t *testing.T) {
	h := newGSFloorHarness()
	b := gsFloorBudget(10, 12, 2, period22Days()...)
	b.TodayUnsettled = 2

	h.run(b, false)

	got := h.out.String()
	if !strings.Contains(got, "6 needed after 2 confirmed today") {
		t.Errorf("coverage line does not show why Need fell from 8 to 6:\n%s", got)
	}
}

// --- the swept constants ----------------------------------------------------

// The two constants the rosterbot-goht follow-up re-derived, pinned as
// fixtures so a revert fails here rather than quietly restoring a trigger that
// fires on more than half of all weeks.
//
// The bead's two designated weeks CANNOT be replayed from the frozen cache and
// were not: the MLB schedule on disk stops at 2026-08-16 and the per-day
// pitcher-GS snapshots at daily period 152 (2026-08-23), so Period 20 has
// rosters but no schedule and Period 21 has neither. What survives of them is
// the audited arithmetic recorded in gs_floor.go, and that is what the first
// two cases below encode. The constants themselves are chosen by the 150-week
// replay in TestDiagGSFloorSweep; the last two cases encode what that replay
// found at each edge.
func TestEvaluateGSFloor_PinsTheSweptFloorConstants(t *testing.T) {
	// A healthy week whose own rotation-math expectation covers its need —
	// Period 21's shape, which finished roughly three starts clear. Discounting
	// the estimate a second time makes it look short. Fails at credit 0.8.
	healthy := gsFloorBudget(10, 12, 5,
		fc(1, []float64{12.0}, 1.4),
		fc(2, nil, 1.4),
		fc(3, nil, 1.4),
	)
	if f := evaluateGSFloor(healthy); f.Fires {
		t.Errorf("fired on a week whose expectation covers its need (need %d, supply %.2f) — "+
			"the estimator already discounts, and crediting at %.1f discounts it twice",
			f.Need, f.Supply, gsFloorEstimateCredit)
	}

	// Period 20's recorded shape: it finished exactly ON its floor, 10/10, and
	// the audit put the mid-week margin near +0.2. A week that close must still
	// be announced, so the credit cannot go above 1.0.
	onTheFloor := gsFloorBudget(10, 12, 6,
		fc(1, []float64{11.0}, 0.4),
		fc(2, nil, 1.2),
		fc(3, nil, 1.2),
	)
	if f := evaluateGSFloor(onTheFloor); !f.Fires {
		t.Errorf("stayed quiet on a week finishing on its floor (need %d, supply %.2f, short %.2f)",
			f.Need, f.Supply, f.Shortfall)
	}

	// The day bound, from both sides. A genuinely short week must speak at
	// three days left and must NOT at four: on the corrected 150-week replay
	// max 3 carries the best precision of every setting that reaches the flat
	// baseline's recall, and the fourth day buys six alerts and one TP.
	shortAt := func(days int) *optimizer.GSBudget {
		fcs := make([]optimizer.DayForecast, 0, days)
		for i := 1; i <= days; i++ {
			fcs = append(fcs, optimizer.DayForecast{Date: gsDay(i), Estimated: 0.1})
		}
		return &optimizer.GSBudget{
			Limit: 12, Floor: 10, Used: 4,
			Today: gsFloorToday, WeekEnd: gsDay(days), Forecast: fcs,
		}
	}
	if f := evaluateGSFloor(shortAt(3)); !f.Fires {
		t.Errorf("stayed quiet three days out on a week needing %d more with %.2f coming", f.Need, f.Supply)
	}
	if f := evaluateGSFloor(shortAt(4)); f.Fires {
		t.Error("fired four days out; the corrected replay puts the best precision at max 3, and Period 21's Tuesday sat inside firing noise on a healthy week")
	}
}

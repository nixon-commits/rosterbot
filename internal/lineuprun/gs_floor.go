package lineuprun

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/alertmarker"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/optimizer"
)

// gsFloorEps guards the float comparisons below, matching the optimizer gate's
// own epsilon so the two agree about what "zero expected starts" means.
const gsFloorEps = 1e-9

// gsFloorEstimateCredit is the share of the rotation-math estimate this trigger
// counts as supply against the weekly GS minimum.
//
// It is the one knob that decides whether the alert is useful or muted, and it
// is an UNMEASURED PRIOR — the same standing as applyGSGate's certaintyMargin,
// and it must not be read as a fitted value.
//
// What bounds it is a two-sided constraint from the only two periods with
// recorded shape (audit 2026-08-26). Period 21 finished comfortably clear of
// its minimum — margin roughly +3 starts at mid-week — and must NOT fire, which
// puts a floor under the credit: too heavy a discount shrinks supply until a
// healthy week looks short, and an alert that fires on a healthy week gets
// muted, which costs more than the gap it was filling. Period 20 finished
// exactly ON its floor (10/10) with a margin near +0.2 at the comparable point
// and MUST fire, which puts a ceiling on it: crediting the estimate in full
// leaves a week that finishes on the floor indistinguishable from one that
// clears it. Those two bracket the credit into roughly [0.6, 0.95]; 0.8 sits in
// the middle rather than on either edge.
//
// Confirmed starts are deliberately NOT discounted. A club that has named one
// of our pitchers is as certain as today is, and the whole reason this trigger
// can wait is that the estimate collapses into confirmations as the week runs
// down — discounting the confirmations too would flatten exactly the signal the
// alert reads.
//
// The sweep this wants is rosterbot-xsm's shape but on the floor side, and it
// cannot be run yet: gs_floor and gs_forecast only started reaching the
// projection snapshots in Period 21, so the first gradeable week is 2026-08-31.
// Until then this is a prior, not a result. Filed as rosterbot-1tia.
const gsFloorEstimateCredit = 0.8

// gsFloorMinDaysLeft is the fewest remaining days on which the alert will still
// fire. Below it the alert is not merely noisy but useless: the only lever the
// bead identifies is a roster action — claiming a starter whose turn falls on a
// day our staff leaves empty — and a claim needs to clear waivers and then have
// the pitcher actually take the ball. An alert on the final evening names a
// problem that can no longer be acted on, which is how a channel gets muted.
const gsFloorMinDaysLeft = 2

// gsFloorMaxDaysLeft is the MOST remaining days on which the alert will fire,
// and it exists because the trigger was measured firing on a healthy week.
//
// Supply is counted from TOMORROW onward, never from today: today's starts land
// in GSBudget.Used the moment Fantrax settles them, and nothing at this seam
// separates a settled start from a pending one — the ambiguity that made the
// old live-roster count diverge every evening (rosterbot-cg8l). That exclusion
// is right, but it leaves the projection missing exactly today's starts, and
// that omission is WORST at the start of a week, where Need is simultaneously
// at its maximum because Used is still zero.
//
// Measured against the real Period 21 snapshots (.backtest/.../2026-08-24..27,
// the first week to carry gs_floor/gs_forecast). Monday's own forecast reads
// confirmed 3 + estimated 7.4, so Supply is 8.92 against a Need of the full
// floor, 10 — a 1.08 shortfall on a week that finished roughly +3 CLEAR, which
// is the bead's designated must-not-fire case. The Lineup cron runs hourly
// 14:00-03:00 UTC, so Used stays 0 through every daytime run before that
// evening's games settle: the false alert was not an edge case but a near
// certainty every Monday, and per-period dedup means it would spend the one
// alert that week was ever going to get.
//
// The projection itself is NOT wrong — Used + undiscounted supply reads 13.40
// on both Monday and Tuesday, matching the audit's own ~13.4 figure exactly.
// Only its split into "banked" and "still to come" is uninformative this early.
// So the guard refuses to evaluate rather than reweighting anything, the same
// choice periodIsVolatile makes for a scoring period that is still settling.
//
// 4 rather than 5 because Tuesday is not safe: at DaysLeft 5 the same real week
// reads -0.12, inside the noise of firing. DaysLeft 4 reads -2.36 and Thursday
// -2.60. Firing from Wednesday still leaves four days to claim a starter, which
// is what gsFloorMinDaysLeft says the alert is for.
//
// The cost is real and worth stating: a week that is already doomed on Monday
// is not announced until Wednesday. That is the deliberate trade — a muted
// channel is worth less than the gap it was filling, and a Monday alert that is
// wrong most weeks mutes the channel.
const gsFloorMaxDaysLeft = 4

// gsFloorFinding is the decision the trigger reached, separated from sending it
// so the whole rule is testable as a pure function over a *GSBudget — no
// marker store, no notifier, no clock.
type gsFloorFinding struct {
	// Fires is the verdict. Everything below is populated whether or not it
	// fired, so the unconditional coverage line can describe a quiet week.
	Fires bool

	Need      int     // starts still required to reach the floor
	Supply    float64 // credited starts from tomorrow through week end
	Shortfall float64 // Need - Supply; positive is the alerting direction
	DaysLeft  int
	// TodayUnsettled is echoed from the budget so the coverage line can show
	// its working: a Need that already nets off today's confirmed starts is
	// otherwise indistinguishable from a week that simply used more.
	TodayUnsettled int

	// EmptyDays are days on which no rostered SP has a scheduled turn at all:
	// nobody of ours confirmed, and no club of ours playing that has yet to
	// name a starter. This is the actionable half of the message.
	EmptyDays []time.Time
	// ThinDays carry a fractional estimate and no confirmation — a turn may
	// come from them, but less than one start is expected. Reported only when
	// there is no fully empty day, so the message is never vacuous.
	ThinDays []time.Time

	Floor, Used, Limit int
	WeekEnd            time.Time
}

// evaluateGSFloor decides whether this matchup week is projected to finish
// under the league's weekly game-start minimum while there is still time to act.
//
// The window is bounded at BOTH ends, and neither bound is decoration.
// gsFloorMaxDaysLeft keeps the trigger out of the early week, where excluding
// today's starts meets a maximal Need and made this fire on a healthy week;
// gsFloorMinDaysLeft keeps it out of the last days, where nothing can still be
// claimed. Inside that window the rule needs no separate urgency coefficient:
// as MLB announces probables the rotation-math estimate collapses into
// confirmations, and a day on which none of our arms was named stops
// contributing at all, so the projection sharpens on its own as the week runs
// down.
//
// An earlier version of this comment claimed the early week was self-protecting
// because "six days of unannounced clubs comfortably covers any ordinary
// shortfall". Real Period 21 data disproved that — six days covered 8.92
// against a Need of 10 — which is why the upper bound exists at all.
//
// That same collapse is what makes the empty days NAMEABLE, and the two are the
// same fact seen twice. A day cannot be known empty in advance: on both
// zero-start days of period 20 our clubs played and other pitchers took the
// ball, which is invisible until those clubs name someone. The alert therefore
// becomes possible at exactly the moment it becomes specific.
func evaluateGSFloor(b *optimizer.GSBudget) gsFloorFinding {
	var f gsFloorFinding
	if b == nil {
		return f
	}
	f.Floor, f.Used, f.Limit, f.WeekEnd = b.Floor, b.Used, b.Limit, b.WeekEnd

	// Credit today's confirmed-but-unsettled starts against the need.
	//
	// NeedForFloor is Floor - Used, and Used trails reality for most of the
	// day: Fantrax settles a day's YTD GS deltas hours after its games. Supply
	// meanwhile starts strictly TOMORROW. So today's confirmed starters — the
	// best-known quantity in the whole forecast, already named by MLB — were
	// counted in neither term, and the reported shortfall spiked at every day
	// roll and decayed every evening while nothing about the roster changed.
	// Measured on Period 22: 03:01Z read +1.2 and 11:30Z read +2.7, and the
	// once-per-week dedup spent the week's only alert at the top of that
	// sawtooth (rosterbot-ogtq).
	//
	// Corrected here rather than inside NeedForFloor deliberately. The gate's
	// floor PROTECTION reads the same method (applyGSGate), and protection is a
	// lineup decision: crediting a start there would let the gate stop
	// protecting an arm on the strength of a start that has not happened yet.
	// This alert only reports, so it is the safe place to be more accurate.
	f.TodayUnsettled = b.TodayUnsettled
	f.Need = b.NeedForFloor() - b.TodayUnsettled
	if f.Need < 0 {
		f.Need = 0
	}

	for _, d := range b.Forecast {
		// Strictly after today, matching FutureDemand. Today is excluded on
		// purpose; see gsFloorMinDaysLeft for why, and for what it costs.
		if !d.Date.After(b.Today) {
			continue
		}
		f.DaysLeft++
		f.Supply += float64(len(d.ConfirmedStarters)) + gsFloorEstimateCredit*d.Estimated

		if len(d.ConfirmedStarters) > 0 {
			continue
		}
		switch {
		case d.Estimated < gsFloorEps:
			f.EmptyDays = append(f.EmptyDays, d.Date)
		case d.Estimated < 1:
			f.ThinDays = append(f.ThinDays, d.Date)
		}
	}

	f.Shortfall = float64(f.Need) - f.Supply
	// A zero Need is a met floor, not a near miss: NeedForFloor already returns
	// 0 both when no minimum is configured and when the week has already
	// reached it, and neither is worth a word.
	f.Fires = f.Need > 0 &&
		f.DaysLeft >= gsFloorMinDaysLeft && f.DaysLeft <= gsFloorMaxDaysLeft &&
		f.Shortfall > gsFloorEps
	return f
}

type gsFloorInputs struct {
	Budget *optimizer.GSBudget
	// Period and Season together identify the matchup week for the marker key.
	// The season is load-bearing, not decoration: weekly period numbers restart
	// each year, so a season-less key would let period 21 of next season find
	// this season's marker and stay silent for the whole week (rosterbot-qoa
	// made the same correction to the past-period cache keys).
	Period fantrax.WeeklyPeriod
	Season int

	// Markers is the dedup seam, identical in shape and policy to
	// ilStartInputs.Markers: the check->send->mark discipline owned by
	// internal/alertmarker. lineupapi.BlobStore satisfies it structurally, and
	// a nil value disables dedup rather than the alert. In-process state
	// cannot serve here — every scheduled run is a fresh container, so without
	// a durable marker each hourly run would re-announce the same standing
	// shortfall, which is the flood the stale-cache marker exists to stop one
	// artifact over.
	Markers alertmarker.Store
	// Notify returns an error so a failed send is left unmarked, the same
	// contract reportILStarts needs: check -> send -> mark is only sound when
	// "sent" is actually known.
	Notify func(message string) error
	DryRun bool
	Out    io.Writer
}

// reportGSFloor raises one alert per matchup week when that week is projected
// to finish under the league's weekly game-start minimum, naming the days on
// which no rostered starter has a turn.
//
// It is the counterpart to gs-check's ViolationMin, which is correct but
// necessarily retrospective: it keys off FindJustEndedPeriod, so it speaks the
// day after the period closed, when the penalty is already taken. Protection
// inside the gate (rosterbot-dpm) can stop the ceiling side making a short week
// worse, but it cannot create a start — MLB names the probables, and on both
// zero-start days of period 20 our clubs played and other pitchers started. The
// only lever that closes such a day is a roster action, and a roster action
// needs lead time, which is the entire reason this fires while the week is open.
//
// Every failure here is soft, on the same reasoning as reportILStarts: this
// runs inside the hourly lineup job and neither an unreachable marker store nor
// a misfiring notifier may take that job down.
func reportGSFloor(in gsFloorInputs) {
	// A nil budget means the gate is disabled for this run, and ComputeGSBudget
	// has already logged the reason with a WARNING. Saying anything here would
	// restate it; saying it in the coverage line's voice would imply a floor
	// reading that was never taken.
	if in.Budget == nil {
		return
	}

	f := evaluateGSFloor(in.Budget)

	// An absent minimum renders as "none configured", never as "floor 0" — a
	// floor of zero and no floor at all are different facts, and only one of
	// them is a league setting (the rule FormatGateSummary already follows).
	if f.Floor == 0 {
		fmt.Fprintf(in.Out, "  gs floor check: no GS minimum configured for period %d\n", in.Period)
		return
	}

	// Printed unconditionally, including the everything-is-fine case, on the
	// same reasoning as the "il-start check:" and "mlb recency coverage:"
	// lines. This check's normal output is silence, so without the inputs a
	// comfortable week and a structurally blind trigger read identically — and
	// a forecast that silently reported zero all season is precisely the
	// failure this repo has already had once (rosterbot-1jvj).
	//
	// Need is reported AFTER today's credit, so on a day that has starts the
	// line must say so — otherwise it cannot be reconciled against Used and
	// Floor, and the daily audit greps exactly this line to rebuild a week.
	need := fmt.Sprintf("%d needed", f.Need)
	if f.TodayUnsettled > 0 {
		need = fmt.Sprintf("%d needed after %d confirmed today", f.Need, f.TodayUnsettled)
	}
	fmt.Fprintf(in.Out,
		"  gs floor check: %d/%d used, floor %d (%s), %.1f credited over %d remaining day(s), %d empty\n",
		f.Used, f.Limit, f.Floor, need, f.Supply, f.DaysLeft, len(f.EmptyDays))

	if !f.Fires {
		return
	}

	msg := gsFloorMessage(f)
	// No glyph prefix here: the message carries its own, and it is the one
	// ClassifyStatus reads (see gsFloorMessage).
	fmt.Fprintf(in.Out, "\n=== GS Floor Risk ===\n  %s\n\n", msg)

	if in.DryRun {
		// Neither send nor mark. Marking on a dry run would mute the real
		// alert that the next live run would otherwise raise.
		return
	}

	key := gsFloorMarkerKey(in.Season, in.Period)
	ctx := context.Background()
	m := alertmarker.New(in.Markers, alertmarker.WithLogf(func(format string, args ...any) {
		fmt.Fprintf(in.Out, "  gs floor check: "+format+"\n", args...)
	}))

	// check -> send -> mark, never claim-then-send (rosterbot-chs). A
	// marker-read failure is logged and treated as unsent (send anyway); a
	// marker-write failure degrades to a duplicate alert next hour, never to
	// silence. A failed send returns its error with nothing marked, so the
	// next run retries.
	if _, err := m.SendOnce(ctx, key, []byte(msg), func() error { return in.Notify(msg) }); err != nil {
		fmt.Fprintf(in.Out, "  gs floor check: send failed for %s: %v\n", key, err)
	}
}

// gsFloorMarkerKey keys on (season, weekly period) — one alert per matchup
// week. The tenant is NOT in the key: the artifact is PerTenant, so the
// user=<uid>/ segment is already applied by layout.PrefixFor, exactly as it is
// for the IL-start markers.
func gsFloorMarkerKey(season int, period fantrax.WeeklyPeriod) string {
	return fmt.Sprintf("%d-p%d", season, period)
}

// gsFloorMessage renders the finding. The empty days are the point of it: a
// count tells the manager the week is short, a date tells them which turn to go
// buy, and only the second is something they can act on.
//
// The leading ⚠️ is load-bearing and not decoration. lineupapi.ClassifyStatus
// badges the activity-feed entry by scanning the message for "⚠" BEFORE it
// looks at the Kind, and Kind "lineup" otherwise classifies as "success" — so
// swapping this for a different glyph (the IL-start alert's 🚨, say) would
// silently file a week that is heading under the floor as a success. Of the
// three available badges, none of which is "warning", the attention-drawing one
// is the only honest choice. Pinned by
// TestGSFloorMessage_IsNotBadgedAsASuccess.
func gsFloorMessage(f gsFloorFinding) string {
	var b strings.Builder
	// Today's confirmed starts are named explicitly rather than folded into
	// Used. They are a THIRD term — Used is what Fantrax has settled, Supply is
	// tomorrow onward, and these sit between the two — so quietly adding them
	// to either would misdescribe the week. Stating all three is also what lets
	// the reader check the shortfall: used + today + supply against the floor.
	today := ""
	if f.TodayUnsettled > 0 {
		today = fmt.Sprintf("%d starting today, ", f.TodayUnsettled)
	}
	fmt.Fprintf(&b, "⚠️ GS floor risk: this week projects to finish under the %d-start minimum — %d used, %s%.1f more expected over %d remaining day(s), leaving it about %.1f short.",
		f.Floor, f.Used, today, f.Supply, f.DaysLeft, f.Shortfall)

	switch {
	case len(f.EmptyDays) > 0:
		fmt.Fprintf(&b, " No rostered starter has a turn on %s — claiming a starter who pitches then is the move.",
			formatGSFloorDays(f.EmptyDays))
	case len(f.ThinDays) > 0:
		fmt.Fprintf(&b, " No day is fully empty; the gap is spread across %s, each expecting under one start.",
			formatGSFloorDays(f.ThinDays))
	default:
		// Every remaining day already expects at least one start, so the week
		// is short on volume rather than on a nameable hole. Saying so is
		// honest; inventing a day to blame would not be.
		b.WriteString(" Every remaining day already expects a start, so the shortfall is depth rather than a single open day.")
	}
	return b.String()
}

func formatGSFloorDays(days []time.Time) string {
	out := make([]string, 0, len(days))
	for _, d := range days {
		out = append(out, d.Format("Mon Jan 2"))
	}
	return strings.Join(out, ", ")
}

package lineuprun

import (
	"fmt"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"golang.org/x/sync/errgroup"
)

// InputsClient is the fetch surface the LoadInputs phase needs — the six
// roster/slot/scoring reads plus the two period lookups. *fantrax.Client
// satisfies it; an eight-method fake satisfies it in tests, which is what makes
// the failure policy below assertable without a Fantrax client (rosterbot-t89).
type InputsClient interface {
	GetHitterRoster() ([]fantrax.Player, error)
	GetActiveSlots() ([]fantrax.Slot, error)
	GetScoringWeights() (fantrax.ScoringWeights, error)
	GetPitcherRoster() ([]fantrax.Player, error)
	GetPitcherSlots() ([]fantrax.Slot, error)
	GetPitcherScoringWeights() (fantrax.ScoringWeights, error)
	GetCurrentPeriod() (fantrax.DailyPeriod, error)
	GetScoringPeriodsAndTeams() ([]fantrax.ScoringPeriod, map[string]string, map[string]string, error)
}

// inputsProgress is the slice of the progress display LoadInputs reports
// through. Done is part of it, not just Logf, because the "Roster" phase
// completion has to land between the six roster lines and the two period lines
// — owning the whole phase here is what keeps that ordering a property of the
// phase rather than of where Run happens to call it.
type inputsProgress interface {
	Start(phase string)
	Done(phase, detail string)
	Logf(format string, args ...any)
}

// Inputs is the typed result of the LoadInputs phase: everything downstream
// phases need from Fantrax that does not vary by date.
//
// The two period lookups carry their error rather than returning it, because
// they are not fatal — each one degrades a specific downstream behaviour and
// the run continues. Downstream consumes the error, not a bare zero value, so
// "no current period" and "period 0" stay distinguishable.
type Inputs struct {
	HitterRoster   []fantrax.Player
	HitterSlots    []fantrax.Slot
	HitterScoring  fantrax.ScoringWeights
	PitcherRoster  []fantrax.Player
	PitcherSlots   []fantrax.Slot
	PitcherScoring fantrax.ScoringWeights

	// CurrentPeriod is the daily scoring period Fantrax considers current.
	// PeriodErr non-nil means blending falls back to base projections only.
	CurrentPeriod fantrax.DailyPeriod
	PeriodErr     error

	// Periods is the weekly matchup "Scoring Period" list from the standings
	// SCHEDULE view — the axis GetGSLimits is keyed by. PeriodsErr non-nil
	// disables the GS-budget gate, which has no safe fallback for a budget
	// decision. NOT usable for date→period resolution in the apply loop; that
	// needs the daily axis (see DailyPeriodFor's doc comment).
	Periods    []fantrax.ScoringPeriod
	PeriodsErr error
}

// LoadInputs fetches everything a run needs from Fantrax that does not vary by
// date, and states the partial-failure policy once instead of eight times:
//
//   - the six roster/slot/scoring reads are FATAL. Every one is load-bearing —
//     without slots there is nothing to optimize into, without scoring weights
//     every projection is worth zero points — so a failure aborts the run
//     rather than silently producing a lineup from incomplete inputs.
//   - the two period lookups are SOFT. Each disables one downstream behaviour
//     (recency blending, the GS gate) that has a defined degraded mode, so
//     their errors ride along on Inputs and the run continues.
//
// Fatal errors are checked in the original sequential order, so the error a
// caller sees for any given combination of failures is the same one the
// pre-phase inline code produced.
//
// # Concurrency
//
// The eight reads run concurrently (rosterbot-t89 criterion 4). They are
// independent GETs that were issued sequentially only because that is the order
// someone wrote them in, and on a cold cache the roster and period reads are
// several hundred ms each. Three things make it safe:
//
//   - *fantrax.Client's auth_client embeds an http.Client and otherwise holds
//     read-only fields; the three in-memory memos it does keep (league info,
//     matchups, period→date map) are each mutex-guarded. The league-info guard
//     was added for exactly this change — four of these six fetches share that
//     memo, so before it a cold-cache concurrent run raced on the pointer.
//   - the file cache writes one file per key and these are eight distinct keys.
//   - every log line and the phase completion are emitted after the group
//     joins, in the original fixed order, so terminal output does not depend on
//     which response lands first.
//
// What is NOT included here is MLB handedness, despite being part of the same
// "fetch things that don't vary by date" group: it is keyed by the MLBAM IDs
// that come out of the FanGraphs projection load, so it cannot run until after
// that, and hoisting the projection load into this phase would widen it from a
// Fantrax phase into most of Run's prologue.
func LoadInputs(ft InputsClient, prog inputsProgress, projSystemName string) (Inputs, error) {
	prog.Start("Roster")

	var in Inputs
	var hitterRosterErr, hitterSlotsErr, hitterScoringErr error
	var pitcherRosterErr, pitcherSlotsErr, pitcherScoringErr error

	var g errgroup.Group
	g.Go(func() error {
		in.HitterRoster, hitterRosterErr = ft.GetHitterRoster()
		return nil
	})
	g.Go(func() error {
		in.HitterSlots, hitterSlotsErr = ft.GetActiveSlots()
		return nil
	})
	g.Go(func() error {
		in.HitterScoring, hitterScoringErr = ft.GetScoringWeights()
		return nil
	})
	g.Go(func() error {
		in.PitcherRoster, pitcherRosterErr = ft.GetPitcherRoster()
		return nil
	})
	g.Go(func() error {
		in.PitcherSlots, pitcherSlotsErr = ft.GetPitcherSlots()
		return nil
	})
	g.Go(func() error {
		in.PitcherScoring, pitcherScoringErr = ft.GetPitcherScoringWeights()
		return nil
	})
	g.Go(func() error {
		in.CurrentPeriod, in.PeriodErr = ft.GetCurrentPeriod()
		return nil
	})
	g.Go(func() error {
		in.Periods, _, _, in.PeriodsErr = ft.GetScoringPeriodsAndTeams()
		return nil
	})
	// No goroutine returns an error — each records its own so the precedence
	// below stays deterministic rather than "whichever failed first".
	_ = g.Wait()

	for _, f := range []struct {
		err  error
		what string
	}{
		{hitterRosterErr, "get hitter roster"},
		{hitterSlotsErr, "get hitter slots"},
		{hitterScoringErr, "get hitter scoring"},
		{pitcherRosterErr, "get pitcher roster"},
		{pitcherSlotsErr, "get pitcher slots"},
		{pitcherScoringErr, "get pitcher scoring"},
	} {
		if f.err != nil {
			return Inputs{}, fmt.Errorf("%s: %w", f.what, f.err)
		}
	}

	prog.Logf("hitter roster: %d hitters (%d active)", len(in.HitterRoster), countActive(in.HitterRoster))
	prog.Logf("hitter active slots: %d", len(in.HitterSlots))
	prog.Logf("hitter scoring weights: %d categories", len(in.HitterScoring))
	prog.Logf("pitcher roster: %d pitchers (%d active)", len(in.PitcherRoster), countActive(in.PitcherRoster))
	prog.Logf("pitcher active slots: %d", len(in.PitcherSlots))
	prog.Logf("pitcher scoring weights: %d categories", len(in.PitcherScoring))
	prog.Done("Roster", fmt.Sprintf("%d hitters (%d active) · %d pitchers (%d active)",
		len(in.HitterRoster), countActive(in.HitterRoster),
		len(in.PitcherRoster), countActive(in.PitcherRoster)))

	if in.PeriodErr != nil {
		prog.Logf("WARNING: could not get current period (%v) — using %s only", in.PeriodErr, projSystemName)
	} else {
		prog.Logf("current period: %d", in.CurrentPeriod)
	}
	if in.PeriodsErr != nil {
		prog.Logf("WARNING: could not fetch weekly scoring periods (%v) — GS-budget gate disabled", in.PeriodsErr)
	}

	return in, nil
}

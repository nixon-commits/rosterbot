package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/schedule"
	"github.com/spf13/cobra"
)

// seasonPolicy says whether a command has anything to do outside the fantasy
// season, which day the gate judges it on, and which flags name an explicit
// window that must run regardless (a historical --dates backfill in December
// is the whole point of --dates).
type seasonPolicy struct {
	InSeasonOnly  bool
	Ref           refDate
	ExplicitFlags []string
}

// refDate names the day the gate judges a command on: the day it LAUNCHES, or
// the day its work COVERS. Those differ for a retrospective job, and the gate
// has to ask about the second — a job that processes yesterday is doing
// in-season work on the first morning after the season ends.
type refDate int

const (
	// refToday is the zero value, so every policy that says nothing keeps the
	// original behavior: the command acts on today (a lineup to set, a waiver
	// list to push, a prospect board to build), and today is what the season
	// window must contain.
	refToday refDate = iota

	// refYesterday is for a command whose subject is yesterday. grade's window
	// ends today-1 (cmd/grade.go's resolveGradeWindow) and gs-check resolves
	// the period that ended yesterday (fantrax.FindJustEndedPeriod). Judging
	// those on today cost exactly one day per season, silently: the
	// 2026-09-28 grade recorded SUCCESS with outcome=off_season and
	// dt=2026-09-27 was never graded for any system (rosterbot-l53u), and the
	// same morning's gs-check exited in one second without ever evaluating
	// period 25 (rosterbot-97gs). Both bounds move together, which is why this
	// references a date rather than special-casing the season's end: on
	// opening day grade would grade the day BEFORE the opener, and is
	// correctly gated there.
	refYesterday
)

// gateReferenceDate is the day seasonGate measures against the window.
func gateReferenceDate(pol seasonPolicy, today time.Time) time.Time {
	switch pol.Ref {
	case refYesterday:
		return today.AddDate(0, 0, -1)
	case refToday:
		return today
	}
	// An unrecognized refDate reads as today, which is the conservative
	// direction: it gates a retrospective job one day early (a recoverable
	// gap, and the state this fix started from) rather than running a
	// today-referenced job one day into the off-season on dead data.
	return today
}

// seasonPolicies classifies every command the EventBridge schedule table
// launches (infra/infra.go) plus the manual recap. In-season-only commands
// read or write something that only exists while games are played — a
// lineup, a projection, a matchup, the waiver wire — and outside the season
// they either page (backtest, rosterbot-asy7), write junk rows (grade,
// rosterbot-zg1r) or push a dead list every morning (waivers). Year-round
// commands track things that keep moving through the winter: dynasty values
// and trades, the archive, the version pin, and football, which is in
// season when baseball is not.
//
// recap-site is year-round for a different reason than the rest of that list:
// not because its subject keeps moving, but because its work is finished
// weeks. It renders only COMPLETED matchup weeks, so its set only grows and
// the off-season is when it is largest. Decisively, it is a weekly MONDAY job
// against a season that ends on a SUNDAY, which makes the first off-season day
// the ONLY day it can ever render the final week and crown the champion —
// gating that day loses the championship page for the year, and loses it
// silently, since a gated run is SUCCESS, exit 0, outcome=off_season and pages
// nobody (measured 2026-09-28: the public 2026 site sat frozen on week 24
// reading "TBD" while the bracket had a champion). It belongs beside
// projection-site, the other retrospective renderer, not beside the
// winter-moving feeds (rosterbot-yqb9).
//
// backtest and recap carry no Ref because Ref answers a question they do not
// have: which SINGLE day does this run's work fall on. Their unit is a whole
// completed matchup week, so neither today nor yesterday is the right day to
// judge them by, and #216 accepted their Monday-after-the-final case
// deliberately (rosterbot-asy7).
//
// Escape flags are a SEPARATE axis — recoverability, not classification — and
// earlier versions of this comment conflated the two, claiming grade and
// gs-check had no flag and that a missed morning could not be rescheduled.
// Both halves were wrong: grade has always declared --dates, and gs-check
// gained --period in rosterbot-97gs precisely so a missed final-period morning
// is recoverable without ROSTERBOT_SEASON_GATE=off. Of the in-season-only
// commands only waivers and prospects declare no escape at all. Ref is not
// about whether a run can be redone by hand; it is what makes the SCHEDULED
// run land on the right day, so nobody has to notice it did not.
//
// An unclassified command is never gated (fail open); the test
// TestSeasonGate_EveryScheduledCommandIsClassified is what catches the
// omission, so the gate itself never has to guess.
var seasonPolicies = map[string]seasonPolicy{
	"optimize":  {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"backtest":  {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"grade":     {InSeasonOnly: true, Ref: refYesterday, ExplicitFlags: []string{"dates"}},
	"shadow":    {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"recap":     {InSeasonOnly: true, ExplicitFlags: []string{"dates", "week"}},
	"gs-check":  {InSeasonOnly: true, Ref: refYesterday, ExplicitFlags: []string{"period"}},
	"waivers":   {InSeasonOnly: true},
	"prospects": {InSeasonOnly: true},

	"version-check":   {},
	"transactions":    {},
	"claims":          {},
	"archive":         {},
	"team-values":     {},
	"projection-site": {},
	"recap-site":      {},
	"football-values": {},
	"football-trades": {},
}

// seasonGateEnv disables the gate when set to "off": the local smoke test
// (make run-all) and a developer asking an in-season question in December.
const seasonGateEnv = "ROSTERBOT_SEASON_GATE"

// seasonWindow is the fantasy season's inclusive calendar bounds and where
// they came from.
type seasonWindow struct {
	Start, End time.Time
	Source     string
}

// errOffSeason is the sentinel an off-season stop satisfies (errors.Is).
var errOffSeason = errors.New("off-season")

// offSeasonStop is the error initApp returns when the gate stops a run. It
// is an error only so the command returns through its ordinary path; Execute
// maps it to exit 0 with nothing on stderr (exitCodeFor).
type offSeasonStop struct{ line string }

func (e *offSeasonStop) Error() string        { return e.line }
func (e *offSeasonStop) Is(target error) bool { return target == errOffSeason }

// seasonGateApplies reports whether the gate has anything to say about this
// invocation before any window is fetched: an in-season-only command, no
// explicit window flag, no override.
func seasonGateApplies(cmdName string, changed func(string) bool, env string) bool {
	pol, ok := seasonPolicies[cmdName]
	if !ok || !pol.InSeasonOnly || env == "off" {
		return false
	}
	for _, f := range pol.ExplicitFlags {
		if changed(f) {
			return false
		}
	}
	return true
}

// seasonGate is the whole decision, pure: gated when the command is
// in-season-only, no explicit window was asked for, the override is not set,
// the window is known, and the command's REFERENCE DATE is strictly outside
// it. The reference date is today for a command that acts on today and
// yesterday for one whose subject is yesterday (see refDate) — the boundary
// question is "was the day this run covers in season", not "is the day it
// launched". Both bounds are inclusive: opening day and the final date are in
// season. An unknowable window fails OPEN — an unknown boundary is not an
// off-season one, and the worse error is a job skipped on a live day.
func seasonGate(cmdName string, changed func(string) bool, today time.Time, win seasonWindow, winErr error, env string) (bool, string) {
	if !seasonGateApplies(cmdName, changed, env) || winErr != nil || win.Start.IsZero() || win.End.IsZero() {
		return false, ""
	}
	ref := gateReferenceDate(seasonPolicies[cmdName], today)
	ymd := ref.Format("2006-01-02")
	if ymd >= win.Start.Format("2006-01-02") && ymd <= win.End.Format("2006-01-02") {
		return false, ""
	}
	// The line names the covered day whenever it differs from today, because
	// "today is 2026-09-29" alone reads as a contradiction to an operator who
	// watched the same job run inside the window the morning before.
	covers := ""
	if !ref.Equal(today) {
		covers = fmt.Sprintf(" (it covers %s)", ymd)
	}
	return true, fmt.Sprintf("Off-season: %s runs only from %s to %s (season bounds from %s); today is %s%s. Nothing to do.",
		cmdName, win.Start.Format("2006-01-02"), win.End.Format("2006-01-02"), win.Source,
		today.Format("2006-01-02"), covers)
}

// seasonWindowFor reads the fantasy season's bounds: Fantrax's own range
// (regular season plus the playoff bracket, cached 7 d) first, and MLB's
// regular season for the year as the credential-free fallback — every
// fantasy season sits inside it, so a day outside is off-season for any
// league. Both failing is the caller's fail-open case.
func seasonWindowFor(ctx context.Context, ft *fantrax.Client, today time.Time) (seasonWindow, error) {
	start, end, err := ft.GetSeasonDateRange()
	if err == nil && !start.IsZero() && !end.IsZero() {
		return seasonWindow{Start: start, End: end, Source: "fantrax"}, nil
	}
	sched := schedule.NewClient()
	if !noCache {
		sched.CacheDir = cacheDir
	}
	mstart, mend, merr := sched.SeasonDates(ctx, today.Year())
	if merr != nil {
		return seasonWindow{}, fmt.Errorf("fantrax season range: %w; mlb season: %w", err, merr)
	}
	return seasonWindow{Start: mstart, End: mend, Source: "mlb statsapi"}, nil
}

// checkSeasonGate is the gate as initApp runs it: decide, and on a stop
// print the one line, record the off_season outcome for the ledger, silence
// cobra's usage dump, and return the sentinel.
func checkSeasonGate(ctx context.Context, cmd *cobra.Command, ft *fantrax.Client, today time.Time) error {
	if cmd == nil {
		return nil
	}
	changed := func(f string) bool { return cmd.Flags().Changed(f) }
	env := os.Getenv(seasonGateEnv)
	if !seasonGateApplies(cmd.Name(), changed, env) {
		return nil
	}
	win, err := seasonWindowFor(ctx, ft, today)
	if err != nil {
		fmt.Fprintf(os.Stderr, "season gate: window unknown (%v) — running anyway\n", err)
	}
	gated, line := seasonGate(cmd.Name(), changed, today, win, err, env)
	if !gated {
		return nil
	}
	fmt.Println(line)
	recordRunOutcome(lineupapi.RunOutcomeOffSeason)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return &offSeasonStop{line: line}
}

// exitCodeFor maps a command's returned error to the process exit code: an
// off-season stop is a clean 0, anything else 1.
func exitCodeFor(err error) int {
	if err == nil || errors.Is(err, errOffSeason) {
		return 0
	}
	return 1
}

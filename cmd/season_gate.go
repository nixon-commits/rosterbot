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
// season, and which flags name an explicit window that must run regardless
// (a historical --dates backfill in December is the whole point of --dates).
type seasonPolicy struct {
	InSeasonOnly  bool
	ExplicitFlags []string
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
// An unclassified command is never gated (fail open); the test
// TestSeasonGate_EveryScheduledCommandIsClassified is what catches the
// omission, so the gate itself never has to guess.
var seasonPolicies = map[string]seasonPolicy{
	"optimize":   {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"backtest":   {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"grade":      {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"shadow":     {InSeasonOnly: true, ExplicitFlags: []string{"dates"}},
	"recap":      {InSeasonOnly: true, ExplicitFlags: []string{"dates", "week"}},
	"gs-check":   {InSeasonOnly: true},
	"waivers":    {InSeasonOnly: true},
	"recap-site": {InSeasonOnly: true},
	"prospects":  {InSeasonOnly: true},

	"version-check":   {},
	"transactions":    {},
	"claims":          {},
	"archive":         {},
	"team-values":     {},
	"projection-site": {},
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
// the window is known, and today is strictly outside it. Opening day and the
// final date are in season. An unknowable window fails OPEN — an unknown
// boundary is not an off-season one, and the worse error is a job skipped on
// a live day.
func seasonGate(cmdName string, changed func(string) bool, today time.Time, win seasonWindow, winErr error, env string) (bool, string) {
	if !seasonGateApplies(cmdName, changed, env) || winErr != nil || win.Start.IsZero() || win.End.IsZero() {
		return false, ""
	}
	ymd := today.Format("2006-01-02")
	if ymd >= win.Start.Format("2006-01-02") && ymd <= win.End.Format("2006-01-02") {
		return false, ""
	}
	return true, fmt.Sprintf("Off-season: %s runs only from %s to %s (season bounds from %s); today is %s. Nothing to do.",
		cmdName, win.Start.Format("2006-01-02"), win.End.Format("2006-01-02"), win.Source, ymd)
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

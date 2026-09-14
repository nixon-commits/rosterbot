package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/jobwire"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/spf13/cobra"
)

const (
	// Relative to the snapshot store root (statestore.SnapshotStore), not a
	// filesystem path: snapshots moved off the bulk dir sync in rosterbot-iqso.
	backtestSnapshotDir = "snapshots"

	// experimentSystem is the base projection every recency variant blends on
	// top of — the system the bot runs in production.
	experimentSystem = "depthcharts-ros"

	// experimentLookbackDays is how far before the grading window the recency
	// series reaches. The trailing windows need history predating the graded
	// days or they all collapse onto the same in-window games.
	experimentLookbackDays = 35
)

var (
	backtestDates             string
	backtestWeeks             int
	backtestMatchup           bool
	backtestSkipProjections   bool
	backtestJSON              bool
	backtestRecencyExperiment bool
)

var backtestCmd = &cobra.Command{
	Use:   "backtest",
	Short: "Grade past lineup moves and projections against actual results",
	RunE:  runBacktest,
}

func init() {
	backtestCmd.Flags().StringVar(&backtestDates, "dates", "", "date range YYYY-MM-DD:YYYY-MM-DD (overrides --weeks/--matchup)")
	backtestCmd.Flags().IntVar(&backtestWeeks, "weeks", 0, "backtest the last N completed matchup weeks")
	backtestCmd.Flags().BoolVar(&backtestMatchup, "matchup", false, "backtest the most recently completed matchup week")
	backtestCmd.Flags().BoolVar(&backtestSkipProjections, "skip-projections", false, "skip the projection-accuracy analysis (faster)")
	backtestCmd.Flags().BoolVar(&backtestJSON, "json", false, "emit machine-readable JSON instead of a human report")
	backtestCmd.Flags().BoolVar(&backtestRecencyExperiment, "recency-experiment", false, "compare YTD vs 14d/30d/decay recency strategies by lineup Gap (hitters + pitchers)")
	rootCmd.AddCommand(backtestCmd)
}

func runBacktest(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	seasonStart, seasonEnd, err := ft.GetSeasonDateRange()
	if err != nil {
		return fmt.Errorf("get season start: %w", err)
	}

	rangeOpts, err := backtestRangeOptions(today, seasonStart, seasonEnd)
	if err != nil {
		return err
	}
	// The weekly period list is what tells a playoff bye/elimination (a
	// clean stop) from a regular-season gap (a fault); soft — without it the
	// no-week case simply stays the error it always was.
	periods, _, _, perr := ft.GetScoringPeriodsAndTeams()
	if perr != nil {
		fmt.Fprintf(os.Stderr, "warning: scoring periods unavailable (%v); a missing matchup week will be reported as an error\n", perr)
	}
	start, end, notice, err := resolveBacktestWindow(ft, rangeOpts, periods)
	if err != nil {
		return err
	}
	if notice != "" {
		fmt.Printf("\n%s\n", notice)
		return nil
	}
	if end.Before(start) {
		return fmt.Errorf("empty backtest window (%s to %s)", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return fmt.Errorf("get hitter slots: %w", err)
	}
	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return fmt.Errorf("get pitcher slots: %w", err)
	}

	// Past periods are immutable — use a long TTL so repeat runs avoid the API.
	snapTTL := cacheTTL(fantrax.PastPeriodTTL)

	// Progress chatter goes to stderr, not stdout: under --json this function's
	// stdout is a document, and a human-readable line in front of it made the
	// output unparseable for exactly the consumers --json exists to serve
	// (rosterbot-5cx). stderr keeps the line visible in a terminal and in the
	// Fargate task's CloudWatch stream, which is where it is actually read.
	fmt.Fprintf(os.Stderr, "Fetching daily fantasy points for %s to %s...\n",
		start.Format("2006-01-02"), end.Format("2006-01-02"))
	// DailyFantasyPoints resolves the MLB-statsapi backfill internally (soft-fail).
	days, err := ft.DailyFantasyPoints(cfg.TeamID, start, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("daily fpts: %w", err)
	}

	if backtestRecencyExperiment {
		return runRecencyExperiment(cmd.Context(), ft, cfg, days, start, end, seasonStart, snapTTL, hitterSlots, pitcherSlots)
	}

	snapStore, err := statestore.FromEnv().SnapshotStore()
	if err != nil {
		return fmt.Errorf("snapshot store: %w", err)
	}

	lineup := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)

	// All three snapshot-derived sections are computed together and BEFORE the
	// --json early return, so they reach every consumer rather than only
	// stdout (rosterbot-5cx). They share one gate: each reads the projection
	// snapshot directory, which is exactly what --skip-projections means it
	// should not do. Two of them previously walked it anyway, making the flag
	// a claim the code did not honour.
	var proj []backtest.ProjectionDayResult
	var gate *backtest.GateSummary
	var shape *backtest.RosterShape
	if !backtestSkipProjections {
		proj = backtest.RunProjectionAnalysis(days, snapStore, backtestSnapshotDir)

		dates := make([]time.Time, len(days))
		for i, d := range days {
			dates[i] = d.Date
		}
		if g := backtest.SummarizeGSGate(snapStore, backtestSnapshotDir, dates); g.Days > 0 {
			gate = &g
		}
		if sh := backtest.SummarizeRosterShape(snapStore, backtestSnapshotDir, days,
			len(hitterSlots), len(pitcherSlots)); sh.Days > 0 {
			shape = &sh
		}
	}

	report := backtest.BuildReport(start, end, lineup, proj)
	report.Gate = gate
	report.Shape = shape

	jobwire.RecordOutput("backtest", backtestToWireResult(report))

	if backtestJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Println()
	fmt.Print(backtest.FormatReport(report))

	if report.Gate != nil {
		fmt.Print(backtest.FormatGateSummary(*report.Gate))
	}
	// Roster shape is the analytical companion to the gate summary: the gate
	// measures which starts the cap declined, this measures the structural
	// imbalance that keeps producing them. Printed after so the measurement
	// comes before its explanation.
	if report.Shape != nil {
		fmt.Print(backtest.FormatRosterShape(*report.Shape))
	}
	return nil
}

// backtestRangeOptions turns the CLI flags into a backtest.RangeOptions. Only
// --dates parsing lives here (it is CLI syntax); which matchup weeks a window
// covers is resolved by internal/backtest.
func backtestRangeOptions(today, seasonStart, seasonEnd time.Time) (backtest.RangeOptions, error) {
	opts := backtest.RangeOptions{
		Today:       today,
		SeasonStart: seasonStart,
		SeasonEnd:   seasonEnd,
		Weeks:       backtestWeeks,
	}
	if backtestDates != "" {
		dates, err := parseDates(backtestDates, today)
		if err != nil {
			return opts, fmt.Errorf("invalid --dates: %w", err)
		}
		if len(dates) == 0 {
			return opts, fmt.Errorf("--dates produced no dates")
		}
		opts.ExplicitStart = dates[0]
		opts.ExplicitEnd = dates[len(dates)-1]
	}
	return opts, nil
}

// resolveBacktestWindow resolves the window to grade. A non-empty notice means
// there is no window and the run should print it and exit 0.
//
// An out-of-season week-relative window is that clean stop, not a failure: the
// weekly Monday job passes no flags, and past the season's final date it exited
// 1 with a usage dump and paged every week until the next opener
// (rosterbot-asy7). The wording mirrors the optimize jobs' season lines.
//
// periods is the league's weekly period list; inside a playoff round a
// missing matchup week is a bye or an elimination and a clean stop, while in
// the regular season it stays a fault (rosterbot-0lyz).
func resolveBacktestWindow(wb fantrax.WeekBounder, opts backtest.RangeOptions, periods []fantrax.ScoringPeriod) (start, end time.Time, notice string, err error) {
	start, end, err = backtest.ResolveRange(wb, opts)
	var oos *backtest.OutOfSeasonError
	if errors.As(err, &oos) {
		if oos.BeforeOpener() {
			return time.Time{}, time.Time{}, fmt.Sprintf("Season starts %s. No completed matchup week to grade yet.",
				oos.Start.Format("2006-01-02")), nil
		}
		return time.Time{}, time.Time{}, fmt.Sprintf("Season ended %s. No completed matchup week to grade.",
			oos.End.Format("2006-01-02")), nil
	}
	if errors.Is(err, fantrax.ErrNoMatchupWeek) {
		yesterday := opts.Today.AddDate(0, 0, -1)
		if p := fantrax.FindCurrentPeriod(periods, yesterday); p != nil && p.Playoff {
			return time.Time{}, time.Time{}, fmt.Sprintf("No matchup week for this team ending %s (%s): bye or eliminated. Nothing to grade.",
				yesterday.Format("2006-01-02"), p.Caption), nil
		}
	}
	if err != nil {
		return time.Time{}, time.Time{}, "", fmt.Errorf("resolve range: %w", err)
	}
	return start, end, "", nil
}

// runRecencyExperiment fetches the extended recency series the trailing-window
// strategies need, then hands both it and the grading window to
// internal/backtest for the comparison.
func runRecencyExperiment(
	ctx context.Context,
	ft *fantrax.Client,
	cfg *config.Config,
	gradeDays []fantrax.DayRoster,
	start, end, seasonStart time.Time,
	snapTTL time.Duration,
	hitterSlots, pitcherSlots []fantrax.Slot,
) error {
	hitterScoring, err := ft.GetScoringWeights()
	if err != nil {
		return fmt.Errorf("get scoring weights: %w", err)
	}

	seriesStart := start.AddDate(0, 0, -experimentLookbackDays)
	if seriesStart.Before(seasonStart) {
		seriesStart = seasonStart
	}
	seriesDays, err := ft.DailyFantasyPoints(cfg.TeamID, seriesStart, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("recency series fetch: %w", err)
	}

	report, err := backtest.RunRecencyExperiment(ctx, ft, gradeDays, seriesDays, backtest.ExperimentOptions{
		ProjectionSystem: experimentSystem,
		CacheDir:         cacheDir,
		ProjectionTTL:    cacheTTL(projections.ProjectionCacheTTL),
		HitterSlots:      hitterSlots,
		PitcherSlots:     pitcherSlots,
		HitterScoring:    hitterScoring,
		BlendMinGP:       cfg.BlendMinGP,
	})
	if err != nil {
		return err
	}
	fmt.Print(backtest.FormatExperiment(report))
	return nil
}

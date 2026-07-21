package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/spf13/cobra"
)

const (
	backtestSnapshotDir = ".backtest/snapshots"

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

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return fmt.Errorf("get season start: %w", err)
	}

	rangeOpts, err := backtestRangeOptions(today, seasonStart)
	if err != nil {
		return err
	}
	start, end, err := backtest.ResolveRange(ft, rangeOpts)
	if err != nil {
		return fmt.Errorf("resolve range: %w", err)
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
	snapTTL := 30 * 24 * time.Hour
	if noCache {
		snapTTL = 0
	}

	fmt.Printf("Fetching daily fantasy points for %s to %s...\n",
		start.Format("2006-01-02"), end.Format("2006-01-02"))
	days, err := ft.DailyFantasyPoints(cfg.TeamID, start, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("daily fpts: %w", err)
	}
	if err := ft.BackfillDailyFPts(days); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: MLB backfill: %v\n", err)
	}

	if backtestRecencyExperiment {
		return runRecencyExperiment(ft, cfg, days, start, end, seasonStart, snapTTL, hitterSlots, pitcherSlots)
	}

	lineup := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)

	var proj []backtest.ProjectionDayResult
	if !backtestSkipProjections {
		proj = backtest.RunProjectionAnalysis(days, backtestSnapshotDir)
	}

	report := backtest.BuildReport(start, end, lineup, proj)

	lineupapi.RecordOutput("backtest", backtestToWireResult(report))

	if backtestJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Println()
	fmt.Print(backtest.FormatReport(report))
	return nil
}

// backtestRangeOptions turns the CLI flags into a backtest.RangeOptions. Only
// --dates parsing lives here (it is CLI syntax); which matchup weeks a window
// covers is resolved by internal/backtest.
func backtestRangeOptions(today, seasonStart time.Time) (backtest.RangeOptions, error) {
	opts := backtest.RangeOptions{
		Today:       today,
		SeasonStart: seasonStart,
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

// runRecencyExperiment fetches the extended recency series the trailing-window
// strategies need, then hands both it and the grading window to
// internal/backtest for the comparison.
func runRecencyExperiment(
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
	if err := ft.BackfillDailyFPts(seriesDays); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: recency series backfill: %v\n", err)
	}

	report, err := backtest.RunRecencyExperiment(ft, gradeDays, seriesDays, backtest.ExperimentOptions{
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

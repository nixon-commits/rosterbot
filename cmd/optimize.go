package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineuprun"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/spf13/cobra"
)

var (
	datesStr           string
	daysAhead          int
	checkRoster        bool
	matchupPeriod      bool
	projectionSystem   string
	showPipeline       bool
	archiveProjections bool
	snapshotFlag       bool
	publishLineupFlag  bool
)

var optimizeCmd = &cobra.Command{
	Use:   "optimize",
	Short: "Optimize daily lineup for hitters and pitchers",
	RunE:  runOptimize,
}

func init() {
	optimizeCmd.Flags().StringVar(&datesStr, "dates", "", "date(s) for schedule lookup: YYYY-MM-DD, YYYY-MM-DD:YYYY-MM-DD, or 'all' (default: today)")
	optimizeCmd.Flags().IntVar(&daysAhead, "days", 0, "optimize for the next N days starting from today")
	optimizeCmd.Flags().BoolVar(&matchupPeriod, "matchup", false, "optimize for all remaining days in the current matchup period")
	optimizeCmd.Flags().BoolVar(&checkRoster, "check-roster", true, "check for roster slot mismatches (IL/minors)")
	optimizeCmd.Flags().StringVar(&projectionSystem, "projections", "depthcharts", "projection system: steamer, depthcharts, thebatx, atc, steamer-ros, depthcharts-ros, thebatx-ros, atc-ros")
	optimizeCmd.Flags().BoolVar(&showPipeline, "pipeline", false, "show full hitter adjustment pipeline detail")
	optimizeCmd.Flags().BoolVar(&snapshotFlag, "snapshot", false, "force-write per-date projection snapshots to .backtest/snapshots/ even in --dry-run (non-dry-run runs always write)")
	optimizeCmd.Flags().BoolVar(&archiveProjections, "archive-projections", false, "deprecated alias for --snapshot (snapshots are written by default on non-dry-run runs; also enabled by BACKTEST_ARCHIVE=1)")
	optimizeCmd.Flags().BoolVar(&publishLineupFlag, "publish-lineup", false, "write today's read-only API lineup JSON (.lineup/ locally, or s3://$STATE_BUCKET/lineup/) even in --dry-run; non-dry-run runs always publish")
	rootCmd.AddCommand(optimizeCmd)
}

// runOptimize is the `optimize` cobra adapter: it validates and parses CLI
// flags into an explicit lineuprun.Options, wires up the Fantrax client via
// initApp, and hands off to lineuprun.Run for the actual orchestration. See
// cmd/shadow.go for the other adapter (one dry-run capture per projection
// system) that builds its own Options against the same Run entry point.
func runOptimize(cmd *cobra.Command, args []string) error {
	// Validated up front (fail fast) — Run also sets this itself, but doing
	// it here means a bad --projections value errors out before initApp does
	// any network/auth setup.
	if err := projections.SetProjectionSystem(projectionSystem); err != nil {
		return err
	}

	today := todayET()

	// Parse dates early for non-"all" cases; "all" and "matchup" need the Fantrax client.
	flagCount := 0
	if daysAhead > 0 {
		flagCount++
	}
	if datesStr != "" {
		flagCount++
	}
	if matchupPeriod {
		flagCount++
	}
	if flagCount > 1 {
		return fmt.Errorf("--days, --dates, and --matchup are mutually exclusive")
	}
	needsSeasonLookup := datesStr == "all"
	needsMatchupLookup := matchupPeriod
	var dates []time.Time
	if daysAhead > 0 {
		for i := 0; i < daysAhead; i++ {
			dates = append(dates, today.AddDate(0, 0, i))
		}
	} else if !needsSeasonLookup && !needsMatchupLookup {
		var err error
		dates, err = parseDates(datesStr, today)
		if err != nil {
			return fmt.Errorf("invalid --dates: %w", err)
		}
	}

	cfg, ft, err := initApp(dates)
	if err != nil {
		return err
	}

	pub, err := statestore.FromEnv().LineupPublisher()
	if err != nil {
		return fmt.Errorf("init lineup publisher: %w", err)
	}

	// Soft: a marker store we cannot build disables dedup, not the alert.
	// Repeating an IL-start push every hour is recoverable; dropping it is the
	// failure the alert exists to prevent.
	ilMarkers, err := statestore.FromEnv().ILStartMarkers()
	if err != nil {
		warn("optimize: init IL-start markers: %v (alert will repeat until resolved)", err)
		ilMarkers = nil
	}

	snapStore, err := statestore.FromEnv().SnapshotStore()
	if err != nil {
		return fmt.Errorf("snapshot store: %w", err)
	}

	opts := lineuprun.Options{
		Today:              today,
		NeedsSeasonLookup:  needsSeasonLookup,
		NeedsMatchupLookup: needsMatchupLookup,
		ProjectionSystem:   projectionSystem,
		CheckRoster:        checkRoster,
		ShowPipeline:       showPipeline,
		WriteSnapshots:     resolveWriteSnapshots(cfg.DryRun, snapshotFlag, archiveProjections, os.Getenv("BACKTEST_ARCHIVE")),
		SnapshotStore:      snapStore,
		SnapshotRoot:       backtestSnapshotDir,
		PublishLineupFlag:  publishLineupFlag,
		Publisher:          pub,
		ILStartMarkers:     ilMarkers,
		Out:                os.Stdout,
		NoCache:            noCache,
		Verbose:            verbose,
	}
	if _, err := lineuprun.Run(ft, cfg, opts); err != nil {
		return err
	}

	// The Trades tab's pending-offer snapshot. This job is the only schedule
	// that runs often enough for a trade offer to be actionable, and it
	// already holds an authenticated client with a warm session.
	//
	// Soft-fail, deliberately after Run and outside its error path: applying
	// the lineup is this command's job and a trades hiccup must not fail a run
	// that already wrote the lineup. Same rule as cmd/grade.go's lineup gaps.
	if err := captureTradeOffers(ft, cfg, time.Now()); err != nil {
		warn("optimize: pending trade offers not captured: %v", err)
	}
	return nil
}

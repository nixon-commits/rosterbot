package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/ndjsonstore/s3ndjson"
	"github.com/spf13/cobra"
)

var (
	gradeDates  string
	gradeWindow int
)

var gradeCmd = &cobra.Command{
	Use:   "grade",
	Short: "Grade past projections and append Graded Snapshots to the Analysis Store",
	RunE:  runGrade,
}

func init() {
	gradeCmd.Flags().StringVar(&gradeDates, "dates", "", "explicit date or range to grade (overrides --window)")
	gradeCmd.Flags().IntVar(&gradeWindow, "window", 3, "(re)grade this many trailing days ending yesterday; re-grades are idempotent per date, so a night that failed self-heals on the next run")
	rootCmd.AddCommand(gradeCmd)
}

func runGrade(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	// Default: a trailing window ending yesterday. Grading is idempotent per
	// date (each dt partition is overwritten), and missing/stale days are
	// skipped, so re-grading recent days every night lets a failed run
	// self-heal on the next one without manual --dates backfill.
	if gradeWindow < 1 {
		gradeWindow = 1
	}
	start, end := today.AddDate(0, 0, -gradeWindow), today.AddDate(0, 0, -1)
	if gradeDates != "" {
		ds, err := parseDates(gradeDates, today)
		if err != nil {
			return err
		}
		start, end = ds[0], ds[len(ds)-1]
	}

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return fmt.Errorf("get season start: %w", err)
	}

	snapTTL := 30 * 24 * time.Hour
	if noCache {
		snapTTL = 0
	}
	days, err := ft.DailyFantasyPoints(cfg.TeamID, start, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("daily fpts: %w", err)
	}
	if err := ft.BackfillDailyFPts(days); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: MLB backfill: %v\n", err)
	}

	// Grade every projection system the shadow command captured. Actuals
	// (days) are fetched once above and reused — only the projection side
	// differs per system. Each system's rows land in its own Hive partition
	// (grades/dt=X/system=Y/...). The depthcharts-ros slice keeps feeding the
	// existing detailed dashboard; the others power the comparison panel.
	bySystemDate := map[string]map[string][]analysis.GradeRow{}
	for _, sys := range shadowSystems {
		dir := systemSnapshotDir(shadowSnapshotRoot, sys)
		results := backtest.RunProjectionAnalysis(days, dir)
		byDate := map[string][]analysis.GradeRow{}
		for _, d := range results {
			if d.Source == "missing" || d.Source == "stale" || d.Source == "no-data" {
				// Forward-only: before the shadow command has captured a day,
				// its snapshot is absent and the day is skipped, not graded.
				// "no-data" means the system had a real outage that day (see
				// Snapshot.HittersNoData/PitchersNoData) — same treatment.
				continue
			}
			dt := d.Date.UTC().Format("2006-01-02")
			for _, p := range d.Players {
				byDate[dt] = append(byDate[dt], analysis.GradeRow{
					Dt:        dt,
					PlayerID:  p.PlayerID,
					Name:      p.Name,
					MLBTeam:   p.MLBTeam,
					Projected: p.Projected,
					Actual:    p.Actual,
					Diff:      p.Diff,
					Bucket:    p.Bucket,
					IsPitcher: p.IsPitcher,
					Source:    p.Source,
				})
			}
		}
		if len(byDate) > 0 {
			bySystemDate[sys] = byDate
		}
	}

	// Aggregate per-date row counts across all systems for the health signal.
	counts := map[string]int{}
	for _, byDate := range bySystemDate {
		for dt, rows := range byDate {
			counts[dt] += len(rows)
		}
	}
	lineupapi.RecordOutput("grade", gradeToWireResult(counts))

	if cfg.DryRun {
		for _, sys := range shadowSystems {
			for dt, rows := range bySystemDate[sys] {
				fmt.Printf("[dry-run] %s %s: %d graded rows\n", sys, dt, len(rows))
			}
		}
		return nil
	}

	var w analysis.Writer
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		store, err := s3ndjson.New(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis store: %w", err)
		}
		w = analysis.NewWriter(store)
	} else {
		w = analysis.NewFileWriter(".analysis")
	}

	for _, sys := range shadowSystems {
		for dt, rows := range bySystemDate[sys] {
			date, _ := time.Parse("2006-01-02", dt)
			if err := w.WriteGrades(date, sys, rows); err != nil {
				return fmt.Errorf("write grades %s %s: %w", sys, dt, err)
			}
			fmt.Printf("wrote %d graded rows for %s %s\n", len(rows), sys, dt)
		}
	}
	return nil
}

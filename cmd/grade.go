package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/analysis/s3grades"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/spf13/cobra"
)

var gradeDates string

var gradeCmd = &cobra.Command{
	Use:   "grade",
	Short: "Grade past projections and append Graded Snapshots to the Analysis Store",
	RunE:  runGrade,
}

func init() {
	gradeCmd.Flags().StringVar(&gradeDates, "dates", "", "date or range to grade (default: yesterday)")
	rootCmd.AddCommand(gradeCmd)
}

func runGrade(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	start, end := today.AddDate(0, 0, -1), today.AddDate(0, 0, -1)
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

	results := backtest.RunProjectionAnalysis(days, backtestSnapshotDir)

	byDate := map[string][]analysis.GradeRow{}
	for _, d := range results {
		if d.Source == "missing" || d.Source == "stale" {
			fmt.Fprintf(os.Stderr, "skip %s: source=%s\n", d.Date.Format("2006-01-02"), d.Source)
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

	counts := map[string]int{}
	for dt, rows := range byDate {
		counts[dt] = len(rows)
	}
	lineupapi.RecordOutput("grade", gradeToWireResult(counts))

	if cfg.DryRun {
		for dt, rows := range byDate {
			fmt.Printf("[dry-run] %s: %d graded rows\n", dt, len(rows))
		}
		return nil
	}

	var w analysis.Writer
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		sw, err := s3grades.New(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis store: %w", err)
		}
		w = sw
	} else {
		w = analysis.NewFileWriter(".analysis")
	}

	for dt, rows := range byDate {
		date, _ := time.Parse("2006-01-02", dt)
		if err := w.WriteGrades(date, rows); err != nil {
			return fmt.Errorf("write grades %s: %w", dt, err)
		}
		fmt.Printf("wrote %d graded rows for %s\n", len(rows), dt)
	}
	return nil
}

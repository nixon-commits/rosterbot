package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
	"github.com/nixon-commits/rosterbot/internal/statestore"
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
	// DailyFantasyPoints resolves the MLB-statsapi backfill internally (soft-fail),
	// so the returned rows never carry placeholder zeros.
	days, err := ft.DailyFantasyPoints(cfg.TeamID, start, end, seasonStart, cacheDir, snapTTL)
	if err != nil {
		return fmt.Errorf("daily fpts: %w", err)
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

	w, err := statestore.FromEnv().AnalysisWriter()
	if err != nil {
		return fmt.Errorf("init analysis store: %w", err)
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

	// The Lineup Gap Store: how the lineup we actually applied scored against
	// the hindsight-optimal one. Computed here because this command already
	// holds `days` and a live client — the read-side (projection-site) has
	// neither.
	//
	// Soft-fail: grades are irreplaceable, and a gap day is recoverable with
	// `grade --dates`, so a gap hiccup must never fail the run.
	if err := recordLineupGaps(ft, days); err != nil {
		fmt.Fprintf(os.Stderr, "warning: lineup gaps not written: %v\n", err)
	}
	return nil
}

// recordLineupGaps grades each day's applied lineup against hindsight and
// persists the result. Slot lists are stableTTL-cached (7d), so this costs one
// cold fetch per week on top of the actuals runGrade already holds.
func recordLineupGaps(ft *fantrax.Client, days []fantrax.DayRoster) error {
	hitterSlots, err := ft.GetActiveSlots()
	if err != nil {
		return fmt.Errorf("get hitter slots: %w", err)
	}
	pitcherSlots, err := ft.GetPitcherSlots()
	if err != nil {
		return fmt.Errorf("get pitcher slots: %w", err)
	}

	results := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)
	if len(results) == 0 {
		return nil
	}

	w, err := statestore.FromEnv().LineupGapWriter()
	if err != nil {
		return fmt.Errorf("init lineup gap store: %w", err)
	}
	return writeLineupGaps(w, results)
}

// writeLineupGaps maps lineup-grade results to store rows, one partition per
// date. Split from recordLineupGaps so the mapping is testable without a
// Fantrax client.
func writeLineupGaps(w lineupgap.Writer, results []backtest.LineupDayResult) error {
	for _, d := range results {
		dt := d.Date.UTC().Format("2006-01-02")
		row := lineupgap.Row{
			Dt:         dt,
			ActualPts:  d.ActualPts,
			OptimalPts: d.OptimalPts,
			Gap:        d.Gap,
			StartedN:   len(d.Started),
			BenchedN:   len(d.Benched),
		}
		if err := w.WriteGaps(d.Date, []lineupgap.Row{row}); err != nil {
			return fmt.Errorf("write lineup gap %s: %w", dt, err)
		}
		fmt.Printf("wrote lineup gap for %s (actual %.1f, optimal %.1f, gap %.1f)\n",
			dt, d.ActualPts, d.OptimalPts, d.Gap)
	}
	return nil
}

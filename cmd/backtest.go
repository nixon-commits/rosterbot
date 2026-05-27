package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/backtest"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/spf13/cobra"
)

const backtestSnapshotDir = ".backtest/snapshots"

var (
	backtestDates           string
	backtestWeeks           int
	backtestMatchup         bool
	backtestSkipProjections bool
	backtestJSON            bool
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
	rootCmd.AddCommand(backtestCmd)
}

func runBacktest(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	start, end, err := resolveBacktestRange(ft, today)
	if err != nil {
		return fmt.Errorf("resolve range: %w", err)
	}
	if end.Before(start) {
		return fmt.Errorf("empty backtest window (%s to %s)", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return fmt.Errorf("get season start: %w", err)
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

	lineup := backtest.RunLineupAnalysis(days, hitterSlots, pitcherSlots)

	var proj []backtest.ProjectionDayResult
	if !backtestSkipProjections {
		proj = backtest.RunProjectionAnalysis(days, backtestSnapshotDir)
	}

	report := backtest.BuildReport(start, end, lineup, proj)

	if backtestJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Println()
	fmt.Print(backtest.FormatReport(report))
	return nil
}

// resolveBacktestRange picks a date window based on flags. Priority:
// explicit --dates, then --weeks, then --matchup, then default (last completed
// matchup week).
func resolveBacktestRange(ft *fantrax.Client, today time.Time) (time.Time, time.Time, error) {
	yesterday := today.AddDate(0, 0, -1)

	if backtestDates != "" {
		dates, err := parseDates(backtestDates, today)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --dates: %w", err)
		}
		if len(dates) == 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("--dates produced no dates")
		}
		return dates[0], dates[len(dates)-1], nil
	}

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	if backtestWeeks > 0 {
		// Walk back `backtestWeeks` matchup-week boundaries from yesterday.
		curEnd := yesterday
		var start time.Time
		for i := 0; i < backtestWeeks; i++ {
			weekStart, weekEnd, err := ft.GetMatchupWeekBounds(curEnd, seasonStart)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			if weekStart.IsZero() {
				break
			}
			if i == 0 {
				// If curEnd sits inside a still-running week, clip to yesterday.
				if weekEnd.After(yesterday) {
					weekEnd = yesterday
				}
				curEnd = weekEnd
			}
			start = weekStart
			// Step back one day before that week's start.
			curEnd = weekStart.AddDate(0, 0, -1)
		}
		if start.IsZero() {
			return time.Time{}, time.Time{}, fmt.Errorf("could not resolve %d matchup week(s)", backtestWeeks)
		}
		end := yesterday
		return start, end, nil
	}

	// Default (and --matchup): last completed matchup week up through yesterday.
	// Try yesterday first — if it was the final day of a matchup, we get that
	// whole week. Otherwise step back to the day before the current week.
	ws, we, err := ft.GetMatchupWeekBounds(yesterday, seasonStart)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if ws.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("no matchup week found for %s", yesterday.Format("2006-01-02"))
	}
	// If today is inside this week, back up to the prior week.
	if !today.After(we) {
		prior := ws.AddDate(0, 0, -1)
		ws, we, err = ft.GetMatchupWeekBounds(prior, seasonStart)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if ws.IsZero() {
			return time.Time{}, time.Time{}, fmt.Errorf("no prior matchup week found")
		}
	}
	return ws, we, nil
}

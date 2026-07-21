package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/recap"
	"github.com/nixon-commits/rosterbot/internal/schedule"
	"github.com/spf13/cobra"
)

var (
	recapDates string
	recapWeek  int
	recapOut   string
	recapJSON  bool
	recapTopN  int
	recapOpen  bool
)

var recapCmd = &cobra.Command{
	Use:   "recap",
	Short: "Render a Sleeper-style HTML recap of a completed matchup week",
	RunE:  runRecap,
}

func init() {
	recapCmd.Flags().StringVar(&recapDates, "dates", "", "matchup week date range YYYY-MM-DD:YYYY-MM-DD (overrides --week)")
	recapCmd.Flags().IntVar(&recapWeek, "week", 0, "matchup week number, 1-indexed (default: most recently completed week)")
	recapCmd.Flags().StringVar(&recapOut, "out", "", "write HTML to this path (default: stdout)")
	recapCmd.Flags().BoolVar(&recapJSON, "json", false, "emit machine-readable JSON instead of HTML")
	recapCmd.Flags().IntVar(&recapTopN, "top", 5, "number of players per leaderboard (Top Batters / Top Pitchers)")
	recapCmd.Flags().BoolVar(&recapOpen, "open", false, "open the rendered HTML in the default browser (requires --out)")
	rootCmd.AddCommand(recapCmd)
}

func runRecap(cmd *cobra.Command, args []string) error {
	today := todayET()
	_, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	weekStart, weekEnd, err := resolveRecapRange(ft, today)
	if err != nil {
		return fmt.Errorf("resolve range: %w", err)
	}
	if weekEnd.Before(weekStart) {
		return fmt.Errorf("empty recap window (%s to %s)", weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))
	}

	// Past matchup weeks are immutable, so reuse the same long TTL the
	// backtest command uses to avoid re-hitting Fantrax on rerun.
	snapTTL := 30 * 24 * time.Hour
	if noCache {
		snapTTL = 0
	}

	fmt.Fprintf(os.Stderr, "Building recap for %s – %s...\n",
		weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

	r, err := recap.Run(ft, recap.Options{
		WeekStart:  weekStart,
		WeekEnd:    weekEnd,
		WeekNumber: recapWeek, // 0 if not provided → recap.Run derives it
		CacheDir:   cacheDir,
		CacheTTL:   snapTTL,
		TopPlayers: recapTopN,
	})
	if err != nil {
		return fmt.Errorf("recap: %w", err)
	}

	out := os.Stdout
	if recapOut != "" {
		if err := os.MkdirAll(filepath.Dir(recapOut), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(recapOut), err)
		}
		f, err := os.Create(recapOut)
		if err != nil {
			return fmt.Errorf("create %s: %w", recapOut, err)
		}
		defer f.Close()
		out = f
	}

	if recapJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}

	if err := recap.Render(out, r); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if recapOut != "" {
		fmt.Fprintf(os.Stderr, "Wrote %s (%s)\n", recapOut, r.WeekLabel)
	}
	if recapOpen {
		if recapOut == "" {
			return fmt.Errorf("--open requires --out (no path to launch)")
		}
		if err := openInBrowser(recapOut); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// resolveRecapRange picks the matchup-week window. Priority: explicit --dates,
// then --week N, then default (last completed matchup week up through yesterday).
func resolveRecapRange(ft *fantrax.Client, today time.Time) (time.Time, time.Time, error) {
	if recapDates != "" {
		dates, err := parseDates(recapDates, today)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --dates: %w", err)
		}
		if len(dates) == 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("--dates produced no dates")
		}
		return dates[0], dates[len(dates)-1], nil
	}

	if recapWeek > 0 {
		ws, we, err := ft.GetMatchupWeekByNumber(recapWeek)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if ws.IsZero() {
			return time.Time{}, time.Time{}, fmt.Errorf("matchup week %d not found in season schedule", recapWeek)
		}
		return ws, we, nil
	}

	seasonStart, _, err := ft.GetSeasonDateRange()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	// First check today's week: a week whose end date is today is over once all
	// of that day's MLB games are final (Sunday games finishing in the evening).
	// The Fantrax weekly points field is a running in-week score and can't
	// signal closure, so use the MLB schedule instead. If today's games aren't
	// all final, fall through to the most recent fully-finished week.
	if ws, we, err := ft.GetMatchupWeekBounds(today, seasonStart); err == nil && !ws.IsZero() && we.Format("2006-01-02") == today.Format("2006-01-02") {
		sched := schedule.NewClient()
		if done, derr := sched.AllGamesFinalOn(we); derr == nil && done {
			return ws, we, nil
		}
	}

	return fantrax.LastCompletedMatchupWeek(ft, seasonStart, today)
}

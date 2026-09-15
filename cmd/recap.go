package cmd

import (
	"context"
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

// The return is named so the deferred Close below can promote its error; every
// `return` in the body still reads normally.
func runRecap(cmd *cobra.Command, args []string) (err error) {
	today := todayET()
	_, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	weekStart, weekEnd, err := resolveRecapRange(cmd.Context(), ft, today)
	if err != nil {
		return fmt.Errorf("resolve range: %w", err)
	}
	if weekEnd.Before(weekStart) {
		return fmt.Errorf("empty recap window (%s to %s)", weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))
	}

	// Past matchup weeks are immutable, so reuse the same long TTL the
	// backtest command uses to avoid re-hitting Fantrax on rerun.
	snapTTL := cacheTTL(fantrax.PastPeriodTTL)

	fmt.Fprintf(os.Stderr, "Building recap for %s – %s...\n",
		weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

	r, err := recap.Run(cmd.Context(), ft, recap.Options{
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
		// Close has to stay deferred — out is os.Stdout on the other branch and
		// several paths below return early — but its error is promoted to the
		// function's, since the render's final bytes are only flushed here. A
		// dropped Close would print "Wrote <path>" and exit 0 over a truncated
		// page, which recap-site then publishes to CloudFront. It never masks an
		// earlier failure: that one is the cause, this would be its echo.
		defer func() {
			if cerr := f.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("close %s: %w", recapOut, cerr)
			}
		}()
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
		if err := openInBrowser(cmd.Context(), recapOut); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// resolveRecapRange picks the matchup-week window. Priority: explicit --dates,
// then --week N, then default (last completed matchup week up through yesterday).
func resolveRecapRange(ctx context.Context, ft *fantrax.Client, today time.Time) (time.Time, time.Time, error) {
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

	periods, _, _, err := ft.GetScoringPeriodsAndTeams()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	sched := schedule.NewClient()
	return recapWindow(periods, today, recapWeek, func(day time.Time) bool {
		done, derr := sched.AllGamesFinalOn(ctx, day)
		return derr == nil && done
	})
}

// recapWindow picks the week from the LEAGUE's weekly period list — regular
// season and playoff rounds alike — never from the operator's own matchup
// weeks: a recap is league-scoped and Round 2 must render whether or not the
// operator's team is still in it. (backtest keeps the team-scoped
// LastCompletedMatchupWeek because it grades one team's lineup.)
//
// --week N is period N. Otherwise a period ending today is over once all of
// that day's MLB games are final (Sunday games finishing in the evening —
// the Fantrax weekly points field is a running score and can't signal
// closure); failing that, the most recent period that ended before today.
func recapWindow(periods []fantrax.ScoringPeriod, today time.Time, week int, todayDone func(time.Time) bool) (time.Time, time.Time, error) {
	if week > 0 {
		for _, p := range periods {
			if int(p.Number) == week {
				return p.StartDate, p.EndDate, nil
			}
		}
		return time.Time{}, time.Time{}, fmt.Errorf("week %d not found in the season schedule (%d periods)", week, len(periods))
	}
	if p := fantrax.FindCurrentPeriod(periods, today); p != nil &&
		p.EndDate.Format("2006-01-02") == today.Format("2006-01-02") && todayDone(today) {
		return p.StartDate, p.EndDate, nil
	}
	p := fantrax.LastCompletedPeriod(periods, today)
	if p == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("no completed week before %s", today.Format("2006-01-02"))
	}
	return p.StartDate, p.EndDate, nil
}

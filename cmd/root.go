package cmd

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/spf13/cobra"
)

func init() {
	log.SetFlags(0)
	log.SetPrefix("⚡ ")
}

var (
	dryRun  bool
	noCache bool
	verbose bool
)

var rootCmd = &cobra.Command{
	Use:   "rosterbot",
	Short: "Fantasy baseball roster automation for Fantrax leagues",
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "print planned moves without applying them")
	rootCmd.PersistentFlags().BoolVar(&noCache, "no-cache", false, "bypass file cache and fetch fresh data from APIs")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "show detailed log output instead of progress display")
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		cache.Verbose = verbose
	}
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// todayET returns today's date in America/New_York as a UTC midnight timestamp.
// This ensures GHA (which runs on UTC) uses the correct Eastern-time date for
// Fantrax scoring periods.
func todayET() time.Time {
	loc, _ := time.LoadLocation("America/New_York")
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// initApp loads configuration and creates a Fantrax client.
//
// When --no-cache isn't set, the client's on-disk cache layer is enabled
// (15m TTL for "today, drifts during the day" data like roster + FA pool;
// 7d TTL for season-stable data like slot counts + scoring weights;
// 30d TTL for past-period snapshots via ttlForPeriod). All commands
// inherit this — recap, optimize, prospects, etc. don't each need to
// remember to opt in.
func initApp(dates []time.Time) (*config.Config, *fantrax.Client, error) {
	cfg, err := config.Load(dryRun, dates)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	ft, err := fantrax.NewClient(cfg.LeagueID, cfg.TeamID)
	if err != nil {
		return nil, nil, fmt.Errorf("fantrax client: %w", err)
	}
	if !noCache {
		ft.SetCache(cacheDir)
	}
	// Surface stale-cache fallbacks (fresh fetch failed, serving cached copy)
	// as a Pushover push when creds are present. Console logging happens
	// unconditionally inside the cache package.
	if cfg.PushoverUserKey != "" && cfg.PushoverAPIToken != "" {
		userKey, apiToken := cfg.PushoverUserKey, cfg.PushoverAPIToken
		cache.Notify = func(title, message string) {
			if err := notify.SendPushover(userKey, apiToken, title, message); err != nil {
				fmt.Fprintf(os.Stderr, "warning: cache notify push failed: %v\n", err)
			}
		}
	}
	return cfg, ft, nil
}

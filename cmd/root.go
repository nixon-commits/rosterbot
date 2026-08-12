package cmd

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/nixon-commits/rosterbot/internal/cache"
	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/nixon-commits/rosterbot/internal/statestore"
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

// cacheDir is the on-disk file cache root shared by every command's Fantrax
// client (via initApp's SetCache call) and by any command that reads/writes
// cache-backed data directly (e.g. backtest, recap, team-values).
const cacheDir = ".cache"

// cacheTTL returns d unless --no-cache is set, in which case it returns 0
// (bypassing the cache entirely).
func cacheTTL(d time.Duration) time.Duration {
	if noCache {
		return 0
	}
	return d
}

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
// PastPeriodTTL for settled past-period snapshots via periodCachePolicy).
// All commands
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
	if err := initShared(); err != nil {
		return nil, nil, err
	}
	if !noCache {
		ft.SetCache(cacheDir)
	}
	return cfg, ft, nil
}

// initShared wires the sport-agnostic middle every command shares regardless
// of which upstream (Fantrax, Sleeper) it talks to: the cache store, the
// cache-notify stale-fallback alert, and the three activity recorders.
// Fantrax-specific setup (config.Load's required-var validation,
// fantrax.NewClient, ft.SetCache) stays in initApp; Sleeper-specific setup
// lives in initFootball. Both call this first so football commands need no
// FANTRAX_* env vars.
func initShared() error {
	// Load .env if present (local dev); ignore error if missing (GHA/Fargate
	// use env directly). config.Load also does this, but initFootball never
	// calls config.Load, so this package needs its own entry point.
	_ = godotenv.Load()

	if !noCache {
		// On Fargate, back the Cache with S3 directly (per-key) instead of
		// local files, so no bulk .cache sync is needed. statestore owns the
		// STATE_BUCKET decision + the cache/ prefix; nil means local (keep the
		// default fsStore).
		st, err := statestore.FromEnv().CacheStore()
		if err != nil {
			return fmt.Errorf("init cache store: %w", err)
		}
		if st != nil {
			cache.SetDefaultStore(st)
		}
	}
	// Surface stale-cache fallbacks (fresh fetch failed, serving cached copy)
	// as a Pushover push when creds are present. Console logging happens
	// unconditionally inside the cache package.
	userKey, apiToken := os.Getenv("PUSHOVER_USER_KEY"), os.Getenv("PUSHOVER_API_TOKEN")
	if userKey != "" && apiToken != "" {
		cache.Notify = func(title, message string) {
			if err := notify.SendPushover(userKey, apiToken, title, message); err != nil {
				fmt.Fprintf(os.Stderr, "warning: cache notify push failed: %v\n", err)
			}
		}
	}
	// Mirror every Pushover send into the durable activity feed (dual-send), so
	// the app can read what currently goes only to Pushover.
	installNotificationRecorder()
	// Persist each job's typed result under RUN_ID so the app can render
	// per-job result views (GET /v1/runs/{id}/output).
	installOutputRecorder()
	// Persist optimize's phase transitions under RUN_ID so the app can show a
	// live progress bar (GET /v1/runs/{id}/progress).
	installProgressRecorder()
	return nil
}

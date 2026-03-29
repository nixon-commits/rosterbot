package cmd

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/spf13/cobra"
)

func init() {
	log.SetFlags(0)
	log.SetPrefix("⚡ ")
}

var (
	dryRun  bool
	noCache bool
)

var rootCmd = &cobra.Command{
	Use:   "rosterbot",
	Short: "Fantasy baseball roster automation for Fantrax leagues",
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "print planned moves without applying them")
	rootCmd.PersistentFlags().BoolVar(&noCache, "no-cache", false, "bypass file cache and fetch fresh data from APIs")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// initApp loads configuration and creates a Fantrax client.
func initApp(dates []time.Time) (*config.Config, *fantrax.Client, error) {
	cfg, err := config.Load(dryRun, dates)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	ft, err := fantrax.NewClient(cfg.LeagueID, cfg.TeamID)
	if err != nil {
		return nil, nil, fmt.Errorf("fantrax client: %w", err)
	}
	return cfg, ft, nil
}

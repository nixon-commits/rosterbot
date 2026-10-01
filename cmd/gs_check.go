package cmd

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/gscheck"
	"github.com/spf13/cobra"
)

var gsCheckCmd = &cobra.Command{
	Use:   "gs-check",
	Short: "Check league-wide GS violations for the most recent scoring period",
	RunE:  runGSCheck,
}

// gsCheckPeriod is the --period flag: an explicit WEEKLY scoring period
// number (the N in the report's "Scoring Period N" caption, never the daily
// roster period) to check instead of the one that ended yesterday. It is the
// recovery path for a missed morning run and is registered as the season
// gate's explicit-window flag for gs-check, so it works in the off-season
// without ROSTERBOT_SEASON_GATE=off (rosterbot-97gs).
var gsCheckPeriod int

func init() {
	gsCheckCmd.Flags().IntVar(&gsCheckPeriod, "period", 0,
		"check this weekly scoring period (the N in \"Scoring Period N\") instead of the one that ended yesterday; recovers a missed run on any later day, off-season included")
	rootCmd.AddCommand(gsCheckCmd)
}

func runGSCheck(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	if !cfg.GSTrackingEnabled {
		fmt.Println("GS tracking disabled (GS_TRACKING_ENABLED not set) — nothing to check.")
		return nil
	}
	if cfg.PushoverGroupKey == "" || cfg.PushoverAPIToken == "" {
		return fmt.Errorf("PUSHOVER_GROUP_KEY and PUSHOVER_API_TOKEN env vars required for gs-check command")
	}

	if gsCheckPeriod < 0 {
		return fmt.Errorf("--period %d: a weekly scoring period number is positive", gsCheckPeriod)
	}
	return gscheck.RunGSCheckPeriod(cmd.Context(), ft, *cfg, fantrax.WeeklyPeriod(gsCheckPeriod))
}

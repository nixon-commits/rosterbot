package cmd

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/gscheck"
	"github.com/spf13/cobra"
)

var gsCheckCmd = &cobra.Command{
	Use:   "gs-check",
	Short: "Check league-wide GS violations for the most recent scoring period",
	RunE:  runGSCheck,
}

func init() {
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

	return gscheck.RunGSCheck(ft, *cfg)
}

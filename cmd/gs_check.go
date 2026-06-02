package cmd

import (
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/gscheck"
	"github.com/spf13/cobra"
)

var forceCheck bool

var gsCheckCmd = &cobra.Command{
	Use:   "gs-check",
	Short: "Check league-wide GS violations for the most recent scoring period",
	RunE:  runGSCheck,
}

func init() {
	gsCheckCmd.Flags().BoolVar(&forceCheck, "force", false,
		"skip end-of-period check, use most recent completed period")
	rootCmd.AddCommand(gsCheckCmd)
}

func runGSCheck(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	if cfg.GSMax <= 0 {
		return fmt.Errorf("GS_MAX env var required for gs-check command")
	}
	if cfg.PushoverGroupKey == "" || cfg.PushoverAPIToken == "" {
		return fmt.Errorf("PUSHOVER_GROUP_KEY and PUSHOVER_API_TOKEN env vars required for gs-check command")
	}

	return gscheck.RunGSCheck(ft, *cfg, forceCheck)
}

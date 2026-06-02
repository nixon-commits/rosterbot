package cmd

import (
	"strings"
	"time"

	"github.com/nixon-commits/rosterbot/internal/waivers"
	"github.com/spf13/cobra"
)

var (
	waiversTopN      int
	waiversPositions string
)

var waiversCmd = &cobra.Command{
	Use:   "waivers",
	Short: "Identify Statcast-driven waiver wire pickups",
	RunE:  runWaivers,
}

func init() {
	waiversCmd.Flags().IntVar(&waiversTopN, "top", 15, "max number of candidates to surface")
	waiversCmd.Flags().StringVar(&waiversPositions, "positions", "", "comma-separated position filter (e.g. \"OF,1B,SP\")")
	rootCmd.AddCommand(waiversCmd)
}

func runWaivers(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	var positions []string
	if waiversPositions != "" {
		for _, s := range strings.Split(waiversPositions, ",") {
			if t := strings.TrimSpace(s); t != "" {
				positions = append(positions, t)
			}
		}
	}

	opts := waivers.Options{
		TopN:             waiversTopN,
		Positions:        positions,
		NoCache:          noCache,
		DryRun:           cfg.DryRun,
		PushoverUserKey:  cfg.PushoverUserKey,
		PushoverAPIToken: cfg.PushoverAPIToken,
	}
	return waivers.Run(ft, today, opts)
}

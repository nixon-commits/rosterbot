package cmd

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/prospects"
	"github.com/spf13/cobra"
)

var listAll bool

var prospectsCmd = &cobra.Command{
	Use:   "prospects",
	Short: "Run minor league prospect report",
	RunE:  runProspects,
}

func init() {
	prospectsCmd.Flags().BoolVar(&listAll, "list-all", false, "list all minors-eligible players in the league with rankings")
	rootCmd.AddCommand(prospectsCmd)
}

func runProspects(cmd *cobra.Command, args []string) error {
	today := todayET()

	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	if listAll {
		return prospects.ListAllProspects(ft, *cfg, today)
	}

	return prospects.RunProspectReport(ft, *cfg, today)
}

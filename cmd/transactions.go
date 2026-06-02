package cmd

import (
	"time"

	"github.com/nixon-commits/rosterbot/internal/transactions"
	"github.com/spf13/cobra"
)

var transactionsCmd = &cobra.Command{
	Use:   "transactions",
	Short: "Check recent league trades and report HKB valuations",
	RunE:  runTransactions,
}

func init() {
	rootCmd.AddCommand(transactionsCmd)
}

func runTransactions(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	return transactions.CheckTrades(ft, ".cache", cfg.PushoverUserKey, cfg.PushoverAPIToken, cfg.DryRun)
}

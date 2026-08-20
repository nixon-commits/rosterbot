package cmd

import (
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/claims"
	"github.com/spf13/cobra"
)

var (
	claimsNoSignals bool
	claimsDropsMin  int
	claimsSince     string
)

var claimsCmd = &cobra.Command{
	Use:   "claims",
	Short: "Daily league-wide recap of processed waiver/FA claims",
	RunE:  runClaims,
}

func init() {
	claimsCmd.Flags().BoolVar(&claimsNoSignals, "no-signals", false, "skip the Statcast signal tie-in (faster)")
	claimsCmd.Flags().IntVar(&claimsDropsMin, "drops-min", 2000, "min HKB value for a dropped player to appear in the drops watch")
	claimsCmd.Flags().StringVar(&claimsSince, "since", "", "override cursor; report claims processed after YYYY-MM-DD")
	rootCmd.AddCommand(claimsCmd)
}

func runClaims(cmd *cobra.Command, args []string) error {
	today := todayET()
	cfg, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	var since time.Time
	if claimsSince != "" {
		since, err = time.Parse("2006-01-02", claimsSince)
		if err != nil {
			return err
		}
	}

	opts := claims.Options{
		CacheDir: cacheDir,
		// CursorPath: AWS sets CLAIMS_CURSOR_PATH=.waivers/last-claims.json so the
		// cursor rides the single-writer claims/ S3 prefix instead of the shared
		// cache/ prefix. Empty lets claims.Run apply its default
		// (.cache/last-claims.json).
		CursorPath: os.Getenv("CLAIMS_CURSOR_PATH"),
		DryRun:     cfg.DryRun,
		NoSignals:  claimsNoSignals,
		Since:      since,
		DropsMin:   claimsDropsMin,
	}
	return claims.Run(cmd.Context(), ft, today, opts)
}

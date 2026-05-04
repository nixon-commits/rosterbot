package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/recap"
	"github.com/spf13/cobra"
)

var (
	recapSiteOut  string
	recapSiteTopN int
)

var recapSiteCmd = &cobra.Command{
	Use:   "recap-site",
	Short: "Render every completed matchup week into a static site directory",
	Long: `Renders one HTML file per completed matchup week into --out plus an
index.html that mirrors the latest week. Each page carries a dropdown
linking to all other weeks. Intended for GitHub Pages deployment via
actions/deploy-pages — no files are committed back to the repo.`,
	RunE: runRecapSite,
}

func init() {
	recapSiteCmd.Flags().StringVar(&recapSiteOut, "out", "dist", "output directory for rendered HTML")
	recapSiteCmd.Flags().IntVar(&recapSiteTopN, "top", 10, "number of players per leaderboard (Top Batters / Top Pitchers)")
	rootCmd.AddCommand(recapSiteCmd)
}

func runRecapSite(cmd *cobra.Command, args []string) error {
	today := todayET()
	_, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	snapTTL := 30 * 24 * time.Hour
	if noCache {
		snapTTL = 0
	}

	fmt.Fprintf(os.Stderr, "Building recap site to %s (today=%s)...\n",
		recapSiteOut, today.Format("2006-01-02"))

	return recap.RunSite(ft, recap.SiteOptions{
		OutDir: recapSiteOut,
		Today:  today,
		Recap: recap.Options{
			CacheDir:   cacheDir,
			CacheTTL:   snapTTL,
			TopPlayers: recapSiteTopN,
		},
	})
}

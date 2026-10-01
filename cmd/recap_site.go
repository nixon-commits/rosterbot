package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/recap"
	"github.com/spf13/cobra"
)

var (
	recapSiteOut  string
	recapSiteTopN int
	recapSiteOpen bool
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
	recapSiteCmd.Flags().IntVar(&recapSiteTopN, "top", 5, "number of players per leaderboard (Top Batters / Top Pitchers)")
	recapSiteCmd.Flags().BoolVar(&recapSiteOpen, "open", false, "open the rendered index.html in the default browser after building")
	rootCmd.AddCommand(recapSiteCmd)
}

func runRecapSite(cmd *cobra.Command, args []string) error {
	today := todayET()
	_, ft, err := initApp([]time.Time{today})
	if err != nil {
		return err
	}

	snapTTL := cacheTTL(fantrax.PastPeriodTTL)

	fmt.Fprintf(os.Stderr, "Building recap site to %s (today=%s)...\n",
		recapSiteOut, today.Format("2006-01-02"))

	if err := recap.RunSite(cmd.Context(), ft, recap.SiteOptions{
		OutDir: recapSiteOut,
		Today:  today,
		Recap: recap.Options{
			CacheDir:   cacheDir,
			CacheTTL:   snapTTL,
			TopPlayers: recapSiteTopN,
		},
	}); err != nil {
		return recapSiteStop(cmd, err, today)
	}

	if recapSiteOpen {
		index := filepath.Join(recapSiteOut, "index.html")
		if err := openInBrowser(cmd.Context(), index); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// recapSiteStop decides what a failed build means. Nothing rendered yet — a
// pre-season run against a league whose first week has not closed — is a clean
// stop, not a failure: the same exit-0, off_season-outcome, cobra-silenced shape
// the season gate uses (checkSeasonGate). recap-site is year-round precisely so
// it can render the final week on the first off-season day, which means the
// gate's start bound no longer hides the empty-week case; left as an error it
// would be a FAILED ledger row every Monday from a new league's creation until
// its first week completes, and opsalert.leadingFailures pages on the streak.
// Every other error is a real failure and passes through untouched.
func recapSiteStop(cmd *cobra.Command, err error, today time.Time) error {
	if err == nil || !errors.Is(err, recap.ErrNoCompletedWeeks) {
		return err
	}
	line := fmt.Sprintf("No completed matchup weeks yet as of %s: nothing to render. Nothing to do.",
		today.Format("2006-01-02"))
	fmt.Println(line)
	recordRunOutcome(lineupapi.RunOutcomeOffSeason)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return &offSeasonStop{line: line}
}

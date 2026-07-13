package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/notify"
	"github.com/spf13/cobra"
)

// shadowSnapshotRoot is where the shadow command writes per-system projection
// snapshots. Each system gets its own Hive-style partition beneath it
// (system=<sys>/<date>.json), graded later by `grade` and synced to S3 like
// the rest of .backtest/.
const shadowSnapshotRoot = ".backtest/snapshots-systems"

// shadowSystems are the projection systems captured side-by-side for the
// model-comparison report. In season these are the rest-of-season variants —
// the only ones meaningful to compare day to day (preseason variants are frozen
// full-season forecasts that go stale). depthcharts-ros is included so its
// slice feeds the existing detailed dashboard and the comparison panel from one
// consistent, same-run capture.
var shadowSystems = []string{"steamer-ros", "depthcharts-ros", "thebatx-ros", "atc-ros"}

// systemSnapshotDir returns the per-system snapshot partition under root.
func systemSnapshotDir(root, system string) string {
	return filepath.Join(root, "system="+system)
}

var shadowCmd = &cobra.Command{
	Use:   "shadow",
	Short: "Capture every projection system's lineup projections (read-only) for model comparison",
	Long: `Runs the full optimize pipeline once per projection system in dry-run, writing
a per-system projection snapshot for each. No lineup is applied and no
notification is sent — this only captures what each system would have projected,
so a later 'grade' run can score them against actuals and the projection report
can rank which system is most accurate.`,
	RunE: runShadow,
}

func init() {
	// --dates mirrors optimize's flag so a specific capture date can be forced
	// (default: today). Bound to the same datesStr var optimize reads.
	shadowCmd.Flags().StringVar(&datesStr, "dates", "", "date(s) to capture: YYYY-MM-DD (default: today)")
	rootCmd.AddCommand(shadowCmd)
}

func runShadow(cmd *cobra.Command, args []string) error {
	// Capture mode: redirect snapshots to per-system partitions and never apply.
	captureSystemRoot = shadowSnapshotRoot
	dryRun = true
	// Roster-alert noise is irrelevant to a capture run.
	checkRoster = false
	defer func() {
		captureSystemRoot = ""
	}()

	state := loadShadowNoDataState(shadowNoDataStateFile)
	var transitions strings.Builder

	for _, sys := range shadowSystems {
		projectionSystem = sys
		fmt.Printf("\n=== shadow capture: %s ===\n", sys)
		if err := runOptimize(cmd, args); err != nil {
			return fmt.Errorf("shadow capture %s: %w", sys, err)
		}
		cur := systemNoData{
			Hitters:  lastProjectionLoadResult.HittersNoData,
			Pitchers: lastProjectionLoadResult.PitchersNoData,
		}
		transitions.WriteString(describeNoDataTransition(sys, state[sys], cur))
		state[sys] = cur
	}

	if err := saveShadowNoDataState(shadowNoDataStateFile, state); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist shadow no-data state: %v\n", err)
	}

	// Alert only on a state *change* (a system going down or recovering), not
	// on every day an outage continues — a still-down system already logs a
	// WARNING per run via runOptimize, this is for the transition itself.
	if msg := strings.TrimSpace(transitions.String()); msg != "" {
		userKey := os.Getenv("PUSHOVER_USER_KEY")
		apiToken := os.Getenv("PUSHOVER_API_TOKEN")
		if userKey != "" && apiToken != "" {
			if err := notify.SendPushover(userKey, apiToken, "Shadow: projection system status changed", msg); err != nil {
				fmt.Fprintf(os.Stderr, "warning: shadow no-data Pushover failed: %v\n", err)
			}
		}
		fmt.Println("\n" + msg)
	}
	return nil
}

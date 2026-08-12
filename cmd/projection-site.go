package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/dynasty"
	"github.com/nixon-commits/rosterbot/internal/lineupgap"
	"github.com/nixon-commits/rosterbot/internal/recaplog"
	"github.com/nixon-commits/rosterbot/internal/report"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/valuereport"
	"github.com/spf13/cobra"
)

var (
	projSiteOut  string
	projSiteOpen bool
)

var projectionSiteCmd = &cobra.Command{
	Use:   "projection-site",
	Short: "Render the projection-accuracy dashboard from the Analysis Store",
	Long: `Reads the Graded Snapshots written by the grade command (analysis/grades/
on S3 when STATE_BUCKET is set, else local .analysis/) and writes the
aggregated model as JSON to <out>/model.json (fed to the dashboard SPA).
Intended for daily deployment to its own S3+CloudFront, mirroring the recap site.`,
	RunE: runProjectionSite,
}

func init() {
	projectionSiteCmd.Flags().StringVar(&projSiteOut, "out", "report", "output directory for the rendered dashboard")
	projectionSiteCmd.Flags().BoolVar(&projSiteOpen, "open", false, "open the rendered model.json in the default handler")
	rootCmd.AddCommand(projectionSiteCmd)
}

func runProjectionSite(cmd *cobra.Command, args []string) error {
	today := todayET()

	reader, err := statestore.FromEnv().AnalysisReader()
	if err != nil {
		return fmt.Errorf("init analysis reader: %w", err)
	}

	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read grades: %w", err)
	}

	// Earliest graded date is a safe season-start floor with no Fantrax call.
	seasonStart := today
	for _, r := range rows {
		if d, err := time.Parse("2006-01-02", r.Dt); err == nil && d.Before(seasonStart) {
			seasonStart = d
		}
	}

	m := report.Aggregate(rows, time.Now().UTC(), seasonStart)

	if err := os.MkdirAll(projSiteOut, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", projSiteOut, err)
	}
	outPath := filepath.Join(projSiteOut, "model.json")
	if err := writeJSONModel(outPath, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d graded rows, latest %s)\n", outPath, len(rows), m.LatestDate)

	// Emit value.json (team HKB value tracker) alongside the accuracy model.
	// It reads its own store and is additive, so a team-value hiccup soft-fails
	// rather than blocking the accuracy dashboard deploy.
	if err := renderValueSite(projSiteOut); err != nil {
		fmt.Fprintf(os.Stderr, "warning: value.json not written: %v\n", err)
	}

	// Emit views.json (recap-site readership) on the same terms: its own source,
	// additive, and soft-failing so a log-read hiccup never blocks the accuracy
	// dashboard deploy.
	if err := renderViewsSite(projSiteOut); err != nil {
		fmt.Fprintf(os.Stderr, "warning: views.json not written: %v\n", err)
	}

	// Emit gap.json (realized-vs-hindsight lineup gap) on the same terms: its
	// own store, additive, soft-failing so a gap hiccup never blocks the
	// accuracy dashboard deploy.
	if err := renderGapSite(projSiteOut); err != nil {
		fmt.Fprintf(os.Stderr, "warning: gap.json not written: %v\n", err)
	}

	// Emit football.json (dynasty football standings) on the same terms: its
	// own store, additive, soft-failing so a football hiccup never blocks the
	// accuracy dashboard deploy. projection-site never calls initApp (pure
	// statestore reads), so there is no Fantrax coupling to work around here.
	if err := renderFootballSite(projSiteOut); err != nil {
		fmt.Fprintf(os.Stderr, "warning: football.json not written: %v\n", err)
	}

	if projSiteOpen {
		if err := openInBrowser(outPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// renderValueSite reads the Team Value Store (S3 when STATE_BUCKET is set, else
// local .teamvalue/) and writes <outDir>/value.json. An empty store still
// writes a valid (empty) model rather than erroring.
func renderValueSite(outDir string) error {
	reader, err := statestore.FromEnv().TeamValueReader()
	if err != nil {
		return fmt.Errorf("init team-value reader: %w", err)
	}
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read team values: %w", err)
	}
	vm := valuereport.BuildModel(rows)
	outPath := filepath.Join(outDir, "value.json")
	if err := writeJSONModel(outPath, vm); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d team-value rows)\n", outPath, len(rows))
	return nil
}

// viewsLimit is how many recent page reads views.json carries. The tab shows a
// recent-hits table, not a history, so this is a display cap rather than a
// sample — and it bounds how many log objects ReadRecent has to open.
const viewsLimit = 200

// renderViewsSite reads the recap site's CloudFront access logs and writes
// <outDir>/views.json.
//
// Skipped entirely (no file, no error) when RECAP_LOG_BUCKET is unset, which is
// the normal local-dev case: CloudFront writes these logs only in AWS, so there
// is nothing to read locally. A deployed run with no readers yet still writes a
// valid empty model, so the tab can say "no reads yet" rather than erroring.
func renderViewsSite(outDir string) error {
	store, err := statestore.FromEnv().RecapLogReader()
	if err != nil {
		return fmt.Errorf("init recap-log reader: %w", err)
	}
	if store == nil {
		return nil
	}
	m, err := recaplog.ReadRecent(store, "", viewsLimit, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("read recap logs: %w", err)
	}
	outPath := filepath.Join(outDir, "views.json")
	if err := writeJSONModel(outPath, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d recent page reads)\n", outPath, len(m.Hits))
	return nil
}

// renderGapSite reads the Lineup Gap Store (S3 when STATE_BUCKET is set, else
// local .lineupgap/) and writes <outDir>/gap.json.
func renderGapSite(outDir string) error {
	reader, err := statestore.FromEnv().LineupGapReader()
	if err != nil {
		return fmt.Errorf("init lineup gap reader: %w", err)
	}
	return writeGapModel(reader, outDir)
}

// writeGapModel is the I/O-light half of renderGapSite, split out so the model
// and file shape are testable against a local store with no environment.
//
// An empty store still writes a valid (empty) model rather than erroring: the
// gap block is the first thing on the Projections tab, and a fresh deploy must
// render "no data yet" rather than a broken headline.
func writeGapModel(reader lineupgap.Reader, outDir string) error {
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read lineup gaps: %w", err)
	}
	m := lineupgap.BuildModel(rows, time.Now().UTC())

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	outPath := filepath.Join(outDir, "gap.json")
	if err := writeJSONModel(outPath, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d lineup-gap days)\n", outPath, len(rows))
	return nil
}

// renderFootballSite reads the Dynasty Value Store (S3 when STATE_BUCKET is
// set, else local .footballvalue/) and writes <outDir>/football.json. An
// empty store still writes a valid (empty) model rather than erroring.
func renderFootballSite(outDir string) error {
	reader, err := statestore.FromEnv().FootballValueReader()
	if err != nil {
		return fmt.Errorf("init football-value reader: %w", err)
	}
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read football values: %w", err)
	}
	coverage, err := reader.ReadAllCoverage()
	if err != nil {
		return fmt.Errorf("read football coverage: %w", err)
	}
	fm := dynasty.BuildModel(rows, coverage, time.Now())
	outPath := filepath.Join(outDir, "football.json")
	if err := writeJSONModel(outPath, fm); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d football rows, %d teams)\n", outPath, len(rows), len(fm.Teams))
	return nil
}

// writeJSONModel encodes v as indented JSON at path.
//
// Close is returned rather than deferred-and-dropped because these files are the
// dashboard's entire data feed: json.Encoder buffers, the final flush happens
// inside Close, and entrypoint.sh syncs whatever landed on disk regardless. A
// dropped Close error means a truncated model.json published to CloudFront while
// the run printed "Wrote ..." and exited 0 — a broken dashboard reported as a
// success. On an encode error the Close error is joined rather than dropped: the
// encode failure is the cause and reads first, but a Close that also fails says
// something independent about the disk, which is worth knowing on exactly the
// path where the write already went wrong.
func writeJSONModel(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return errors.Join(fmt.Errorf("encode %s: %w", filepath.Base(path), err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

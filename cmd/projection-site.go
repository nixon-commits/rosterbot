package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nixon-commits/rosterbot/internal/analysis"
	"github.com/nixon-commits/rosterbot/internal/analysis/s3grades"
	"github.com/nixon-commits/rosterbot/internal/report"
	"github.com/nixon-commits/rosterbot/internal/teamvalue"
	"github.com/nixon-commits/rosterbot/internal/teamvalue/s3teamvalue"
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
on S3 when STATE_BUCKET is set, else local .analysis/) and renders a single
self-contained HTML dashboard to <out>/index.html. Intended for daily
deployment to its own S3+CloudFront, mirroring the recap site.`,
	RunE: runProjectionSite,
}

func init() {
	projectionSiteCmd.Flags().StringVar(&projSiteOut, "out", "report", "output directory for the rendered dashboard")
	projectionSiteCmd.Flags().BoolVar(&projSiteOpen, "open", false, "open the rendered index.html in the default browser")
	rootCmd.AddCommand(projectionSiteCmd)
}

func runProjectionSite(cmd *cobra.Command, args []string) error {
	today := todayET()

	var reader analysis.Reader
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		r, err := s3grades.NewReader(context.Background(), bucket, "analysis/")
		if err != nil {
			return fmt.Errorf("init analysis reader: %w", err)
		}
		reader = r
	} else {
		reader = analysis.NewFileReader(".analysis")
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
	outPath := filepath.Join(projSiteOut, "index.html")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := report.Render(f, m); err != nil {
		return fmt.Errorf("render: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d graded rows, latest %s)\n", outPath, len(rows), m.LatestDate)

	// Emit value.html (team HKB value tracker) alongside the accuracy dashboard.
	// It reads its own store and is additive, so a team-value hiccup soft-fails
	// rather than blocking the accuracy dashboard deploy.
	if err := renderValueSite(projSiteOut); err != nil {
		fmt.Fprintf(os.Stderr, "warning: value.html not rendered: %v\n", err)
	}

	if projSiteOpen {
		if err := openInBrowser(outPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
	return nil
}

// renderValueSite reads the Team Value Store (S3 when STATE_BUCKET is set, else
// local .teamvalue/) and writes <outDir>/value.html. An empty store renders a
// graceful collecting-data page rather than erroring.
func renderValueSite(outDir string) error {
	var reader teamvalue.Reader
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		r, err := s3teamvalue.NewReader(context.Background(), bucket, teamValuePrefix)
		if err != nil {
			return fmt.Errorf("init team-value reader: %w", err)
		}
		reader = r
	} else {
		reader = teamvalue.NewFileReader(teamValueLocalDir)
	}
	rows, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read team values: %w", err)
	}
	outPath := filepath.Join(outDir, "value.html")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := valuereport.Render(f, valuereport.BuildModel(rows)); err != nil {
		return fmt.Errorf("render value: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s (%d team-value rows)\n", outPath, len(rows))
	return nil
}

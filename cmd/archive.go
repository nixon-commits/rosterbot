package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/prospects"
	"github.com/nixon-commits/rosterbot/internal/waivers"
	"github.com/spf13/cobra"
)

var archiveDate string
var archiveDryRun bool

var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Capture a durable daily snapshot of ephemeral upstream data (HKB, projections, Savant, prospects)",
	RunE:  runArchive,
}

func init() {
	archiveCmd.Flags().StringVar(&archiveDate, "date", "", "capture date YYYY-MM-DD (default: today UTC)")
	archiveCmd.Flags().BoolVar(&archiveDryRun, "dry-run", false, "fetch and report sizes without writing")
	rootCmd.AddCommand(archiveCmd)
}

func runArchive(cmd *cobra.Command, args []string) error {
	date := time.Now().UTC()
	if archiveDate != "" {
		d, err := time.Parse("2006-01-02", archiveDate)
		if err != nil {
			return fmt.Errorf("bad --date: %w", err)
		}
		date = d
	}
	sources := []archive.Source{
		archive.FuncSource{N: "hkb", F: hkb.ArchiveArtifacts},
		archive.FuncSource{N: "projections", F: projections.ArchiveArtifacts},
		archive.FuncSource{N: "savant", F: waivers.ArchiveArtifacts},
		archive.FuncSource{N: "prospects", F: prospects.ArchiveArtifacts},
	}
	return runArchiveSources(context.Background(), sources, archive.Writer{Root: ".archive"}, date, archiveDryRun)
}

// runArchiveSources runs each source independently. A single source failure is
// logged and skipped; the command errors only when every source failed.
func runArchiveSources(ctx context.Context, sources []archive.Source, w archive.Writer, date time.Time, dryRun bool) error {
	if len(sources) == 0 {
		return nil
	}
	var failed int
	for _, s := range sources {
		arts, err := s.Fetch(ctx, date)
		if err != nil {
			warn("archive %s: %v", s.Name(), err)
			failed++
			continue
		}
		if dryRun {
			var total int
			for _, a := range arts {
				total += len(a.Bytes)
			}
			fmt.Printf("archive %s (dry-run): %d artifact(s), %d bytes\n", s.Name(), len(arts), total)
			continue
		}
		if err := w.Write(date, s.Name(), arts); err != nil {
			warn("archive write %s: %v", s.Name(), err)
			failed++
		}
	}
	if failed == len(sources) {
		return fmt.Errorf("archive: all %d sources failed", len(sources))
	}
	return nil
}

package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/nixon-commits/rosterbot/internal/archive"
	"github.com/nixon-commits/rosterbot/internal/hkb"
	"github.com/nixon-commits/rosterbot/internal/projections"
	"github.com/nixon-commits/rosterbot/internal/prospects"
	"github.com/nixon-commits/rosterbot/internal/statcast"
	"github.com/nixon-commits/rosterbot/internal/statestore"
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

// resolveArchiveDate turns the --date flag into the partition this run writes.
//
// It refuses any date but today because the sources are not date-parameterised:
// hkb, projections and prospects fetch whatever the upstream serves at the
// moment of the call, and only two of savant's five CSVs (the rolling 14d/30d
// windows) take a date range at all. So `--date 2026-08-01` does not backfill
// 2026-08-01 — it writes 13 of 15 blobs of TODAY'S data under dt=2026-08-01,
// which is worse than the gap it appears to close (rosterbot-0l31). The Infra
// page then counts the day as present, and the archive's entire reason for
// existing is being the only surviving record of what upstream said on a given
// day, so a partition of the wrong day's bytes is unfalsifiable later. That is
// also why layout.Archive is NoBackfill: the gap is real and permanent, and the
// only honest response is to leave it.
//
// Today is allowed, and is the one date the fetches actually describe — that is
// the legitimate same-day retry after a failed 14:15 UTC run.
//
// A future date is refused too, for that reason plus one of its own: a dt=
// ahead of today makes LatestPartition permanently fresh, so the Infra row
// would stop reporting staleness for the artifact it was dated into.
//
// There is deliberately no --force. The flag could only ever be used to write
// data that is wrong, and nothing downstream can distinguish such a partition
// from a real one.
func resolveArchiveDate(flag string, now time.Time) (time.Time, error) {
	if flag == "" {
		return now.UTC(), nil
	}
	d, err := time.Parse("2006-01-02", flag)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad --date: %w", err)
	}
	// YYYY-MM-DD sorts lexicographically in chronological order, so comparing
	// the rendered days IS comparing the days — and it avoids reasoning about
	// the instant time.Parse invented for a flag that names a calendar day.
	got, today := d.Format("2006-01-02"), now.UTC().Format("2006-01-02")
	switch {
	case got < today:
		return time.Time{}, fmt.Errorf(
			"--date %s is in the past and archive cannot backfill: every source fetches CURRENT "+
				"upstream data (HKB, FanGraphs and the prospect board publish no history), so this "+
				"would write today's data under dt=%s. Run without --date (today is %s) and leave "+
				"the gap — it is permanent, which is why archive/ is flagged NoBackfill", got, got, today)
	case got > today:
		return time.Time{}, fmt.Errorf(
			"--date %s is in the future: the sources return today's data either way, and a "+
				"partition dated ahead of today would also mask staleness on the Infra page "+
				"(today is %s)", got, today)
	}
	return d, nil
}

func runArchive(cmd *cobra.Command, args []string) error {
	// Checked before the dry-run branch below, so --dry-run is refused on the
	// same dates the real run is. A dry run exists to preview the real one; a
	// preview of a date the real run rejects reports sizes for today's bytes
	// under a caller-chosen day, which is the same lie in miniature.
	date, err := resolveArchiveDate(archiveDate, time.Now())
	if err != nil {
		return err
	}
	sources := []archive.Source{
		archive.FuncSource{N: "hkb", F: hkb.ArchiveArtifacts},
		archive.FuncSource{N: "projections", F: projections.ArchiveArtifacts},
		archive.FuncSource{N: "savant", F: statcast.ArchiveArtifacts},
		archive.FuncSource{N: "prospects", F: prospects.ArchiveArtifacts},
	}
	// Dry-run must not need a store: it only fetches and reports sizes, and the
	// S3 constructor would otherwise demand credentials to do nothing.
	var w archive.Writer
	if !archiveDryRun {
		w, err = statestore.FromEnv().ArchiveWriter()
		if err != nil {
			return fmt.Errorf("archive store: %w", err)
		}
	}
	return runArchiveSources(context.Background(), sources, w, date, archiveDryRun)
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

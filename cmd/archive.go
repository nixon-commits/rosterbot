package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
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
var archiveCatchUp bool

var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Capture a durable daily snapshot of ephemeral upstream data (HKB, projections, Savant, prospects)",
	RunE:  runArchive,
}

func init() {
	archiveCmd.Flags().StringVar(&archiveDate, "date", "", "capture date YYYY-MM-DD (default: today UTC)")
	archiveCmd.Flags().BoolVar(&archiveDryRun, "dry-run", false, "fetch and report sizes without writing")
	archiveCmd.Flags().BoolVar(&archiveCatchUp, "catch-up", false,
		"fetch only the sources today has not captured yet; the hourly ArchiveCatchUp schedule runs this")
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
// the legitimate same-day retry after a failed 14:15 UTC run. That retry is no
// longer only a manual one: the ArchiveCatchUp schedule fires `archive
// --catch-up` hourly from 15:15 to 23:15 UTC, and this rule is why it stops at
// 23:15 rather than running through the night.
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
	// S3 constructor would otherwise demand credentials to do nothing. A
	// catch-up is the exception even when it is a dry run, because the mode IS
	// a question to the store about what today already holds.
	var w archive.Writer
	if !archiveDryRun || archiveCatchUp {
		w, err = statestore.FromEnv().ArchiveWriter()
		if err != nil {
			return fmt.Errorf("archive store: %w", err)
		}
	}
	return runArchiveSources(context.Background(), sources, w, date, archiveRun{dryRun: archiveDryRun, catchUp: archiveCatchUp}, os.Stdout)
}

// runArchiveSources runs each source independently and reports the run as a
// whole. Isolation is kept — a lost source never stops the next one from
// being fetched and written — but the run is no longer silent about the loss.
//
// Two lines are the positive control (rosterbot-kvgw). Every lost source gets
// its own `archive: LOST source <name>: <reason>` line, and every run ends
// with one summary, `archive: wrote N/M sources (…); lost K (…)`, printed
// unconditionally. Before this a healthy run logged NOTHING: measured over the
// 14:15Z CloudWatch slot, nine of eleven days had zero events and the two
// FanGraphs-403 days had nine `warn:` lines, so a flawless run and a total loss
// of two as-of-date sources were indistinguishable by log content, and
// dt=2026-09-26/27 of projections and prospects were permanently gone before
// anyone diffed S3 by hand. A line that prints only on failure cannot prove a
// success happened; the count can.
//
// --catch-up (opt.catchUp) changes which sources are attempted, and nothing
// else. Each source is skipped when the store says this day already holds
// artifacts for it, so a day the 14:15 run captured in full costs one listing
// per source and no upstream traffic. That cheapness is the point: it is what
// lets the ArchiveCatchUp schedule fire every hour to 23:15 UTC, which is the
// only real defence against a Cloudflare challenge that lasts hours. A store
// read that FAILS fetches the source anyway — "I could not look" is not
// "already captured", and the opposite reading would silence every catch-up
// fire during an S3 incident.
//
// The exit policy below is deliberately unchanged for a catch-up: a day still
// missing a source fails the run however many times it has been attempted. A
// SUCCESS record from a fire that captured nothing is the precise trap the
// positive control above was built to close, and it would be worse here, since
// the later fires are the ones an operator would read as the final word.
//
// One limit worth stating: the skip is per SOURCE, not per artifact. A
// partition holding six of projections' eight blobs reads as captured, so a
// later clean hour will not complete it. That matches the grain of the exit
// policy below, where a partial loss inside a source is already that source's
// own warning rather than a lost day.
//
// The exit policy escalates by GRAIN. A whole source lost — fetch error, write
// error, or a fetch that produced zero artifacts, which is the same absent
// partition by another route — fails the run, so the ledger records FAILED and
// opsalert's Streak pages on it; the other sources are already written by
// then, so the failure costs nothing they captured. A partial loss INSIDE a
// source (projections archiving six of its eight FanGraphs blobs) is that
// source's own warning to stderr and stays one: the day has a partition, and
// the shortfall is named where it happened.
// archiveRun carries the run's modes. A struct rather than two bool
// parameters: `false, false` at a call site says nothing about which mode is
// which, and both are read in more than one place below.
type archiveRun struct {
	dryRun  bool
	catchUp bool
}

func runArchiveSources(ctx context.Context, sources []archive.Source, w archive.Writer, date time.Time, opt archiveRun, out io.Writer) error {
	if len(sources) == 0 {
		return nil
	}
	day := date.UTC().Format("2006-01-02")
	var written, lost, already []string
	for _, s := range sources {
		if opt.catchUp {
			has, err := w.Has(date, s.Name())
			switch {
			case err != nil:
				// "I could not look" is not "already captured". Reading it as
				// the latter would turn every catch-up fire into a no-op
				// during exactly the kind of incident that makes them matter,
				// so an unreadable partition is fetched.
				fmt.Fprintf(out, "archive: cannot tell whether %s is captured (%v); fetching it\n", s.Name(), err)
			case has:
				already = append(already, s.Name())
				continue
			}
		}
		arts, err := s.Fetch(ctx, date)
		switch {
		case err != nil:
			fmt.Fprintf(out, "archive: LOST source %s: %v\n", s.Name(), err)
			lost = append(lost, s.Name())
			continue
		case len(arts) == 0:
			fmt.Fprintf(out, "archive: LOST source %s: produced 0 artifacts\n", s.Name())
			lost = append(lost, s.Name())
			continue
		}
		if opt.dryRun {
			var total int
			for _, a := range arts {
				total += len(a.Bytes)
			}
			fmt.Fprintf(out, "archive %s (dry-run): %d artifact(s), %d bytes\n", s.Name(), len(arts), total)
			written = append(written, s.Name())
			continue
		}
		if err := w.Write(date, s.Name(), arts); err != nil {
			fmt.Fprintf(out, "archive: LOST source %s: write: %v\n", s.Name(), err)
			lost = append(lost, s.Name())
			continue
		}
		written = append(written, s.Name())
	}

	// A catch-up that found nothing to do still says so. Silence would have to
	// be interpreted, and this job's whole history is about not making anyone
	// interpret silence.
	if opt.catchUp && len(already) == len(sources) {
		fmt.Fprintf(out, "archive (catch-up): dt=%s already complete; all %d sources captured\n", day, len(sources))
		return nil
	}

	verb, prefix := "wrote", "archive"
	switch {
	case opt.dryRun:
		verb, prefix = "fetched", "archive (dry-run)"
	case opt.catchUp:
		verb, prefix = "filled", "archive (catch-up)"
	}
	summary := fmt.Sprintf("%s: %s %d/%d sources (%s)", prefix, verb, len(written), len(sources), strings.Join(written, ", "))
	if opt.catchUp {
		// The denominator is what this fire ATTEMPTED, not the source count:
		// "filled 1/4" on a day with three sources already captured reads as
		// three losses.
		summary = fmt.Sprintf("%s: dt=%s %s %d/%d attempted (%s)", prefix, day, verb, len(written), len(sources)-len(already), strings.Join(written, ", "))
		if len(already) > 0 {
			summary += fmt.Sprintf("; already captured %d (%s)", len(already), strings.Join(already, ", "))
		}
	}
	if len(lost) > 0 {
		summary += fmt.Sprintf("; lost %d (%s)", len(lost), strings.Join(lost, ", "))
	}
	fmt.Fprintln(out, summary)

	if len(lost) > 0 {
		return fmt.Errorf("archive: lost %d of %d source(s): %s", len(lost), len(sources), strings.Join(lost, ", "))
	}
	return nil
}

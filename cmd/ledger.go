package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/s3lineup"
	"github.com/spf13/cobra"
)

// runWriter is the write side of the run ledger, satisfied by both the S3 and
// local-file run stores.
type runWriter interface {
	PutRun(context.Context, lineupapi.RunDetail) error
}

var (
	ledgerID       string
	ledgerCommand  string
	ledgerStatus   string
	ledgerExitCode int
	ledgerStarted  string
	ledgerEnded    string
	ledgerTrigger  string
	ledgerLogFile  string
)

// run-ledger is an internal command invoked by entrypoint.sh to record one job
// run into the ledger (start: RUNNING; end: SUCCESS/FAILED). JSON is built here
// in Go rather than in shell so escaping the log tail stays sane.
var ledgerCmd = &cobra.Command{
	Use:    "run-ledger",
	Short:  "Internal: write a run ledger record (used by entrypoint.sh)",
	Hidden: true,
	RunE:   runLedger,
}

func init() {
	f := ledgerCmd.Flags()
	f.StringVar(&ledgerID, "id", "", "run id (ECS task id)")
	f.StringVar(&ledgerCommand, "command", "", "command that ran")
	f.StringVar(&ledgerStatus, "status", "", "RUNNING | SUCCESS | FAILED")
	f.IntVar(&ledgerExitCode, "exit-code", -1, "process exit code (-1 = unset, for RUNNING)")
	f.StringVar(&ledgerStarted, "started", "", "RFC3339 start time")
	f.StringVar(&ledgerEnded, "ended", "", "RFC3339 end time")
	f.StringVar(&ledgerTrigger, "trigger", "schedule", "schedule | manual")
	f.StringVar(&ledgerLogFile, "log-file", "", "path to captured output (tailed into log_tail on failure)")
	rootCmd.AddCommand(ledgerCmd)
}

func runLedger(cmd *cobra.Command, args []string) error {
	if ledgerID == "" || ledgerStarted == "" || ledgerStatus == "" {
		return fmt.Errorf("run-ledger: --id, --started, and --status are required")
	}

	rec := lineupapi.RunDetail{
		Run: lineupapi.Run{
			ID:        ledgerID,
			Command:   ledgerCommand,
			Status:    ledgerStatus,
			StartedAt: ledgerStarted,
			EndedAt:   ledgerEnded,
			Trigger:   ledgerTrigger,
		},
	}
	if ledgerExitCode >= 0 {
		ec := ledgerExitCode
		rec.ExitCode = &ec
	}
	if ledgerStatus == "FAILED" && ledgerLogFile != "" {
		rec.LogTail = tailFile(ledgerLogFile, 50, 8000)
	}

	var w runWriter
	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		s, err := s3lineup.NewRuns(context.Background(), bucket, "runledger/")
		if err != nil {
			return err
		}
		w = s
	} else {
		w = lineupapi.NewFileRunStore(".lineup/runs")
	}
	return w.PutRun(context.Background(), rec)
}

// tailFile returns up to the last maxLines lines of path, capped at maxBytes
// characters (keeping the most recent), or "" on any read error.
func tailFile(path string, maxLines, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	return out
}

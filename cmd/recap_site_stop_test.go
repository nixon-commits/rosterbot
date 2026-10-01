package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/recap"
	"github.com/spf13/cobra"
)

func stopDay() time.Time { return time.Date(2027, 2, 15, 0, 0, 0, 0, time.UTC) }

// Nothing rendered yet is a clean stop, shaped exactly like the season gate's:
// exit 0, off_season on the ledger row so the dashboard can tell "did nothing,
// correctly" from "did the work", and cobra silenced so a no-op does not print
// a usage dump. Without this, year-round recap-site pages the operator every
// Monday of the next pre-season.
func TestRecapSiteStop_NoCompletedWeeksIsACleanStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(runOutcomeFileEnv, path)
	c := &cobra.Command{Use: "recap-site"}

	got := recapSiteStop(c, fmt.Errorf("site: %w", recap.ErrNoCompletedWeeks), stopDay())

	if !errors.Is(got, errOffSeason) {
		t.Fatalf("err = %v, want it to satisfy errors.Is(err, errOffSeason)", got)
	}
	if code := exitCodeFor(got); code != 0 {
		t.Errorf("exit code = %d, want 0 — a pre-season run must not page", code)
	}
	if !c.SilenceUsage || !c.SilenceErrors {
		t.Error("cobra not silenced; a clean stop would print the error and a usage dump")
	}
	outcome, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no run outcome written: %v", err)
	}
	if string(outcome) != lineupapi.RunOutcomeOffSeason {
		t.Errorf("outcome = %q, want %q", outcome, lineupapi.RunOutcomeOffSeason)
	}
}

// Every other failure is still a failure: a real error must reach the operator
// untouched, and must not leave an off_season outcome claiming the run was a
// correct no-op.
func TestRecapSiteStop_OtherErrorsPassThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(runOutcomeFileEnv, path)
	c := &cobra.Command{Use: "recap-site"}
	boom := errors.New("standings: 503")

	if got := recapSiteStop(c, boom, stopDay()); !errors.Is(got, boom) {
		t.Fatalf("err = %v, want the original error", got)
	}
	if code := exitCodeFor(recapSiteStop(c, boom, stopDay())); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Error("a real failure wrote an off_season outcome; only the empty-week stop may")
	}
	if got := recapSiteStop(c, nil, stopDay()); got != nil {
		t.Errorf("err = %v, want nil for a successful build", got)
	}
}

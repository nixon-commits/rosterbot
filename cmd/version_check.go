package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/fantrax"
	"github.com/pmurley/go-fantrax/auth_client"
	"github.com/spf13/cobra"
)

var versionCheckCmd = &cobra.Command{
	Use:   "version-check",
	Short: "Check whether the pinned Fantrax API version still passes Fantrax's gate",
	Long: `Probes Fantrax with the client version this binary pins, unauthenticated.

Exits non-zero ONLY when Fantrax rejects the pin outright (STALE_CLIENT), which
is the one condition the probe can positively identify. An unrecognized response
or a transport failure exits 0 with a warning: a Fantrax outage already fails
every other scheduled job and pages through those, and this probe must not raise
a second alert for a cause it did not diagnose.

A non-zero exit is written to the run ledger as FAILED, which the opsnotify
Lambda turns into a Pushover on the first failure, quoting the last log line.`,
	RunE: runVersionCheck,
}

func init() {
	rootCmd.AddCommand(versionCheckCmd)
}

// versionCheckResultLine maps a probe outcome to the line to print and whether
// the command should fail. Split out from runVersionCheck so the exit policy is
// testable without a network or a cobra harness.
//
// Returning a non-nil error is what makes entrypoint.sh record the run as
// FAILED; opsalert.Streak then fires on the first failure and quotes the last
// non-empty log line, so the error text IS the alert body.
func versionCheckResultLine(status fantrax.VersionStatus, code string, err error) (string, error) {
	switch {
	case err != nil:
		return fmt.Sprintf("⚠ version probe inconclusive: %v (pinned %s unverified; not failing the run)",
			err, auth_client.APIVersion), nil
	case status == fantrax.VersionStale:
		return "", fmt.Errorf("STALE_CLIENT: Fantrax rejected pinned API version %s — bump auth_client.APIVersion in the go-fantrax fork (runbook is the doc comment on that constant)",
			auth_client.APIVersion)
	case status == fantrax.VersionUnknown:
		return fmt.Sprintf("⚠ version probe inconclusive: unrecognized pageError code %q (pinned %s unverified; not failing the run)",
			code, auth_client.APIVersion), nil
	default:
		return fmt.Sprintf("Fantrax API version %s OK (gate passed, response code %q)",
			auth_client.APIVersion, code), nil
	}
}

func runVersionCheck(cmd *cobra.Command, args []string) error {
	// No initApp: the probe is deliberately unauthenticated and needs no config,
	// so it keeps working even when credentials or the session cache are broken.
	status, code, err := fantrax.CheckAPIVersion(context.Background())

	line, exitErr := versionCheckResultLine(status, code, err)
	if exitErr != nil {
		// This print is redundant, not load-bearing: rootCmd sets neither
		// SilenceErrors nor SilenceUsage, so on a RunE error cobra already
		// prints "Error: <err>" plus a usage dump to stderr once this RunE
		// returns, and cmd/root.go's Execute() reprints the raw error again
		// after rootCmd.Execute() returns — that final reprint, not this one,
		// is what guarantees the diagnosis lands as the last non-empty line,
		// which is what opsalert quotes into the Pushover.
		fmt.Fprintln(os.Stderr, exitErr)
		return exitErr
	}
	fmt.Println(line)
	return nil
}

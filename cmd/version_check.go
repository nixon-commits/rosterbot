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

Exits non-zero when Fantrax rejects the pin outright (STALE_CLIENT), which is
the one condition the probe can positively identify. An unrecognized response
or a transport failure exits 0 with a warning: a Fantrax outage already fails
every other scheduled job and pages through those, and this probe must not raise
a second alert for a cause it did not diagnose.

When the pin reads OK, a second probe sends a deliberately obsolete version as a
positive control. A working gate must reject it; if it does not, the gate has
stopped discriminating and every "version OK" this job reports is meaningless,
so that also exits non-zero. Without it a broken probe reads green forever and
no heartbeat can tell, because the job does launch and does exit 0.

A non-zero exit is written to the run ledger as FAILED, which the opsnotify
Lambda turns into a Pushover on the first failure, quoting the last log line.`,
	RunE: runVersionCheck,
}

func init() {
	rootCmd.AddCommand(versionCheckCmd)
}

// probe is one gate probe's outcome, named so the exit policy can be stated
// over two of them rather than over five loose parameters.
type probe struct {
	status fantrax.VersionStatus
	code   string
	err    error
}

// versionCheckResultLine maps the probe outcomes to the line to print and
// whether the command should fail. Split out from runVersionCheck so the exit
// policy is testable without a network or a cobra harness.
//
// Returning a non-nil error is what makes entrypoint.sh record the run as
// FAILED; opsalert.Streak then fires on the first failure and quotes the last
// non-empty log line, so the error text IS the alert body.
//
// control is the positive-control probe (fantrax.ControlVersion), run only when
// the pinned version reads OK, and nil when it was not run. It exists because a
// green reading from this job is only worth anything if the job can still go
// red: if Fantrax drops or reworks its version gate, the pinned probe keeps
// answering "OK" forever and the check silently becomes a no-op that no
// heartbeat can detect, since it does launch and does exit 0 (rosterbot-0a1).
// A control that stops returning STALE_CLIENT means exactly that, so it is the
// one additional condition worth failing on.
func versionCheckResultLine(pinned probe, control *probe) (string, error) {
	switch {
	case pinned.err != nil:
		// An unreachable Fantrax cannot prove the pin stale, so failing here
		// would page for someone else's outage. The error rides into the
		// returned line, so this degrades to noise rather than to silence.
		//nolint:nilerr // deliberate soft-fail; see the doc comment above on what a non-nil error costs
		return fmt.Sprintf("⚠ version probe inconclusive: %v (pinned %s unverified; not failing the run)",
			pinned.err, auth_client.APIVersion), nil
	case pinned.status == fantrax.VersionStale:
		return "", fmt.Errorf("STALE_CLIENT: Fantrax rejected pinned API version %s — bump auth_client.APIVersion in the go-fantrax fork (runbook is the doc comment on that constant)",
			auth_client.APIVersion)
	case pinned.status == fantrax.VersionUnknown:
		return fmt.Sprintf("⚠ version probe inconclusive: unrecognized pageError code %q (pinned %s unverified; not failing the run)",
			probeCode(pinned), auth_client.APIVersion), nil
	}

	okLine := fmt.Sprintf("Fantrax API version %s OK (gate passed, response code %q)",
		auth_client.APIVersion, pinned.code)

	switch {
	case control == nil:
		return okLine, nil
	case control.err != nil:
		// Transport failure on the control alone. The gate answered the pinned
		// probe a moment ago, so this is a blip, not a broken mechanism —
		// warning only, on the same rule that keeps a Fantrax outage silent.
		//nolint:nilerr // deliberate soft-fail; only PROBE_BLIND below is worth failing on
		return okLine + fmt.Sprintf("\n⚠ control probe inconclusive: %v (probe self-check skipped)", control.err), nil
	case control.status == fantrax.VersionStale:
		return okLine + fmt.Sprintf(" · control %s correctly rejected", fantrax.ControlVersion), nil
	default:
		return "", fmt.Errorf("PROBE_BLIND: control version %s returned %q instead of STALE_CLIENT — Fantrax's version gate no longer rejects an obsolete client, so this job's \"version OK\" readings are meaningless (pinned %s also read OK). Fix the probe before trusting it again",
			fantrax.ControlVersion, probeCode(*control), auth_client.APIVersion)
	}
}

// probeCode renders a probe's pageError code for a message, naming the empty case
// rather than printing "" — an absent code and an unrecognized one are
// different diagnoses and the message is the alert body.
func probeCode(p probe) string {
	if p.code == "" {
		return "(no pageError)"
	}
	return p.code
}

func runVersionCheck(cmd *cobra.Command, args []string) error {
	// No initApp: the probe is deliberately unauthenticated and needs no config,
	// so it keeps working even when credentials or the session cache are broken.
	ctx := context.Background()

	status, code, err := fantrax.CheckAPIVersion(ctx)
	pinned := probe{status: status, code: code, err: err}

	// The control runs only when the pinned probe read OK. That is the only
	// reading whose trustworthiness is in question — a STALE_CLIENT or an
	// inconclusive result has already told us what it can, and probing twice
	// during a Fantrax outage just doubles the load for no extra information.
	var control *probe
	if err == nil && status == fantrax.VersionOK {
		cs, cc, cerr := fantrax.CheckControlVersion(ctx)
		control = &probe{status: cs, code: cc, err: cerr}
	}

	line, exitErr := versionCheckResultLine(pinned, control)
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

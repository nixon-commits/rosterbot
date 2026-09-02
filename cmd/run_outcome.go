package cmd

import "os"

// runOutcomeFileEnv is the env var name entrypoint.sh uses for the
// RUN_OUTCOME_FILE side channel. It is a Go constant, rather than a literal
// inlined at each use, so run_outcome_test.go's shell-parsing guard (modelled
// on TestEntrypointRunIDIsTheLedgerID) can pin that entrypoint.sh's terminal
// run-ledger invocation reads the outcome from the exact same env var name
// this file writes it under — the join key across sh and Go must not drift.
const runOutcomeFileEnv = "RUN_OUTCOME_FILE"

// recordRunOutcome leaves a process-level verdict about THIS run for
// entrypoint.sh's terminal run-ledger write to pick up (see
// lineupapi.RunOutcomeTenantActionable and RUN_OUTCOME_FILE in entrypoint.sh).
// A bare exit status cannot carry it: an exit-0 connect that demoted a tenant
// to needs_reconnect is indistinguishable, on the ledger row alone, from one
// that actually verified — this file is the other channel.
//
// It is a silent no-op when RUN_OUTCOME_FILE is unset, which is every local
// run: entrypoint.sh only sets it inside the container, so nothing here
// should ever fail a `go run . connect` on a laptop. A write failure is
// likewise swallowed — the file is a best-effort side channel to a ledger
// write that itself never blocks the run, the same posture as connect's other
// soft failures (a cookie cache that could not be written, a feed that could
// not be opened).
func recordRunOutcome(outcome string) {
	path := os.Getenv(runOutcomeFileEnv)
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(outcome), 0o644)
}

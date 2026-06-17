package lineupapi

import (
	"context"
	"sort"
	"strings"
)

// jobCommands is the allowlist of triggerable jobs -> the rosterbot CLI command
// each runs. Mirrors the EventBridge schedules in infra/. A POST to a name not
// in this map is a 400. Jobs run for real (the user opted into full power):
// optimize applies the lineup, waivers/claims/transactions send Pushover.
var jobCommands = map[string][]string{
	"optimize":     {"optimize", "--matchup"},
	"waivers":      {"waivers"},
	"prospects":    {"prospects"},
	"claims":       {"claims"},
	"gs-check":     {"gs-check"},
	"transactions": {"transactions"},
	"recap-site":   {"recap-site", "--out", "dist"},
	"backtest":     {"backtest"},
	"grade":        {"grade"},
}

// JobCommand returns the CLI args for a job name and whether it is allowed.
func JobCommand(name string) ([]string, bool) {
	args, ok := jobCommands[name]
	return args, ok
}

// JobNames returns the sorted allowlist (handy for error messages / discovery).
func JobNames() []string {
	names := make([]string, 0, len(jobCommands))
	for n := range jobCommands {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RunStore is the read side of the run ledger (GET /v1/runs, /v1/runs/{id}).
// List returns up to limit runs, newest first. Get returns one run by id;
// ok=false means not found (404).
type RunStore interface {
	List(ctx context.Context, limit int) ([]Run, error)
	Get(ctx context.Context, id string) (*RunDetail, bool, error)
}

// JobRunner launches a job asynchronously (ECS RunTask) and returns the run id
// (the ECS task id) so the caller can poll the ledger for it.
type JobRunner interface {
	Run(ctx context.Context, command []string) (id string, err error)
}

// commandString renders args for display, e.g. {"optimize","--matchup"} ->
// "optimize --matchup".
func commandString(args []string) string { return strings.Join(args, " ") }

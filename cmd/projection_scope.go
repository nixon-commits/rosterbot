package cmd

import (
	"fmt"
	"os"
)

// projectionScope selects which half of projection-site's work a run does.
//
// The command produces two unrelated kinds of artifact and that is why it had
// to be split: the three PRIVATE reports (model, gap, views) describe one
// manager's own performance and belong under reports/user=<uid>/, while
// value.json and football.json are league-wide standings published to a public
// prefix.
//
// Kept as one command with a flag rather than two commands, because the two
// halves share the whole read path — the Analysis Store, the season floor, the
// aggregation — and splitting the binary would duplicate it.
type projectionScope string

const (
	// scopeAll is the local-development and manual-run default: do everything,
	// which is what this command did before the split.
	scopeAll projectionScope = "all"

	// scopeTenant renders only the private per-tenant reports. It runs once per
	// tenant under the fan-out.
	scopeTenant projectionScope = "tenant"

	// scopeLeague renders only the league-wide public artifacts. It stays a
	// singleton: value.json and football.json describe every team, so N tenants
	// producing them would be N tasks racing on one key.
	scopeLeague projectionScope = "league"
)

func parseProjectionScope(s string) (projectionScope, error) {
	switch projectionScope(s) {
	case scopeAll, scopeTenant, scopeLeague:
		return projectionScope(s), nil
	}
	// The value comes from an EventBridge rule's static argv, so a typo is a
	// wiring mistake. Defaulting to "all" would make a mistyped per-tenant
	// schedule quietly publish the league-wide artifacts from every tenant at
	// once, each overwriting the last.
	return "", fmt.Errorf("projection-site: unknown --scope %q (want all, tenant or league)", s)
}

// scopeWritesPrivateReports reports whether this run produces model/gap/views.
func scopeWritesPrivateReports(s projectionScope) bool {
	return s == scopeAll || s == scopeTenant
}

// scopeWritesPublicDir reports whether this run produces value.json and
// football.json.
func scopeWritesPublicDir(s projectionScope) bool {
	return s == scopeAll || s == scopeLeague
}

// ensurePublicDir creates the output directory ONLY for a scope that publishes
// into it.
//
// THIS IS A DATA-LOSS GUARD, not tidiness. entrypoint.sh runs `sync up` after
// every command, and publishSite mirrors ./report into DASHBOARD_BUCKET with
// --delete; it no-ops when the directory is ABSENT. So a per-tenant run that
// created an empty ./report would have the sync delete value.json and
// football.json for the whole league — once per tenant, every day.
func ensurePublicDir(s projectionScope, dir string) error {
	if !scopeWritesPublicDir(s) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

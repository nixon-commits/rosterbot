package lineupapi

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// jobRowRe extracts (schedule id, first CLI argv token) from each row of
// infra.go's jobs table. Rows look like:
//
//	{"Lineup", "cron(0 14-23,0-3 * * ? *)", jsii.Strings("optimize", "--matchup", ...), hourlyGap},
//	{"GsCheck", "cron(0 12 * * ? *)", jsii.Strings("gs-check"), dailyGap},
//
// The first jsii.Strings argument is the CLI subcommand (e.g. "optimize",
// "gs-check") — the same string handleJob's path value and jobSpecs are keyed
// on — so it, not the schedule id, is what leagueWideJobs must be checked
// against.
var jobRowRe = regexp.MustCompile(`\{"([A-Za-z]+)",\s*"cron\([^)]*\)",\s*jsii\.Strings\("([a-z][a-z-]*)"`)

// perTenantJobsRe and its key extractor are copied verbatim from
// internal/statestore/layout/producer_test.go, which established this
// pattern first. infra/infra.go is `package main` in a separate Go module
// (bare module path "infra", outside github.com/nixon-commits/rosterbot/)
// that replaces the root module via `../` — so the root module importing it
// back would be a cycle, and nothing lets the dependency run either
// direction. Parsing is the only way to compare the two lists without
// restating one of them here, which is the staleness this test exists to
// catch.
var perTenantJobsRe = regexp.MustCompile(`perTenantJobs\s*:?=\s*map\[string\]bool\{([^}]*)\}`)
var perTenantKeyRe = regexp.MustCompile(`"([^"]+)"\s*:\s*true`)

// TestLeagueWideJobsCoversEveryInfraSingleton is the authz-side twin of
// layout's TestEveryPerTenantArtifactHasAFanningOutProducer: two
// hand-maintained lists — infra.go's schedule table (which jobs fan out per
// tenant) and authz.go's leagueWideJobs (which jobs a member may launch) —
// that must agree, with nothing making them, because leagueWideJobs is a
// DENYLIST. A job absent from it is member-launchable by default, so a new
// schedule added to infra.go and left out of leagueWideJobs doesn't fail
// loudly — it silently opens a league-wide/deployment-wide job (real Fantrax
// writes, a Pushover to everyone, a rebuild of a shared site) to every member
// via POST /v1/jobs/{name}.
//
// Every schedule in infra.go's jobs table that is NOT in perTenantJobs runs
// once, as the operator, against the whole league or deployment — so its CLI
// command must be in leagueWideJobs. A schedule that IS in perTenantJobs
// fans out one task per tenant, acting only on that tenant's own team, so a
// member launching it is fine (and is checked here too: a command that is
// EXCLUSIVELY per-tenant — never also a singleton under another schedule
// name — has no business being in leagueWideJobs, since that would block a
// member from a job that only ever touches their own data).
func TestLeagueWideJobsCoversEveryInfraSingleton(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "infra", "infra.go"))
	if err != nil {
		t.Fatalf("read infra.go: %v", err)
	}

	rows := jobRowRe.FindAllSubmatch(src, -1)
	if len(rows) == 0 {
		t.Fatal("parsed no rows from infra.go's jobs table; jobRowRe no longer matches its " +
			"shape (a guard that matches nothing would pass vacuously) — update jobRowRe to " +
			"the current `{\"ID\", \"cron(...)\", jsii.Strings(\"cmd\", ...), maxGap}` row " +
			"format rather than deleting this test")
	}

	m := perTenantJobsRe.FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find the perTenantJobs map in infra/infra.go — if it was renamed " +
			"or restructured, update perTenantJobsRe rather than deleting this test: it is " +
			"what tells this guard which infra.go schedules are per-tenant fan-outs versus " +
			"league-wide singletons")
	}
	perTenant := map[string]bool{}
	for _, k := range perTenantKeyRe.FindAllSubmatch(m[1], -1) {
		perTenant[string(k[1])] = true
	}
	if len(perTenant) == 0 {
		t.Fatal("parsed the perTenantJobs map but found no entries; the guard would pass vacuously")
	}

	// singleton and exclusivelyPerTenant are keyed by CLI command (jsii.Strings'
	// first argument), not by schedule id: leagueWideJobs and jobSpecs are both
	// keyed by command, and a command like "projection-site" appears under two
	// schedule ids (ProjectionSite the league-wide singleton, ProjectionReports
	// the per-tenant half) that must be judged together, not separately.
	singleton := map[string]bool{}
	perTenantCmd := map[string]bool{}
	for _, row := range rows {
		if perTenant[string(row[1])] {
			perTenantCmd[string(row[2])] = true
		} else {
			singleton[string(row[2])] = true
		}
	}
	exclusivelyPerTenant := map[string]bool{}
	for cmd := range perTenantCmd {
		if !singleton[cmd] {
			exclusivelyPerTenant[cmd] = true
		}
	}

	for cmd := range singleton {
		if !leagueWideJobs[cmd] {
			t.Errorf("infra.go schedules %q to run once, league-wide (not in perTenantJobs), "+
				"but %q is missing from leagueWideJobs in internal/lineupapi/authz.go — "+
				"add %q: true there, or a member can launch it via POST /v1/jobs/%s and it "+
				"will run against the whole league/deployment on their behalf "+
				"(rosterbot-twp1)", cmd, cmd, cmd, cmd)
		}
	}
	for cmd := range exclusivelyPerTenant {
		if leagueWideJobs[cmd] {
			t.Errorf("infra.go only ever schedules %q as a per-tenant fan-out (every row using "+
				"it is in perTenantJobs) — acting solely on the launching tenant's own team — "+
				"but %q is listed in leagueWideJobs in internal/lineupapi/authz.go, which "+
				"blocks every member from launching their own tenant's job. Remove it from "+
				"leagueWideJobs, or if it should stay blocked, add a singleton (non-per-tenant) "+
				"schedule for it in infra.go so the restriction has a reason", cmd, cmd)
		}
	}

	t.Logf("checked %d infra.go schedule rows (%d league-wide, %d per-tenant) against %d "+
		"leagueWideJobs entries", len(rows), len(singleton), len(perTenantCmd), len(leagueWideJobs))
}

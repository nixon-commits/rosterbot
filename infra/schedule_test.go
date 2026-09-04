package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// jobsTableEntryRe matches one row of the jobs table in infra.go, e.g.
//
//	{"Lineup", "cron(0 14-23,0-3 * * ? *)", jsii.Strings("optimize", "--archive-projections"), hourlyGap},
//
// Parsed rather than restated, for the same reason
// internal/statestore/layout/producer_test.go parses infra.go instead of
// hand-copying its schedule table: a restated list is the staleness this
// guard exists to catch, and infra's module path ("infra", outside
// github.com/nixon-commits/rosterbot/) means nothing outside this package can
// import the unexported `jobs` slice anyway.
var jobsTableEntryRe = regexp.MustCompile(`\{"([A-Za-z]+)",\s*"cron\([^)]*\)",\s*jsii\.Strings\(([^)]*)\),\s*\w+\}`)

// jobArgvByID reads infra.go's own source (cwd is this package's directory
// under `go test`) and returns each schedule's argv, keyed by job id.
func jobArgvByID(t *testing.T) map[string][]string {
	t.Helper()
	src, err := os.ReadFile("infra.go")
	if err != nil {
		t.Fatalf("read infra.go: %v", err)
	}
	out := map[string][]string{}
	for _, m := range jobsTableEntryRe.FindAllSubmatch(src, -1) {
		id := string(m[1])
		var argv []string
		for _, a := range strings.Split(string(m[2]), ",") {
			a = strings.TrimSpace(a)
			a = strings.Trim(a, `"`)
			if a != "" {
				argv = append(argv, a)
			}
		}
		out[id] = argv
	}
	if len(out) == 0 {
		t.Fatal("parsed no jobs from infra.go's jobs table; the guard would pass vacuously")
	}
	return out
}

// TestHourlyLineupIsTodayOnly_DailyMatchupPassIsSeparate pins rosterbot-cem's
// design (b): the hourly Lineup schedule optimizes TODAY ONLY (no --matchup),
// and the once-daily pre-write of the rest of the current matchup week runs
// on its own schedule instead.
//
// An hourly cadence can only legitimately learn anything NEW about today
// (late scratches, probables firming up) — re-deciding a future day's lineup
// every hour is pure churn, not responsiveness. Measured from production
// notifications (the bead): only ~16% of hourly lineup notifications
// concerned today; the other ~84% were future-dated --matchup speculation
// that got re-decided the very next hour, with a single future date
// (2026-07-21) flipping Bohm 32 times, Jeffers 29, Ward 29.
func TestHourlyLineupIsTodayOnly_DailyMatchupPassIsSeparate(t *testing.T) {
	argv := jobArgvByID(t)

	lineup, ok := argv["Lineup"]
	if !ok {
		t.Fatal(`no "Lineup" job found in infra.go's jobs table`)
	}
	for _, a := range lineup {
		if a == "--matchup" {
			t.Errorf(`hourly "Lineup" job still carries --matchup (argv = %v) — this reintroduces `+
				"the churn rosterbot-cem removed: an hourly cadence re-deciding a future day's "+
				"lineup every hour with no new information to act on", lineup)
		}
	}

	matchup, ok := argv["LineupMatchup"]
	if !ok {
		t.Fatal(`no "LineupMatchup" job found in infra.go's jobs table — rosterbot-cem's design (b) ` +
			`requires a once-daily schedule to run the --matchup future-day pre-write that the ` +
			`hourly Lineup job no longer does`)
	}
	var hasOptimizeCmd, hasMatchupFlag bool
	for _, a := range matchup {
		switch a {
		case "optimize":
			hasOptimizeCmd = true
		case "--matchup":
			hasMatchupFlag = true
		}
	}
	if !hasOptimizeCmd || !hasMatchupFlag {
		t.Errorf(`"LineupMatchup" job argv = %v, want it to run "optimize" with "--matchup"`, matchup)
	}
}

// perTenantJobsRe extracts the keys of infra.go's perTenantJobs map literal —
// the same pattern internal/statestore/layout/producer_test.go uses to read
// this exact map from outside the module.
var perTenantJobsRe = regexp.MustCompile(`perTenantJobs\s*:=\s*map\[string\]bool\{([^}]*)\}`)
var perTenantKeyRe = regexp.MustCompile(`"([^"]+)"\s*:\s*true`)

// TestLineupMatchupFansOutPerTenant pins that the new LineupMatchup schedule
// is in perTenantJobs. It reads the tenant's own roster and matchup-week
// bounds exactly like the hourly Lineup job it splits off from, so a run as
// the operator singleton would apply someone else's team's lineup, or read
// the wrong tenant's matchup week entirely.
func TestLineupMatchupFansOutPerTenant(t *testing.T) {
	src, err := os.ReadFile("infra.go")
	if err != nil {
		t.Fatalf("read infra.go: %v", err)
	}
	m := perTenantJobsRe.FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find the perTenantJobs map in infra.go")
	}
	fanOut := map[string]bool{}
	for _, k := range perTenantKeyRe.FindAllSubmatch(m[1], -1) {
		fanOut[string(k[1])] = true
	}
	if len(fanOut) == 0 {
		t.Fatal("parsed perTenantJobs but found no entries; the guard would pass vacuously")
	}
	if !fanOut["LineupMatchup"] {
		t.Error(`"LineupMatchup" is not in perTenantJobs — it would run once as the operator, ` +
			"applying the operator's own lineup and matchup week for every tenant")
	}
}

package layout

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// perTenantJobsRe extracts the keys of infra.go's perTenantJobs map literal.
var perTenantJobsRe = regexp.MustCompile(`perTenantJobs\s*:?=\s*map\[string\]bool\{([^}]*)\}`)
var keyRe = regexp.MustCompile(`"([^"]+)"\s*:\s*true`)

// TestEveryPerTenantArtifactHasAFanningOutProducer is a cross-file derivation
// guard, the same class of check as `make check-pins`: two hand-maintained
// lists that must agree, with nothing making them.
//
// All() says which artifacts are written PER TENANT. infra.go's
// perTenantJobs says which schedules actually get a per-tenant task. When an
// artifact is PerTenant but its producer is not in that set, the producer runs
// once — as the operator — and writes only the operator's partition. The read
// side resolves per caller, so every other tenant gets a permanent 404.
//
// That is precisely what happened to layout.Reports: declared PerTenant,
// resolved per caller, produced by projection-site, which was kept out of the
// fan-out because it ALSO published league-wide artifacts. Nothing failed;
// non-operators simply had no model, gap or views report, forever.
//
// IT READS infra.go'S SOURCE rather than importing it, because infra is a
// module whose path is bare "infra" — outside github.com/nixon-commits/
// rosterbot/, so Go's internal-package rule forbids it from importing
// internal/statestore/layout, and the dependency cannot run the other way
// either. Parsing is the only way to compare the two without restating one of
// them here, and a restated list is the failure this test exists to catch.
//
// An artifact with no Producer is skipped: some are written by more than one
// job, and naming one would be a lie rather than a check.
func TestEveryPerTenantArtifactHasAFanningOutProducer(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "infra.go"))
	if err != nil {
		t.Fatalf("read infra.go: %v", err)
	}
	m := perTenantJobsRe.FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find the perTenantJobs map in infra/infra.go — if it was " +
			"renamed or restructured, update this guard rather than deleting it: it is " +
			"what stops a PerTenant artifact being produced by a job that runs once")
	}
	fanOut := map[string]bool{}
	for _, k := range keyRe.FindAllSubmatch(m[1], -1) {
		fanOut[string(k[1])] = true
	}
	if len(fanOut) == 0 {
		t.Fatal("parsed perTenantJobs but found no entries; the guard would pass vacuously")
	}

	for _, a := range All() {
		if !a.PerTenant || a.Producer == "" {
			continue
		}
		if !fanOut[a.Producer] {
			t.Errorf("%s is PerTenant and produced by %q, which is not in infra.go's "+
				"perTenantJobs — that job runs once as the operator, so only the "+
				"operator's partition is ever written and every other tenant reads a "+
				"permanent 404. Either fan the job out, or split it so the per-tenant "+
				"half has its own schedule.",
				a.Name, a.Producer)
		}
	}
	t.Logf("checked %d fanning-out jobs against %d artifacts", len(fanOut), len(All()))
}

// jobIDRe extracts the id of each entry in infra.go's jobs table, whose rows
// look like {"Lineup", "cron(...)", jsii.Strings(...), hourlyGap}.
var jobIDRe = regexp.MustCompile(`\{"([A-Za-z]+)",\s*"cron\(`)

// scheduleIDsFromInfra returns every EventBridge schedule id declared in
// infra/infra.go.
//
// Parsed rather than imported: infra's module path is bare "infra", outside
// github.com/nixon-commits/rosterbot/, so Go's internal-package rule forbids it
// from importing this package, and nothing lets the dependency run the other
// way. The alternative is a hand-copied list, which is exactly the staleness
// this replaces.
func scheduleIDsFromInfra(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "infra.go"))
	if err != nil {
		t.Fatalf("read infra.go: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range jobIDRe.FindAllSubmatch(src, -1) {
		ids[string(m[1])] = true
	}
	if len(ids) == 0 {
		t.Fatal("parsed no schedules from infra.go's jobs table; the guard would pass vacuously")
	}
	return ids
}

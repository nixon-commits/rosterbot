package jobwire

import (
	"os/exec"
	"strings"
	"testing"
)

// TestProducersDoNotImportLineupapi pins the dependency direction this
// package exists for: the five job packages publish their results through
// jobwire and must not import internal/lineupapi directly — its
// WebAuthn/session/admin surface has nothing to do with a waiver report.
// Before jobwire, each of them imported it solely for its DTO structs and
// RecordOutput; this test is what keeps that from quietly coming back.
//
// DIRECT imports only, deliberately. All five producers still reach
// lineupapi TRANSITIVELY through internal/notify, whose feed and APNs sinks
// import it by documented design (the same coupling that forced
// internal/pushover out as a stdlib-only leaf). That edge is notify's to
// own; asserting the full dep graph here would hold jobwire's guard hostage
// to a decision made in a different package.
func TestProducersDoNotImportLineupapi(t *testing.T) {
	const module = "github.com/nixon-commits/rosterbot"
	const forbidden = module + "/internal/lineupapi"

	producers := []string{
		module + "/internal/prospects",
		module + "/internal/transactions",
		module + "/internal/waivers",
		module + "/internal/gscheck",
		module + "/internal/claims",
	}

	for _, pkg := range producers {
		out, err := exec.CommandContext(t.Context(), "go", "list", "-f", `{{join .Imports "\n"}}`, pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", pkg, err, out)
		}
		for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			// Exact match: jobwire's own path shares the lineupapi/ segment
			// and is exempt — it is the leaf both sides are allowed to share.
			if strings.TrimSpace(dep) == forbidden {
				t.Errorf("%s imports %s directly — job packages publish through jobwire only", pkg, forbidden)
			}
		}
	}
}

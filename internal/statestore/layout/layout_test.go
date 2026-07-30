package layout

import (
	"strings"
	"testing"
)

// Every artifact the bot writes must be enumerable, or the Infra status page
// silently omits it — which is the exact failure mode that page exists to catch.
func TestAll_CoversEveryKnownPrefix(t *testing.T) {
	want := []string{
		"cache/",
		"analysis/grades/",
		"analysis/team-values/",
		"analysis/lineup-gaps/",
		"runledger/",
		"runs/",
		"notifications/",
		"lineup/",
		"archive/",
		"backtest/",
		"claims/",
		"session/",
	}
	got := map[string]bool{}
	for _, a := range All() {
		got[a.S3Prefix] = true
	}
	for _, p := range want {
		if !got[p] {
			t.Errorf("prefix %q is not in All() — it would be invisible on the Infra page", p)
		}
	}
}

func TestAll_PrefixesAreWellFormed(t *testing.T) {
	for _, a := range All() {
		if a.S3Prefix == "" {
			t.Errorf("%s: empty S3Prefix", a.Name)
		}
		if !strings.HasSuffix(a.S3Prefix, "/") {
			t.Errorf("%s: S3Prefix %q must end in / so it can't match a sibling by accident", a.Name, a.S3Prefix)
		}
		if strings.HasPrefix(a.S3Prefix, "/") {
			t.Errorf("%s: S3Prefix %q must be relative to the bucket root", a.Name, a.S3Prefix)
		}
		if a.Name == "" {
			t.Errorf("%s: needs a human-readable Name for the status page", a.S3Prefix)
		}
	}
}

// Each artifact must declare how fresh it is expected to be, or the page cannot
// render a verdict. Ephemeral data is exempt: cache/ is TTL-evicted and its
// age is not a health signal.
func TestAll_DurableArtifactsDeclareAMaxAge(t *testing.T) {
	for _, a := range All() {
		if a.Durable && a.MaxAge == 0 {
			t.Errorf("%s: durable artifact has no MaxAge — the status page can't judge it", a.Name)
		}
	}
}

// Producer names must match real EventBridge schedules; a typo here sends the
// reader hunting for a job that doesn't exist.
func TestAll_ProducersAreRealSchedules(t *testing.T) {
	valid := map[string]bool{
		"Lineup": true, "Prospects": true, "GsCheck": true, "Waivers": true,
		"Transactions": true, "Claims": true, "Backtest": true, "Grade": true,
		"ProjectionSite": true, "Archive": true, "TeamValues": true, "Shadow": true,
		"": true, // no single producer (e.g. session/, written by every task)
	}
	for _, a := range All() {
		if !valid[a.Producer] {
			t.Errorf("%s: Producer %q is not a known EventBridge schedule", a.Name, a.Producer)
		}
	}
}

// The Team Value Store is the one artifact where a missing day is permanent
// (docs/adr/0002: HKB has no history, rosters aren't archived). The status page
// keys its gap detection off this flag, so it must not be silently dropped.
func TestTeamValues_IsFlaggedUnbackfillable(t *testing.T) {
	for _, a := range All() {
		if a.S3Prefix == "analysis/team-values/" {
			if !a.NoBackfill {
				t.Error("team-values must be flagged NoBackfill — a gap there is unrecoverable data loss")
			}
			if !a.Partitioned {
				t.Error("team-values is dt-partitioned; gap detection needs that flag")
			}
			return
		}
	}
	t.Fatal("team-values artifact missing from All()")
}

// The Lineup Gap Store IS backfillable, unlike Team Values — past-period roster
// snapshots are immutable and cached, so a missed day is recoverable with
// `grade --dates`. Flagging it NoBackfill would make the Infra page rank a
// recoverable gap as permanent data loss.
func TestLineupGaps_IsBackfillable(t *testing.T) {
	for _, a := range All() {
		if a.S3Prefix == "analysis/lineup-gaps/" {
			if a.NoBackfill {
				t.Error("lineup-gaps is recoverable via `grade --dates`; it must not be flagged NoBackfill")
			}
			if !a.Partitioned {
				t.Error("lineup-gaps is dt-partitioned; gap detection needs that flag")
			}
			return
		}
	}
	t.Fatal("lineup-gaps artifact missing from All()")
}

func TestCache_IsEphemeral(t *testing.T) {
	for _, a := range All() {
		if a.S3Prefix == "cache/" {
			if a.Durable {
				t.Error("cache/ is TTL-evicted and regenerable — it must not be marked Durable")
			}
			return
		}
	}
	t.Fatal("cache artifact missing from All()")
}

// Every artifact must name its on-disk location, so `serve`'s file-backed
// lister can show the same view locally as the Lambda shows against S3.
func TestAll_DeclareALocalDir(t *testing.T) {
	for _, a := range All() {
		if a.LocalDir == "" {
			t.Errorf("%s: no LocalDir — it would be invisible in local serve", a.Name)
		}
	}
}

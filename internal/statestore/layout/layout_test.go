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
// TestAll_ProducersAreRealSchedules now READS infra.go's schedule table rather
// than restating it.
//
// It held a hand-copied list of schedule names, which is the shape of guard
// this repo keeps getting bitten by: it went stale the moment a schedule was
// added, and its failure said "unknown schedule" about a schedule that plainly
// exists. Parsing the real table means adding a schedule cannot break it and
// deleting one cannot silently pass it.
//
// See scheduleIDsFromInfra for why parsing is the only option across this
// module boundary.
func TestAll_ProducersAreRealSchedules(t *testing.T) {
	valid := scheduleIDsFromInfra(t)
	valid[""] = true // no single producer (e.g. session/, written by every task)
	for _, a := range All() {
		if !valid[a.Producer] {
			t.Errorf("%s: Producer %q is not a schedule in infra.go's jobs table",
				a.Name, a.Producer)
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

// The Daily Archive is the second artifact whose gaps are permanent, and the
// flag is the only thing that says so. Its sources — HKB's rankings, the four
// FanGraphs rest-of-season feeds, the Savant leaderboards, the prospect board —
// are point-in-time reads with no historical replay endpoint, so a day nobody
// archived is gone. Clearing this flag would put those days back under the
// Infra page's "Re-runnable" footer, telling an operator to re-run a job that
// structurally cannot recover them (rosterbot-0l31).
func TestArchive_IsFlaggedUnbackfillable(t *testing.T) {
	for _, a := range All() {
		if a.S3Prefix == "archive/" {
			if !a.NoBackfill {
				t.Error("archive/ must be flagged NoBackfill — its upstreams publish no history, " +
					"so a missing day cannot be re-fetched, only faked with today's data")
			}
			if !a.Partitioned {
				t.Error("archive/ is dt-partitioned; gap detection needs that flag")
			}
			return
		}
	}
	t.Fatal("archive artifact missing from All()")
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

// The Football Trade Log's four defining choices, pinned because three of them
// are exclusions — and an exclusion is invisible: nothing fails when it silently
// stops holding.
//
// It is absent from All(), so NONE of the guards above see it. That is the same
// position FootballTrades and ILStarts are in, and it is why this test exists at
// all rather than relying on the table walks.
func TestFootballTradeLog_LayoutChoicesArePinned(t *testing.T) {
	a := FootballTradeLog

	// 1. Its own prefix, nested under the marker prefix. Safe only because the
	//    marker store is a BlobStore, which Gets and Publishes exact keys and
	//    never lists — so it can neither see nor collide with a dt= partition.
	if a.S3Prefix != "football/trades/log/" {
		t.Errorf("S3Prefix = %q, want football/trades/log/", a.S3Prefix)
	}
	if !strings.HasPrefix(a.S3Prefix, FootballTrades.S3Prefix) {
		t.Errorf("S3Prefix %q no longer nests under the marker prefix %q", a.S3Prefix, FootballTrades.S3Prefix)
	}
	if a.LocalDir != ".football/trades/log" {
		t.Errorf("LocalDir = %q, want .football/trades/log (covered by .gitignore's .football/)", a.LocalDir)
	}

	// 2. No MaxAge and absent from All(). A partition is written only by a poll
	//    that graded something, so a quiet league writes nothing for weeks and
	//    an age check would read the normal case as stale.
	if a.MaxAge != 0 {
		t.Errorf("MaxAge = %v, want 0 — a trade-less week is normal, not stale", a.MaxAge)
	}
	for _, in := range All() {
		if in.S3Prefix == a.S3Prefix {
			t.Error("FootballTradeLog is in All(); the Infra page would gap-scan a series that is " +
				"legitimately empty most weeks, and TestAll_DurableArtifactsDeclareAMaxAge would now fail")
		}
	}

	// 3. NOT NoBackfill — but not fully recoverable either. Sleeper keeps
	//    completed trades forever so a missing row's IDENTITY is re-derivable;
	//    StatsGuy has no history so its GRADE is not. --relog rebuilds the row
	//    at today's prices and marks it regraded rather than pretending.
	if a.NoBackfill {
		t.Error("NoBackfill = true; a missing day is re-derivable from Sleeper, unlike the Team Value Store")
	}

	// 4. League-wide, like every other football artifact: one Sleeper league,
	//    not one per tenant.
	if a.PerTenant {
		t.Error("PerTenant = true; the football league is shared, so a tenant segment would fragment one league's log")
	}
}

// The stale-cache marker's layout choices, pinned for the same reason the
// Football Trade Log's are: it is absent from All(), so none of the table walks
// above see it, and every one of these choices is an exclusion that fails
// silently if it stops holding.
func TestStaleCacheAlerts_LayoutChoicesArePinned(t *testing.T) {
	a := StaleCacheAlerts

	if a.S3Prefix != "alerts/stale-cache/" {
		t.Errorf("S3Prefix = %q, want alerts/stale-cache/", a.S3Prefix)
	}
	if a.S3Prefix == ILStarts.S3Prefix {
		t.Errorf("shares a prefix with the IL-start markers; one would overwrite the other")
	}
	if a.LocalDir != ".alerts/stale-cache" {
		t.Errorf("LocalDir = %q, want .alerts/stale-cache", a.LocalDir)
	}

	// NOT PerTenant. The cache it guards is a league-wide singleton fetched
	// once per day, so the outage it reports is shared. Tenant-scoping it would
	// let N tenants each re-announce the one upstream failure — which is the
	// flood this marker exists to stop, reintroduced one level down.
	if a.PerTenant {
		t.Error("PerTenant: a shared cache's outage must not alert once per tenant")
	}

	// No MaxAge, and absent from All(). A marker exists only while something is
	// wrong, so a healthy deployment writes nothing for months and an age check
	// would read that silence as staleness.
	if a.MaxAge != 0 {
		t.Errorf("MaxAge = %v, want 0 — no markers is the healthy case, not a stale one", a.MaxAge)
	}
	for _, listed := range All() {
		if listed.S3Prefix == a.S3Prefix {
			t.Error("in All(): the status page would show a healthy deployment as missing")
		}
	}
}

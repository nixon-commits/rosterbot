package statestore

import (
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// TestEmptyTenantIsByteIdenticalToTheLegacyLayout is the migration gate.
//
// Every deployed object today sits at the un-segmented prefix. If an empty
// tenant produced anything else — even a trailing "user=/" — the running
// deployment would silently start reading an empty bucket: no error, no missing
// file, just a dashboard with nothing in it and jobs that think they have never
// run. Empty is a real mode, not a missing value.
func TestEmptyTenantIsByteIdenticalToTheLegacyLayout(t *testing.T) {
	s := New("bucket")
	for _, a := range []struct {
		name string
		art  artifact
	}{
		{"lineup", lineupArtifact},
		{"runledger", runLedgerArtifact},
		{"analysis", analysisArtifact},
		{"reports", reportsArtifact},
		{"cache", cacheArtifact},
		{"teamvalues", teamValueArtifact},
	} {
		if got, want := s.prefixFor(a.art), a.art.s3Prefix; got != want {
			t.Errorf("%s: prefixFor with no tenant = %q, want %q — an un-migrated "+
				"deployment would read an empty prefix and report itself as never having run",
				a.name, got, want)
		}
		if got, want := s.dirFor(a.art), a.art.localDir; got != want {
			t.Errorf("%s: dirFor with no tenant = %q, want %q", a.name, got, want)
		}
	}
}

func TestPerTenantArtifactsGainTheSegment(t *testing.T) {
	s := ForTenant("bucket", "u123")
	cases := map[string]struct {
		art  artifact
		want string
	}{
		"lineup":    {lineupArtifact, "lineup/user=u123/"},
		"runledger": {runLedgerArtifact, "runledger/user=u123/"},
		"reports":   {reportsArtifact, "reports/user=u123/"},
		"analysis":  {analysisArtifact, "analysis/grades/user=u123/"},
		// backtest arrived here in rosterbot-iqso, when projection snapshots
		// moved off cmd/sync.go's bulk directory sync onto SnapshotStore. The
		// sync's own test used to assert this scoping; it has to be asserted
		// somewhere, because the concern it protects against — one tenant's
		// snapshots grading against another's — did not go away with the sync.
		"backtest": {backtestArtifact, "backtest/user=u123/"},
	}
	for name, c := range cases {
		if got := s.prefixFor(c.art); got != c.want {
			t.Errorf("%s: prefixFor = %q, want %q", name, got, c.want)
		}
	}
}

// TestSharedArtifactsNeverGainTheSegment is the other half, and the one that
// costs money to get wrong.
//
// The TTL Cache holds FanGraphs projections, Savant CSVs and the MLB schedule —
// identical for every tenant. Segmenting it would multiply the request load on
// those upstreams by the tenant count, which is how an IP gets blocked and every
// tenant's jobs fail at once. The league-wide value stores describe all teams,
// so splitting them per tenant would fragment data that is about the league.
func TestSharedArtifactsNeverGainTheSegment(t *testing.T) {
	s := ForTenant("bucket", "u123")
	for name, a := range map[string]artifact{
		"cache":            cacheArtifact,
		"teamvalues":       teamValueArtifact,
		"tradevalues":      tradeValuesArtifact,
		"footballvalues":   footballValueArtifact,
		"footballtrades":   footballTradesArtifact,
		"footballtradelog": footballTradeLogArtifact,
	} {
		if got := s.prefixFor(a); got != a.s3Prefix {
			t.Errorf("%s: shared artifact gained a tenant segment (%q); the cache in "+
				"particular would then multiply upstream load by the tenant count",
				name, got)
		}
	}
}

// TestSessionIsPerTenant pins the single most dangerous artifact in the table.
//
// session/ holds the Fantrax auth cookie. auth_client.CacheFile is a package
// CONST, so every task writes the same local path — safe only because each task
// is its own container. If two tenants ever shared the session/ prefix, tenant
// B's task would sync down tenant A's cookie and authenticate AS A: not a data
// leak, a wrong-roster write against someone else's team.
func TestSessionIsPerTenant(t *testing.T) {
	if !layout.Session.PerTenant {
		t.Fatal("layout.Session is not PerTenant. Sharing it means one tenant's job " +
			"authenticates to Fantrax as another tenant and edits their roster")
	}
}

// TestPerTenantSplitIsDeliberate walks the whole table so a newly added artifact
// cannot default into either side unnoticed.
//
// The rule is WHO THE DATA IS ABOUT, not who produces it: Grade writes both the
// Analysis Store (one manager's projections — per-tenant) and, on the same run,
// nothing shared; TeamValues writes a league-wide table from one job. Sharing
// something tenant-specific is a cross-tenant leak; splitting something
// league-wide merely duplicates it. The directions are not equally bad, which is
// why this asserts the exact expected set rather than a count.
func TestPerTenantSplitIsDeliberate(t *testing.T) {
	wantPerTenant := map[string]bool{
		"Analysis Store": true, "Lineup Gap Store": true, "Projection Snapshots": true,
		"Run Ledger": true, "Run Output": true, "Notifications": true,
		"Published Lineup": true, "Pending Offers": true, "Trade Offer Log": true,
		"Private Dashboard Reports": true, "Fantrax Session": true, "Run Progress": true,
	}
	all := append(layout.All(), layout.Progress, layout.FootballTrades, layout.FootballTradeLog)
	for _, a := range all {
		if got, want := a.PerTenant, wantPerTenant[a.Name]; got != want {
			t.Errorf("%q PerTenant = %v, want %v — adding an artifact must be a "+
				"deliberate choice on this axis, because sharing tenant data leaks it "+
				"and the failure is silent", a.Name, got, want)
		}
	}
}

// TestProducerAndReaderComposeIdenticalPrefixes is the guard against the one
// failure mode of splitting this rule across two consumers.
//
// internal/statestore builds the PRODUCERS' stores; the API Lambda builds the
// READERS'. If those two ever disagree about how a tenant segment is composed,
// jobs write to one location and the dashboard reads another — and nothing
// fails. Every job reports success, every prefix looks healthy to its own
// writer, and the dashboard is simply empty.
//
// Both now go through layout.Artifact.PrefixFor. This asserts they agree for
// every artifact and for both the tenant and no-tenant cases, so a future
// "optimisation" that reintroduces a local copy in either place fails here.
func TestProducerAndReaderComposeIdenticalPrefixes(t *testing.T) {
	for _, tenant := range []string{"", "u123"} {
		sel := ForTenant("bucket", tenant)
		for _, a := range append(layout.All(), layout.Progress) {
			// The producer path, as statestore composes it.
			producer := sel.prefixFor(artifact{name: a.Name, s3Prefix: a.S3Prefix, localDir: a.LocalDir, perTenant: a.PerTenant})
			// The reader path, as lambda/main.go composes it.
			reader := a.PrefixFor(tenant)
			if producer != reader {
				t.Errorf("tenant=%q %s: producer writes %q, reader reads %q — jobs and "+
					"dashboard would disagree with no error on either side",
					tenant, a.Name, producer, reader)
			}
		}
	}
}

// TestRuntimePrefixesStartWithTheLayoutPrefix closes the axis that let the
// Analysis Store break in production (rosterbot-crq.11).
//
// TestProducerAndReaderComposeIdenticalPrefixes passed throughout that outage,
// because statestore and the API Lambda genuinely agreed — they agreed on the
// WRONG path. The drift was between both of them and the layout declaration,
// which is what the backfill, the Infra page's gap scan and the Athena partition
// template all key off.
//
// The Analysis Store was the only artifact whose prefix was split across two
// files: statestore rooted the store at "analysis/" and internal/analysis
// appended "grades/". Untenanted the halves rejoined and nothing looked wrong;
// with a tenant the segment landed BETWEEN them, so the runtime read
// analysis/user=<uid>/grades/ while 168 backfilled objects sat in
// analysis/grades/user=<uid>/. Every producer reported success and the
// Projections tab was empty.
//
// The invariant that catches it: whatever prefix the runtime builds must begin
// with the artifact's declared prefix. A split can then only extend the layout
// path, never interleave with it.
func TestRuntimePrefixesStartWithTheLayoutPrefix(t *testing.T) {
	runtime := map[string]artifact{
		"Analysis Store":            analysisArtifact,
		"Team Value Store":          teamValueArtifact,
		"Lineup Gap Store":          lineupGapArtifact,
		"Run Ledger":                runLedgerArtifact,
		"Run Output":                runOutputArtifact,
		"Notifications":             notificationArtifact,
		"Published Lineup":          lineupArtifact,
		"Pending Offers":            tradesArtifact,
		"Trade Values Table":        tradeValuesArtifact,
		"Trade Offer Log":           tradeOfferArtifact,
		"Private Dashboard Reports": reportsArtifact,
		"Dynasty Value Store":       footballValueArtifact,
	}
	for _, a := range append(layout.All(), layout.Progress) {
		art, ok := runtime[a.Name]
		if !ok {
			continue // not built through statestore (Cache, Archive, Session, ...)
		}
		for _, tenant := range []string{"", "u123"} {
			got := ForTenant("bucket", tenant).prefixFor(art)
			if !strings.HasPrefix(got, a.S3Prefix) {
				t.Errorf("%s: runtime builds %q, which does not start with the declared "+
					"prefix %q. The backfill, the Infra gap scan and the Athena partition "+
					"template all use the declared one, so they would read a different "+
					"location than the producers write — with no error on either side",
					a.Name, got, a.S3Prefix)
			}
		}
	}
}

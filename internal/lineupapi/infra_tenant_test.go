package lineupapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

type stubLister struct{ l PrefixListing }

func (s stubLister) ListPrefix(_ context.Context, _ string) (PrefixListing, error) {
	return s.l, nil
}

func rowFor(t *testing.T, a layout.Artifact, l PrefixListing, now time.Time) ArtifactStatus {
	t.Helper()
	st := buildStatus(context.Background(), stubLister{l}, []layout.Artifact{a}, now)
	if len(st.Artifacts) != 1 {
		t.Fatalf("got %d rows, want 1", len(st.Artifacts))
	}
	return st.Artifacts[0]
}

// TestOneLiveTenantCannotMaskADeadOne is the reason this page needs a tenant
// dimension at all.
//
// LastModified on a per-tenant prefix is the newest object across EVERY tenant.
// With one tenant still running hourly and another silent for a week, the
// aggregate is minutes old and the row reads healthy — while the artifact this
// page exists to watch has stopped being produced for somebody. That is the
// rosterbot-ys8 blindness (a job that stopped is indistinguishable from one
// that is fine) reproduced one level down.
func TestOneLiveTenantCannotMaskADeadOne(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	a := layout.Lineup // durable, MaxAge 6h, PerTenant

	row := rowFor(t, a, PrefixListing{
		Objects: 100,
		// Aggregate freshness comes from the healthy tenant.
		LastModified: now.Add(-5 * time.Minute),
		Tenants: map[string]TenantListing{
			"alive": {Objects: 99, LastModified: now.Add(-5 * time.Minute)},
			"dead":  {Objects: 1, LastModified: now.Add(-7 * 24 * time.Hour)},
		},
	}, now)

	if row.Health != HealthStale {
		t.Fatalf("health = %q, want stale. The aggregate is 5 minutes old, so a "+
			"tenant-blind page reports OK while one tenant has produced nothing for "+
			"a week", row.Health)
	}
	if row.WorstTenant != "dead" {
		t.Errorf("WorstTenant = %q, want \"dead\" — a stale row that does not name "+
			"the tenant leaves the operator to find them by hand", row.WorstTenant)
	}
	if row.Tenants != 2 {
		t.Errorf("Tenants = %d, want 2", row.Tenants)
	}
}

// TestGapsAreScannedPerTenant covers the same masking in the partition scan.
// Unioning dt= values across tenants means a day missing for one tenant is
// hidden by any other tenant having written it.
func TestGapsAreScannedPerTenant(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	a := layout.TradeOffers // Partitioned + NoBackfill + PerTenant

	full := []string{"2026-08-11", "2026-08-12", "2026-08-13"}
	holed := []string{"2026-08-11", "2026-08-13"} // 08-12 missing

	row := rowFor(t, a, PrefixListing{
		Objects: 5, LastModified: now.Add(-time.Hour),
		// The union has no hole at all — that is the trap.
		Partitions: full,
		Tenants: map[string]TenantListing{
			"complete": {Objects: 3, LastModified: now.Add(-time.Hour), Partitions: full},
			"holed":    {Objects: 2, LastModified: now.Add(-time.Hour), Partitions: holed},
		},
	}, now)

	if len(row.Gaps) == 0 {
		t.Fatal("no gaps reported. The unioned dt= set is complete, so a tenant-blind " +
			"scan sees nothing — while one tenant is missing a day that, for an " +
			"unbackfillable artifact, is permanent data loss")
	}
	var found bool
	for _, g := range row.Gaps {
		if strings.HasPrefix(g, "holed/") && strings.HasSuffix(g, "2026-08-12") {
			found = true
		}
		if strings.HasPrefix(g, "complete/") {
			t.Errorf("reported a gap for the complete tenant: %q", g)
		}
	}
	if !found {
		t.Errorf("gaps = %v, want one naming holed/2026-08-12", row.Gaps)
	}
	if row.Health != HealthGap {
		t.Errorf("health = %q, want gap — TradeOffers is NoBackfill, so a missing "+
			"day cannot be re-run", row.Health)
	}
}

// TestSharedArtifactIgnoresTenantSlices guards the other direction. A shared
// artifact must be judged on its aggregate even if some key happens to contain
// a user= segment, or the league-wide stores would start reporting per-tenant
// health they do not have.
func TestSharedArtifactIgnoresTenantSlices(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	row := rowFor(t, layout.TeamValues, PrefixListing{
		Objects: 10, LastModified: now.Add(-time.Hour),
		Tenants: map[string]TenantListing{
			"stale-looking": {Objects: 1, LastModified: now.Add(-30 * 24 * time.Hour)},
		},
	}, now)

	if row.WorstTenant != "" || row.Tenants != 0 {
		t.Errorf("shared artifact reported tenant detail (worst=%q, n=%d); its data "+
			"describes the league, not one manager", row.WorstTenant, row.Tenants)
	}
	if row.Health != HealthOK {
		t.Errorf("health = %q, want ok", row.Health)
	}
}

// TestNoTenantSegmentsBehavesAsBefore is the pre-cutover state: the bucket has
// no user= segments yet, so a per-tenant artifact must judge exactly as it did
// before this change.
func TestNoTenantSegmentsBehavesAsBefore(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	row := rowFor(t, layout.Lineup, PrefixListing{
		Objects: 10, LastModified: now.Add(-time.Minute),
	}, now)

	if row.Health != HealthOK {
		t.Errorf("health = %q, want ok", row.Health)
	}
	if row.Tenants != 0 || row.WorstTenant != "" {
		t.Errorf("reported tenant detail with no tenant segments present")
	}
}

package lineupapi

import (
	"context"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

func TestRepro_SkippedExceedsPartitionsWithTwoTenants(t *testing.T) {
	now := ymd(2026, 7, 20)
	a := layout.Artifact{
		Name: "X", S3Prefix: "analysis/grades/", Durable: true,
		Partitioned: true, PerTenant: true,
	}
	// Two tenants both skip the SAME 3 All-Star-break days (a leaguewide gap:
	// every tenant's roster plays no games on the same dates), and those 3
	// days are the ONLY partitions that exist. The union (Partitions) counts
	// each calendar day once; the per-tenant qualified Skipped list counts
	// one entry per (tenant, day) pair.
	skipDays := []string{"2026-07-13", "2026-07-14", "2026-07-15"}
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/grades/": {
			Objects:      6,
			Partitions:   skipDays, // union across tenants: 3 distinct calendar days
			LastModified: now.Add(-time.Hour),
			Tenants: map[string]TenantListing{
				"tenantA": {Objects: 3, Partitions: skipDays, SkippedDays: skipDays},
				"tenantB": {Objects: 3, Partitions: skipDays, SkippedDays: skipDays},
			},
		},
	}}

	st := buildStatus(context.Background(), lister, []layout.Artifact{a}, now)
	row := st.Artifacts[0]

	t.Logf("Partitions=%d Skipped=%v (len=%d)", row.Partitions, row.Skipped, len(row.Skipped))
	if len(row.Skipped) > row.Partitions {
		t.Logf("CONFIRMED: skipped count (%d) exceeds partitions count (%d)", len(row.Skipped), row.Partitions)
	} else {
		t.Errorf("did not reproduce: skipped=%d partitions=%d", len(row.Skipped), row.Partitions)
	}
}

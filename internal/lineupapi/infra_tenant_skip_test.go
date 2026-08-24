package lineupapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// ArtifactStatus.Skipped is per-(tenant, day) while ArtifactStatus.Partitions
// counts distinct calendar days, so len(Skipped) can legitimately EXCEED
// Partitions. That looks like a bug and is not; this test exists so nobody
// "fixes" it into one.
//
// Making Skipped a day union would hide exactly what per-tenant computation is
// for: a day skipped for one tenant and graded for another. That is the same
// argument the Gaps block makes a few lines above it in buildStatusFor — the
// union hides the case worth seeing — and it is why the tenant qualifier is on
// the entry rather than the whole row.
//
// The obligation the asymmetry creates lands on the consumer: anything that
// compares the two must dedupe by day first. web/dashboard/infra.js does, for
// its "N days (M skipped)" annotation, which would otherwise read "3 days
// (6 skipped)" — a parenthetical asserting a subset while showing a superset.
func TestBuildStatus_SkippedIsPerTenantWhilePartitionsCountsDays(t *testing.T) {
	now := ymd(2026, 7, 20)
	a := layout.Artifact{
		Name: "X", S3Prefix: "analysis/grades/", Durable: true,
		Partitioned: true, PerTenant: true,
	}

	// The All-Star break is leaguewide, so every tenant skips the same days —
	// the ordinary way to reach this, not a contrived one.
	skipDays := []string{"2026-07-13", "2026-07-14", "2026-07-15"}
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/grades/": {
			Objects:      6,
			Partitions:   skipDays,
			LastModified: now.Add(-time.Hour),
			Tenants: map[string]TenantListing{
				"tenantA": {Objects: 3, Partitions: skipDays, SkippedDays: skipDays},
				"tenantB": {Objects: 3, Partitions: skipDays, SkippedDays: skipDays},
			},
		},
	}}

	row := buildStatus(context.Background(), lister, []layout.Artifact{a}, now).Artifacts[0]

	if row.Partitions != 3 {
		t.Errorf("Partitions = %d, want 3 distinct days", row.Partitions)
	}
	if len(row.Skipped) != 6 {
		t.Fatalf("Skipped = %v (len %d), want 6 — one entry per (tenant, day)", row.Skipped, len(row.Skipped))
	}

	// Two tenants, so every entry must carry its tenant; without the qualifier
	// the list would read as six days rather than three days twice over.
	days := map[string]bool{}
	for _, e := range row.Skipped {
		uid, day, found := strings.Cut(e, "/")
		if !found {
			t.Errorf("entry %q is unqualified, but there is more than one tenant", e)
			continue
		}
		if uid != "tenantA" && uid != "tenantB" {
			t.Errorf("entry %q names an unknown tenant", e)
		}
		days[day] = true
	}
	if len(days) != 3 {
		t.Errorf("deduped to %d days, want 3 — this is the count a consumer must show", len(days))
	}

	// A skipped day is correct by construction and must never colour the row.
	if row.Health != HealthOK {
		t.Errorf("Health = %v, want OK — every day here was deliberately skipped", row.Health)
	}
}

// The converse: one tenant, so the qualifier separates nothing and is omitted.
// An 87-character opaque WebAuthn handle in front of every date buries the
// only part the reader needs.
func TestBuildStatus_SingleTenantSkippedDaysAreNotQualified(t *testing.T) {
	now := ymd(2026, 7, 20)
	a := layout.Artifact{
		Name: "X", S3Prefix: "analysis/grades/", Durable: true,
		Partitioned: true, PerTenant: true,
	}
	days := []string{"2026-07-13", "2026-07-14"}
	lister := &fakeInfraLister{byPrefix: map[string]PrefixListing{
		"analysis/grades/": {
			Objects:      2,
			Partitions:   days,
			LastModified: now.Add(-time.Hour),
			Tenants: map[string]TenantListing{
				"onlyTenant": {Objects: 2, Partitions: days, SkippedDays: days},
			},
		},
	}}

	row := buildStatus(context.Background(), lister, []layout.Artifact{a}, now).Artifacts[0]

	if len(row.Skipped) != 2 {
		t.Fatalf("Skipped = %v, want 2", row.Skipped)
	}
	for _, e := range row.Skipped {
		if strings.Contains(e, "/") {
			t.Errorf("entry %q is tenant-qualified, but there is only one tenant", e)
		}
	}
}

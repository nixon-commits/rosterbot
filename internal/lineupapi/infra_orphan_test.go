package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

func rowForTenants(t *testing.T, a layout.Artifact, l PrefixListing, now time.Time, f tenantFilter) ArtifactStatus {
	t.Helper()
	st := buildStatusFor(context.Background(), stubLister{l}, []layout.Artifact{a}, now, f)
	if len(st.Artifacts) != 1 {
		t.Fatalf("got %d rows, want 1", len(st.Artifacts))
	}
	return st.Artifacts[0]
}

func liveTenantFilter(ids ...string) tenantFilter {
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	return tenantFilter{known: known}
}

// TestADeletedTenantsFrozenSliceDoesNotPinTheRow is the whole point of the
// filter (rosterbot-1oai).
//
// DeleteUser deliberately leaves a tenant's durable S3 artifacts behind, and
// calls them inert. They are not inert here: the tenant set is derived from
// user= segments in the listing, so a deleted tenant's frozen slice keeps being
// judged against MaxAge by a producer that will never run again. The row goes
// red and stays red for the life of the bucket — the failure this file's own
// LostGaps comment refuses to create ("a permanent red is how a status page
// teaches you to stop reading it").
func TestADeletedTenantsFrozenSliceDoesNotPinTheRow(t *testing.T) {
	now := time.Date(2026, 8, 21, 17, 24, 0, 0, time.UTC)
	a := layout.RunLedger // durable, MaxAge 6h, PerTenant

	l := PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			"live":    {Objects: 1870, LastModified: now.Add(-13 * time.Minute)},
			"deleted": {Objects: 5, LastModified: now.Add(-84 * time.Hour)},
		},
	}

	row := rowForTenants(t, a, l, now, liveTenantFilter("live"))

	if row.Health != HealthOK {
		t.Fatalf("health = %q, want ok. The only stale tenant no longer exists, so "+
			"nothing can ever clear this row", row.Health)
	}
	if row.WorstTenant != "" {
		t.Errorf("WorstTenant = %q, want empty", row.WorstTenant)
	}
	if row.Tenants != 1 {
		t.Errorf("Tenants = %d, want 1 — the count is paired with WorstTenant and "+
			"must describe the tenants actually being judged", row.Tenants)
	}
	// The orphan data still exists and still costs storage. Dropping it from the
	// verdict must not drop it from the page.
	if row.OrphanTenants != 1 {
		t.Errorf("OrphanTenants = %d, want 1 — a filtered-out segment that is "+
			"reported nowhere is indistinguishable from one that was never there",
			row.OrphanTenants)
	}
	if row.Objects != 1875 {
		t.Errorf("Objects = %d, want 1875 — the aggregate listing is a factual "+
			"statement about the bucket, not a verdict", row.Objects)
	}
}

// TestAnUnreadableTenantListJudgesEveryTenant pins the fail-open direction.
//
// The live tenant list arrives over the network. A listing that fails, or a
// deployment with no UserStore at all, must fall back to judging everyone: a
// filter that silently narrowed on a blind read would drop a REAL tenant whose
// producer had died, which is the rosterbot-ys8 blindness this page exists to
// catch. Getting this backwards trades a false alarm for a missing one.
func TestAnUnreadableTenantListJudgesEveryTenant(t *testing.T) {
	now := time.Date(2026, 8, 21, 17, 24, 0, 0, time.UTC)
	a := layout.RunLedger

	l := PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			"live": {Objects: 1870, LastModified: now.Add(-13 * time.Minute)},
			"dark": {Objects: 5, LastModified: now.Add(-84 * time.Hour)},
		},
	}

	for _, tc := range []struct {
		name string
		f    tenantFilter
	}{
		{"no user store at all", tenantFilter{}},
		{"listing returned nothing", liveTenantFilter()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rowForTenants(t, a, l, now, tc.f)
			if row.Health != HealthStale {
				t.Fatalf("health = %q, want stale — an unreadable tenant list must "+
					"judge everyone, not quietly excuse them", row.Health)
			}
			if row.WorstTenant != "dark" {
				t.Errorf("WorstTenant = %q, want \"dark\"", row.WorstTenant)
			}
			if row.OrphanTenants != 0 {
				t.Errorf("OrphanTenants = %d, want 0 — nothing is known to be an "+
					"orphan when the live set is unknown", row.OrphanTenants)
			}
		})
	}
}

// TestADeletedTenantContributesNoGaps covers the partition scan, which walks the
// same tenant map. An orphan's frozen dt= range would otherwise report missing
// days every one of which is unfixable by construction — and on a NoBackfill
// artifact that gap escalates the row's health as well.
func TestADeletedTenantContributesNoGaps(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	a := layout.TradeOffers // Partitioned + NoBackfill + PerTenant

	full := []string{"2026-08-18", "2026-08-19", "2026-08-20"}
	holed := []string{"2026-08-18", "2026-08-20"} // 08-19 missing

	row := rowForTenants(t, a, PrefixListing{
		Objects: 5, LastModified: now.Add(-time.Hour),
		Partitions: full,
		Tenants: map[string]TenantListing{
			"live":    {Objects: 3, LastModified: now.Add(-time.Hour), Partitions: full},
			"deleted": {Objects: 2, LastModified: now.Add(-time.Hour), Partitions: holed},
		},
	}, now, liveTenantFilter("live"))

	if len(row.Gaps) != 0 {
		t.Fatalf("Gaps = %v, want none — the hole belongs to a tenant that no "+
			"longer exists and no re-run could ever fill it", row.Gaps)
	}
	if row.Health != HealthOK {
		t.Errorf("health = %q, want ok — a NoBackfill gap escalates, so an orphan's "+
			"phantom hole would pin this row permanently", row.Health)
	}
}

// TestInfraJoinsTheTenantDirectoryToTheUserSegment drives the whole route with
// the ids production actually carries, because the filter's one silent failure
// mode is a join that never matches.
//
// UserID is a base64url WebAuthn handle and the S3 segment is
// layout.PrefixFor's "user=" + tenant — byte-identical, verified live: DDB holds
// pk "USER#57VUso…" while the bucket holds "runledger/user=57VUso…/". If those
// ever diverge the filter degrades to its fail-open zero value, every row keeps
// its old verdict, and nothing anywhere reports that the join stopped working.
// Comparing key strings would not catch it; driving both sides does.
func TestInfraJoinsTheTenantDirectoryToTheUserSegment(t *testing.T) {
	const (
		liveUID    = "57VUsoMRiBvON3CXIz3N3Tv5UhTsOeg_L517-QPobKY1x6r8VitfqTCgwkA9cDnCTwLQeQ2tn21Hj6S-0amddg"
		deletedUID = "JbSLJSUOLSabxKji_UpMzs_LZMFvlzwF3VnHJiAdd2hV_En3LLjRxRI2_kr3nj0s6FD0fd7_8GXj49cIDZ4YBQ"
	)

	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), &User{
		ID: liveUID, Email: "operator@example.test", Role: RoleAdmin, Status: UserActive,
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// deletedUID is deliberately never created — that IS the scenario.

	now := time.Now().UTC()
	lister := stubLister{PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			liveUID:    {Objects: 1870, LastModified: now.Add(-13 * time.Minute)},
			deletedUID: {Objects: 5, LastModified: now.Add(-84 * time.Hour)},
		},
	}}

	h := Handler(Config{Token: "t", Infra: lister, Users: users})
	req := httptest.NewRequest(http.MethodGet, "/v1/infra", nil)
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var st InfraStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var row *ArtifactStatus
	for i := range st.Artifacts {
		if st.Artifacts[i].Prefix == layout.RunLedger.S3Prefix {
			row = &st.Artifacts[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("no %s row in the response", layout.RunLedger.S3Prefix)
	}
	if row.Health != HealthOK {
		t.Errorf("health = %q, want ok — the tenant directory names only the live "+
			"tenant, so the 3.5-day-old slice belongs to nobody", row.Health)
	}
	if row.OrphanTenants != 1 {
		t.Errorf("OrphanTenants = %d, want 1 — a zero here means the join silently "+
			"matched nothing and the filter fell back to judging everyone",
			row.OrphanTenants)
	}
}

// TestAPrefixHoldingOnlyOrphanDataIsNotAFault covers the case where the filter
// removes every segment a prefix has.
//
// Reachable without deleting the whole deployment: a live tenant who has not
// produced THIS artifact yet, beside a deleted one who did. The aggregate
// LastModified is then entirely the orphan's, so leaving the row on the
// aggregate verdict reinstates the permanent red for that prefix — the same bug
// one layer along, and the reason health must come from the live segments
// rather than from the mixed aggregate.
func TestAPrefixHoldingOnlyOrphanDataIsNotAFault(t *testing.T) {
	now := time.Date(2026, 8, 21, 17, 24, 0, 0, time.UTC)
	a := layout.Trades // durable, MaxAge 14h, PerTenant

	row := rowForTenants(t, a, PrefixListing{
		Objects:      3,
		LastModified: now.Add(-84 * time.Hour), // the orphan's, since it is the only writer
		Tenants: map[string]TenantListing{
			"deleted": {Objects: 3, LastModified: now.Add(-84 * time.Hour)},
		},
	}, now, liveTenantFilter("live-but-has-not-written-here-yet"))

	if row.Health != HealthOK {
		t.Fatalf("health = %q, want ok — every object here belongs to a tenant that "+
			"no longer exists, so there is no producer whose silence this could "+
			"report", row.Health)
	}
	if row.OrphanTenants != 1 {
		t.Errorf("OrphanTenants = %d, want 1", row.OrphanTenants)
	}
	if row.Tenants != 0 {
		t.Errorf("Tenants = %d, want 0 — nothing live was judged", row.Tenants)
	}
}

package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

// perTenantConns is a ConnectionStore keyed by tenant, which memConnections
// (one record for the whole store) cannot be: every assertion here is about two
// tenants whose connections differ, so a single-record fake would answer the
// same thing for both and pass whatever the filter did.
type perTenantConns struct {
	conns map[UserID]*FantraxConnection
	errs  map[UserID]error
}

func (c perTenantConns) GetConnection(_ context.Context, uid UserID) (*FantraxConnection, bool, error) {
	if err := c.errs[uid]; err != nil {
		return nil, false, err
	}
	conn, ok := c.conns[uid]
	return conn, ok, nil
}

func (c perTenantConns) PutConnection(context.Context, *FantraxConnection) error { return nil }

func dormantTenantFilter(known []string, dormant ...string) tenantFilter {
	f := liveTenantFilter(known...)
	f.dormant = make(map[string]bool, len(dormant))
	for _, id := range dormant {
		f.dormant[id] = true
	}
	return f
}

// TestADormantTenantsFrozenSliceDoesNotPinTheRow is rosterbot-5lkj: the same
// permanent red as rosterbot-1oai, reached by the other route.
//
// The tenant EXISTS — a real member with a passkey — so liveTenants (ListUsers,
// deliberately not ListActive) keeps them on the page. But their Fantrax
// connection is needs_reconnect, and lambda/dispatch refuses to launch any job
// for a tenant whose connection is not Usable(). No producer will ever write
// under their segment again, so their slice cannot refresh, so it reads stale
// against MaxAge forever — with no action on this page that could clear it.
func TestADormantTenantsFrozenSliceDoesNotPinTheRow(t *testing.T) {
	now := time.Date(2026, 8, 28, 17, 24, 0, 0, time.UTC)
	a := layout.RunLedger // durable, MaxAge 6h, PerTenant

	l := PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			"connected": {Objects: 1870, LastModified: now.Add(-13 * time.Minute)},
			"dormant":   {Objects: 5, LastModified: now.Add(-169 * time.Hour)},
		},
	}

	row := rowForTenants(t, a, l, now, dormantTenantFilter([]string{"connected", "dormant"}, "dormant"))

	if row.Health != HealthOK {
		t.Fatalf("health = %q, want ok. The only stale tenant is one the fan-out "+
			"never launches, so nothing can ever clear this row", row.Health)
	}
	if row.WorstTenant != "" {
		t.Errorf("WorstTenant = %q, want empty", row.WorstTenant)
	}
	if row.Tenants != 1 {
		t.Errorf("Tenants = %d, want 1 — the count is paired with WorstTenant and "+
			"must describe the tenants actually being judged", row.Tenants)
	}
	if row.DormantTenants != 1 {
		t.Errorf("DormantTenants = %d, want 1 — a segment excluded from the verdict "+
			"and named nowhere is indistinguishable from one that was never there",
			row.DormantTenants)
	}
	if row.OrphanTenants != 0 {
		t.Errorf("OrphanTenants = %d, want 0 — this tenant exists; an orphan needs "+
			"no action and a dormant tenant is a person waiting on a reconnect",
			row.OrphanTenants)
	}
}

// TestADormantTenantContributesNoGaps covers the gap scan, which walks the same
// tenant map on a separate pass. Excluding a tenant from the health loop and not
// from this one leaves them able to redden the row through whichever path was
// missed — on a NoBackfill artifact a phantom hole escalates the health outright.
func TestADormantTenantContributesNoGaps(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	a := layout.TradeOffers // Partitioned + NoBackfill + PerTenant

	full := []string{"2026-08-25", "2026-08-26", "2026-08-27"}
	holed := []string{"2026-08-25", "2026-08-27"} // 08-26 missing

	row := rowForTenants(t, a, PrefixListing{
		Objects: 5, LastModified: now.Add(-time.Hour),
		Partitions: full,
		Tenants: map[string]TenantListing{
			"connected": {Objects: 3, LastModified: now.Add(-time.Hour), Partitions: full},
			"dormant":   {Objects: 2, LastModified: now.Add(-time.Hour), Partitions: holed},
		},
	}, now, dormantTenantFilter([]string{"connected", "dormant"}, "dormant"))

	if len(row.Gaps) != 0 {
		t.Fatalf("Gaps = %v, want none — the hole belongs to a tenant no job runs "+
			"for, so no re-run could ever fill it", row.Gaps)
	}
	if row.Health != HealthOK {
		t.Errorf("health = %q, want ok — a NoBackfill gap escalates, so a dormant "+
			"tenant's phantom hole would pin this row permanently", row.Health)
	}
}

// TestATenantThatIsBothDeletedAndDormantCountsAsAnOrphan pins the precedence,
// which is a deliberate choice rather than an accident of map iteration.
//
// Orphan wins because the two counts exist to prompt different actions: a
// dormant tenant is a person to chase for a reconnect, and there is nobody to
// chase once the account is gone. Counting them in both buckets would invent a
// second segment that does not exist.
func TestATenantThatIsBothDeletedAndDormantCountsAsAnOrphan(t *testing.T) {
	now := time.Date(2026, 8, 28, 17, 24, 0, 0, time.UTC)
	a := layout.RunLedger

	// "gone" is absent from the known set (deleted) AND marked dormant, the
	// state a filter built from a stale read could genuinely produce.
	f := liveTenantFilter("connected")
	f.dormant = map[string]bool{"gone": true}

	row := rowForTenants(t, a, PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			"connected": {Objects: 1870, LastModified: now.Add(-13 * time.Minute)},
			"gone":      {Objects: 5, LastModified: now.Add(-169 * time.Hour)},
		},
	}, now, f)

	if row.OrphanTenants != 1 {
		t.Errorf("OrphanTenants = %d, want 1", row.OrphanTenants)
	}
	if row.DormantTenants != 0 {
		t.Errorf("DormantTenants = %d, want 0 — one segment must be counted once, "+
			"and a deleted tenant is nobody to chase for a reconnect", row.DormantTenants)
	}
	if row.Tenants != 1 {
		t.Errorf("Tenants = %d, want 1", row.Tenants)
	}
}

// TestAConnectedTenantIsStillJudged is the inverse guard, and it is the one
// worth the most.
//
// A filter that quietly matched every tenant would exempt the whole page and
// nothing would look wrong: every row would read ok, including during a genuine
// producer outage. Dormancy must exempt exactly the tenants no job runs for.
func TestAConnectedTenantIsStillJudged(t *testing.T) {
	now := time.Date(2026, 8, 28, 17, 24, 0, 0, time.UTC)
	a := layout.RunLedger

	row := rowForTenants(t, a, PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			"connected": {Objects: 1870, LastModified: now.Add(-169 * time.Hour)},
			"dormant":   {Objects: 5, LastModified: now.Add(-13 * time.Minute)},
		},
	}, now, dormantTenantFilter([]string{"connected", "dormant"}, "dormant"))

	if row.Health != HealthStale {
		t.Fatalf("health = %q, want stale — a connected tenant's producer has gone "+
			"quiet, which is exactly the outage this page exists to report", row.Health)
	}
	if row.WorstTenant != "connected" {
		t.Errorf("WorstTenant = %q, want \"connected\"", row.WorstTenant)
	}
}

// TestLiveTenants_DormancyComesFromTheConnectionStore drives the filter's
// construction, because the predicate is the load-bearing part: it must be the
// SAME Usable() test lambda/dispatch gates the launch on. A second definition of
// "will a job run for this tenant" drifts from the one that decides, silently.
//
// The fail-open cases are here rather than in a split-level test for the same
// reason: a nil store and a failed read are decisions liveTenants makes, and
// getting either backwards trades a false alarm for a missing one.
func TestLiveTenants_DormancyComesFromTheConnectionStore(t *testing.T) {
	const (
		connected = UserID("connected")
		broken    = UserID("broken")
		unread    = UserID("unread")
		never     = UserID("never")
	)

	users := NewFileUserStore(t.TempDir())
	for _, uid := range []UserID{connected, broken, unread, never} {
		if err := users.CreateUser(context.Background(), &User{
			ID: uid, Email: string(uid) + "@example.test", Role: RoleMember, Status: UserActive,
		}); err != nil {
			t.Fatalf("CreateUser(%s): %v", uid, err)
		}
	}

	conns := perTenantConns{
		conns: map[UserID]*FantraxConnection{
			connected: {UserID: connected, Status: ConnVerified},
			broken:    {UserID: broken, Status: ConnNeedsReconnect},
			// `never` has no record at all — invited, never completed connect.
			// dispatch treats that identically to a broken one.
		},
		errs: map[UserID]error{unread: errors.New("dynamodb blip")},
	}

	t.Run("with a connection store", func(t *testing.T) {
		f := Config{Users: users, Connections: conns}.liveTenants(context.Background())

		for _, uid := range []UserID{connected, broken, unread, never} {
			if !f.live(string(uid)) {
				t.Errorf("live(%s) = false — every one of these tenants exists", uid)
			}
		}
		if f.dormant[string(connected)] {
			t.Error("a verified tenant is dormant — the fan-out launches for them, " +
				"so their silence is a real outage")
		}
		if !f.dormant[string(broken)] {
			t.Error("a needs_reconnect tenant is not dormant — dispatch refuses to " +
				"launch any job for them, so nothing will ever write their slice")
		}
		if !f.dormant[string(never)] {
			t.Error("a tenant with no connection record is not dormant — dispatch " +
				"skips them exactly as it skips a broken one")
		}
		if f.dormant[string(unread)] {
			t.Error("a tenant whose connection read FAILED is dormant — excluding " +
				"on a blind read is how a real dead producer goes unreported")
		}
	})

	t.Run("with no connection store at all", func(t *testing.T) {
		f := Config{Users: users}.liveTenants(context.Background())
		if len(f.dormant) != 0 {
			t.Errorf("dormant = %v, want empty — with no store there is nothing "+
				"known about who the fan-out launches for, so everyone is judged",
				f.dormant)
		}
	})
}

// TestInfraExemptsADormantTenantEndToEnd drives the whole route with the ids
// production carries, because the filter's one silent failure mode is a join
// that never matches: UserID is a base64url WebAuthn handle and the S3 segment
// is layout.PrefixFor's "user=" + tenant. If those diverge the dormant set is
// simply empty, the row keeps its old red verdict, and nothing reports that the
// join stopped working.
func TestInfraExemptsADormantTenantEndToEnd(t *testing.T) {
	const (
		liveUID    = "57VUsoMRiBvON3CXIz3N3Tv5UhTsOeg_L517-QPobKY1x6r8VitfqTCgwkA9cDnCTwLQeQ2tn21Hj6S-0amddg"
		dormantUID = "4tTXzUa7LSabxKji_UpMzs_LZMFvlzwF3VnHJiAdd2hV_En3LLjRxRI2_kr3nj0s6FD0fd7_8GXj49cIDZ4YBQ"
	)

	users := NewFileUserStore(t.TempDir())
	for _, u := range []*User{
		{ID: liveUID, Email: "operator@example.test", Role: RoleAdmin, Status: UserActive},
		{ID: dormantUID, Email: "tester@example.test", Role: RoleMember, Status: UserActive},
	} {
		if err := users.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	conns := perTenantConns{conns: map[UserID]*FantraxConnection{
		liveUID:    {UserID: liveUID, Status: ConnVerified},
		dormantUID: {UserID: dormantUID, Status: ConnNeedsReconnect},
	}}

	now := time.Now().UTC()
	lister := stubLister{PrefixListing{
		Objects:      1875,
		LastModified: now.Add(-13 * time.Minute),
		Tenants: map[string]TenantListing{
			liveUID:    {Objects: 1870, LastModified: now.Add(-13 * time.Minute)},
			dormantUID: {Objects: 5, LastModified: now.Add(-169 * time.Hour)},
		},
	}}

	h := Handler(Config{Token: "t", Infra: lister, Users: users, Connections: conns})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/infra", nil)
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
		t.Errorf("health = %q, want ok — the 7-day-old slice belongs to a tenant "+
			"the fan-out refuses to launch", row.Health)
	}
	if row.DormantTenants != 1 {
		t.Errorf("DormantTenants = %d, want 1 — a zero here means the join silently "+
			"matched nothing and the filter fell back to judging everyone",
			row.DormantTenants)
	}
	if row.OrphanTenants != 0 {
		t.Errorf("OrphanTenants = %d, want 0 — both tenants exist", row.OrphanTenants)
	}
}

// TestAParkedTenantIsDormant pins the OTHER half of dispatch's launch gate.
//
// That gate is a conjunction, not a single call: dispatch lists via ListActive
// (which filters Status == UserActive) and THEN checks conn.Usable(). Reading
// only the connection half left `parked AND ConnVerified` judged — a state the
// Tenants tab produces directly, since handleSetTenantStatus parks by writing
// u.Status alone and never touches the connection record. The symptom was this
// bead's own: park a tester, and six hours later every per-tenant row reads
// stale off their frozen slice with no detail line and nothing able to clear it.
//
// The no-store case is the sharper half. Parking is knowable from the user
// record alone, so gating it behind a connection read would leave local dev —
// where Connections is nil — judging a tenant the scheduler skips.
func TestAParkedTenantIsDormant(t *testing.T) {
	const (
		active = UserID("active-connected")
		parked = UserID("parked-but-connected")
	)

	users := NewFileUserStore(t.TempDir())
	for uid, status := range map[UserID]UserStatus{active: UserActive, parked: UserParked} {
		if err := users.CreateUser(context.Background(), &User{
			ID: uid, Email: string(uid) + "@example.test", Role: RoleMember, Status: status,
		}); err != nil {
			t.Fatalf("CreateUser(%s): %v", uid, err)
		}
	}

	// BOTH connections are verified. If the filter consulted only the
	// connection store, neither tenant would be dormant and this test could
	// not fail — which is precisely how the gap survived the first round.
	conns := perTenantConns{conns: map[UserID]*FantraxConnection{
		active: {UserID: active, Status: ConnVerified},
		parked: {UserID: parked, Status: ConnVerified},
	}}

	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"with a connection store", Config{Users: users, Connections: conns}},
		{"with no connection store at all", Config{Users: users}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.cfg.liveTenants(context.Background())

			if !f.live(string(parked)) {
				t.Error("live(parked) = false — a parked tenant still EXISTS, and " +
					"dropping them from the page hides somebody who is broken")
			}
			if !f.dormant[string(parked)] {
				t.Error("a parked tenant is not dormant — dispatch's ListActive " +
					"filters them out, so no producer will ever write their slice " +
					"and their frozen data would pin every per-tenant row red")
			}
			if f.dormant[string(active)] {
				t.Error("an active, verified tenant is dormant — the fan-out " +
					"launches for them, so their silence is a real outage and " +
					"exempting it blanks the page's ability to report one")
			}
		})
	}
}

// TestAnAllDormantPrefixReportsNoGaps is the all-exempt case, which the
// per-tenant gap scan used to miss because it was gated on the JUDGED tenant set
// while the health rebuild above it was gated on the aggregate. With every
// segment exempt the judged set is empty, control fell through to the flat
// branch, and row.Gaps kept the union computed from the exempt tenants' own
// partitions — so health rebuilt to ok and a NoBackfill artifact was then
// escalated straight back to HealthGap off a hole no job could ever fill.
//
// Reachable on one broken connection — the operator's own, after a password
// rotation — where the orphan equivalent would have required deleting the whole
// deployment. That asymmetry is why it survived the orphan work unnoticed.
func TestAnAllDormantPrefixReportsNoGaps(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	a := layout.TradeOffers // Partitioned + NoBackfill + PerTenant

	holed := []string{"2026-08-25", "2026-08-27"} // 08-26 missing

	row := rowForTenants(t, a, PrefixListing{
		Objects: 2, LastModified: now.Add(-9 * 24 * time.Hour),
		Partitions: holed,
		Tenants: map[string]TenantListing{
			"only": {Objects: 2, LastModified: now.Add(-9 * 24 * time.Hour), Partitions: holed},
		},
	}, now, dormantTenantFilter([]string{"only"}, "only"))

	if len(row.Gaps) != 0 {
		t.Errorf("Gaps = %v, want none — every segment here belongs to a tenant "+
			"the scheduler skips, so these are somebody else's missing days and "+
			"no re-run could fill them", row.Gaps)
	}
	if len(row.LostGaps) != 0 {
		t.Errorf("LostGaps = %v, want none — reporting a day as gone for good "+
			"when no producer was ever going to write it is a red nothing clears",
			row.LostGaps)
	}
	if row.Health != HealthOK {
		t.Errorf("health = %q, want ok — the NoBackfill escalation fired off a "+
			"phantom hole, reinstating the permanent red this exemption removes",
			row.Health)
	}
	if row.DormantTenants != 1 {
		t.Errorf("DormantTenants = %d, want 1 — the row must still SAY why it is "+
			"quiet, or a silenced prefix is indistinguishable from a healthy one",
			row.DormantTenants)
	}
}

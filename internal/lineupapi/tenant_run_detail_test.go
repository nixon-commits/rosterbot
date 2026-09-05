package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTenantRun_MemberForbidden mirrors TestTenantRuns_MemberForbidden one
// route down: the run-DETAIL route lives under /v1/tenants/ too, and only a
// test proves the adminOnlyRoutes prefix actually reaches it — this exposes a
// FAILED run's log tail, not only the list.
func TestTenantRun_MemberForbidden(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/admin1/runs/r-admin", "bob", 0))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member GET /v1/tenants/admin1/runs/r-admin = %d, want 403", rec.Code)
	}
}

// TestTenantRun_AdminSeesTheNamedTenantsDetail is the admin happy path: the
// detail returned (log tail included) belongs to the tenant named in the
// PATH, not to the admin caller, and a run id that exists only under the
// OTHER tenant's ledger 404s rather than being served from a shared store.
//
// MUTATION: resolve the view via cfg.tenantView(ctx) (the caller's own view)
// instead of cfg.tenantViewOf(ctx, id) — every tenant's drill-down would then
// show the ADMIN's own run detail.
func TestTenantRun_AdminSeesTheNamedTenantsDetail(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/bob/runs/r-bob", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET bob's run detail = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "log-of-r-bob") {
		t.Errorf("bob's log tail is missing from his run detail: %s", body)
	}

	// The other direction, so the assertion above is not vacuously true of a
	// build that always serves a fixed single-tenant detail.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/admin1/runs/r-admin", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET admin1's own run detail = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "log-of-r-admin") {
		t.Errorf("admin1's own log tail is missing from his own run detail: %s", rec.Body)
	}
}

// TestTenantRun_CrossTenantRunID404s: admin1's run id, asked for under bob's
// path, must 404 rather than silently falling back to admin1's ledger. This
// is the run-detail twin of TestTenantRunOutput_AdminSeesTheNamedTenantsOutput's
// negative case.
func TestTenantRun_CrossTenantRunID404s(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/bob/runs/r-admin", "admin1", 0))
	if rec.Code != http.StatusNotFound {
		t.Errorf("admin's own run id under bob's path = %d, want 404: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "log-of-r-admin") {
		t.Errorf("admin1's log tail leaked into bob's drill-down: %s", rec.Body)
	}
}

// TestTenantRun_UnknownTenant404s, same reasoning as
// TestTenantRuns_UnknownTenant404s one route down: TenantStores.For builds
// stores at a prefix derived from the uid string and would succeed for a
// prefix nothing has ever written to, so only the user directory can tell
// "unknown tenant" from "known tenant, no such run".
func TestTenantRun_UnknownTenant404s(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/nobody/runs/r-x", "admin1", 0))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("unknown tenant run detail = %d %q, want a handler 404 naming the missing "+
			"user (a bare mux 404 means the route itself is absent)", rec.Code, rec.Body)
	}
}

// connectRunPerTenant is a TenantStores giving each tenant a single "connect"
// command run, keyed by tenant so admin1 and bob each have a DIFFERENT run id
// — the shape TestTenants_ConnectVerdictIsTheRowTenantsNotTheCallers exercises
// for the list route, reused here for the detail route.
type connectRunPerTenant map[UserID]string

func (m connectRunPerTenant) For(_ context.Context, uid UserID) (TenantView, error) {
	id := m[uid]
	if id == "" {
		return TenantView{}, nil
	}
	return TenantView{Runs: connectRunDetail{id: id}}, nil
}

type connectRunDetail struct{ id string }

func (d connectRunDetail) List(context.Context, int) ([]Run, error) {
	return []Run{{ID: d.id, Command: "connect", Status: "SUCCESS", StartedAt: nowRFC3339()}}, nil
}

func (d connectRunDetail) Get(_ context.Context, id string) (*RunDetail, bool, error) {
	if id != d.id {
		return nil, false, nil
	}
	return &RunDetail{
		Run:     Run{ID: d.id, Command: "connect", Status: "SUCCESS", StartedAt: nowRFC3339()},
		LogTail: "log-of-" + d.id,
	}, true, nil
}

// TestTenantRun_CarriesTheRowTenantsConnectVerdict pins that the detail route
// stamps the connect verdict via cfg.tenantConnectRun (the row tenant's own
// connection record), not cfg.lastConnectRun (the caller's) — the same
// mis-attribution hazard connectrun.go's doc comment warns about, and which
// the list route (TestTenants_ConnectVerdictIsTheRowTenantsNotTheCallers)
// already guards for GET /v1/tenants.
//
// MUTATION: swap cfg.tenantConnectRun(ctx, id, ...) for cfg.lastConnectRun(ctx,
// ...) in handleTenantRun — bob's verified connect run then inherits admin1's
// (the calling admin's) failed verdict.
func TestTenantRun_CarriesTheRowTenantsConnectVerdict(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	for _, u := range []*User{
		{ID: "admin1", Email: "op@e.test", Role: RoleAdmin, Status: UserActive},
		{ID: "bob", Email: "bob@e.test", Role: RoleMember, Status: UserActive},
	} {
		if err := users.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	secret := []byte("s")
	h := Handler(Config{
		Users: users, Enrollments: users, SessionSecret: secret, WebAuthn: testWebAuthn(t),
		Tenants: connectRunPerTenant{"admin1": "r-op", "bob": "r-bob"},
		Connections: perTenantConns{conns: map[UserID]*FantraxConnection{
			"admin1": {UserID: "admin1", LastConnectRun: &ConnectRun{
				RunID: "r-op", Verdict: ConnectVerdictFailed, LastError: "bot_challenge"}},
			"bob": {UserID: "bob", LastConnectRun: &ConnectRun{
				RunID: "r-bob", Verdict: ConnectVerdictVerified}},
		}},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/bob/runs/r-bob", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET bob's run detail = %d, want 200: %s", rec.Code, rec.Body)
	}
	var detail RunDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if detail.Connect == nil || detail.Connect.Verdict != ConnectVerdictVerified {
		t.Errorf("bob's run detail = %+v, want his own verified connect verdict, not "+
			"the calling admin's failed one", detail.Connect)
	}
}

package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tenantDrilldownFixture is tenantsFixture plus a per-tenant run ledger and
// captured output, built with runDetailPerTenant (runs_tenantparam_test.go) so
// admin1 and bob each have a DIFFERENT, DISTINGUISHABLE ledger — a shared
// store would answer identically for both rows and pass whether or not the
// route resolved the right tenant.
func tenantDrilldownFixture(t *testing.T) (http.Handler, []byte) {
	t.Helper()
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
	tenants := runDetailPerTenant{"admin1": "r-admin", "bob": "r-bob"}
	return Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Tenants: tenants}), secret
}

// TestTenantRuns_MemberForbidden mirrors TestTenantStatus_MemberForbidden: the
// route lives under /v1/tenants/, and only a test proves the adminOnlyRoutes
// prefix actually reaches it — this exposes every tenant's full run ledger, not
// only the bounded summary GET /v1/tenants carries.
func TestTenantRuns_MemberForbidden(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/admin1/runs", "bob", 0))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member GET /v1/tenants/admin1/runs = %d, want 403", rec.Code)
	}
}

// TestTenantRunOutput_MemberForbidden, same shape one level down: the captured
// stdout of another tenant's run.
func TestTenantRunOutput_MemberForbidden(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet,
		"/v1/tenants/admin1/runs/r-admin/output", "bob", 0))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member GET /v1/tenants/admin1/runs/r-admin/output = %d, want 403", rec.Code)
	}
}

// TestTenantRuns_AdminSeesTheNamedTenantsLedger is the admin happy path: the
// ledger returned belongs to the tenant named in the PATH, not to the admin
// caller, and not to the OTHER tenant either.
//
// MUTATION: resolve the view via cfg.tenantView(ctx) (the caller's own view)
// instead of cfg.tenantViewOf(ctx, id) — every tenant's drill-down would then
// show the ADMIN's own ledger, the exact per-row mis-attribution
// rosterbot-nejq's TenantView exists to remove.
func TestTenantRuns_AdminSeesTheNamedTenantsLedger(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/bob/runs", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET bob's runs = %d, want 200: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "r-bob") {
		t.Errorf("bob's own run is missing from his drill-down: %s", body)
	}
	if strings.Contains(body, "r-admin") {
		t.Errorf("the ADMIN CALLER's run leaked into bob's drill-down: %s", body)
	}

	// The other direction, so the assertion above is not vacuously true of a
	// build that always serves a fixed single-tenant ledger.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/admin1/runs", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET admin1's own runs = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "r-admin") {
		t.Errorf("admin1's own run is missing from his own drill-down: %s", rec.Body)
	}
}

// TestTenantRunOutput_AdminSeesTheNamedTenantsOutput is the output twin: the
// captured stdout returned belongs to the PATH tenant's run, and the same run
// id under the wrong tenant's path 404s rather than falling back to a shared
// store.
func TestTenantRunOutput_AdminSeesTheNamedTenantsOutput(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet,
		"/v1/tenants/bob/runs/r-bob/output", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET bob's run output = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "output-of-r-bob") {
		t.Errorf("bob's captured output is missing: %s", rec.Body)
	}

	// admin1's own run id does not exist in bob's output store, so asking for
	// it under bob's path must 404, not silently serve admin1's output.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet,
		"/v1/tenants/bob/runs/r-admin/output", "admin1", 0))
	if rec.Code != http.StatusNotFound {
		t.Errorf("admin's own run id under bob's path = %d, want 404: %s", rec.Code, rec.Body)
	}
}

// TestTenantRuns_UnknownTenant404s pins the handler 404 (naming the missing
// user) rather than the empty-200 a bare tenantViewOf would produce:
// TenantStores.For builds stores at a PREFIX derived from the uid string and
// succeeds for a prefix nothing has ever written to, so only the user
// directory can tell "unknown tenant" from "known tenant, empty ledger".
func TestTenantRuns_UnknownTenant404s(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants/nobody/runs", "admin1", 0))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("unknown tenant runs = %d %q, want a handler 404 naming the missing "+
			"user (a bare mux 404 means the route itself is absent)", rec.Code, rec.Body)
	}
}

// TestTenantRunOutput_UnknownTenant404s, same reasoning one level down.
func TestTenantRunOutput_UnknownTenant404s(t *testing.T) {
	h, secret := tenantDrilldownFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet,
		"/v1/tenants/nobody/runs/r-x/output", "admin1", 0))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("unknown tenant run output = %d %q, want a handler 404 naming the "+
			"missing user", rec.Code, rec.Body)
	}
}

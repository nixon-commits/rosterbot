package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func tenantsFixture(t *testing.T) (http.Handler, []byte) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	for _, u := range []*User{
		{ID: "admin1", Email: "op@e.test", Role: RoleAdmin, Status: UserActive, AutoApply: true},
		{ID: "bob", Email: "bob@e.test", Role: RoleMember, Status: UserActive},
	} {
		if err := users.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	secret := []byte("s")
	return Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t)}), secret
}

// TestTenants_IsAdminOnly is the whole security property of this endpoint. It
// exposes every tenant's email, team and connection state, and adminOnlyRoutes
// is an ALLOWLIST — a route omitted from it is reachable by every member, which
// is how a new route defaults in this codebase.
func TestTenants_IsAdminOnly(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "bob", 0))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member GET /v1/tenants = %d, want 403 — every tenant's email and "+
			"connection state is behind this route", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Errorf("admin GET /v1/tenants = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// TestTenants_ListsEveryActiveTenant, in a stable order so the page does not
// reshuffle between polls.
func TestTenants_ListsEveryActiveTenant(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))

	var body TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if len(body.Tenants) != 2 {
		t.Fatalf("got %d tenants, want 2: %+v", len(body.Tenants), body.Tenants)
	}
	if body.Tenants[0].ID != "admin1" || body.Tenants[1].ID != "bob" {
		t.Errorf("unstable order: %v, %v", body.Tenants[0].ID, body.Tenants[1].ID)
	}
}

// TestTenants_ShowsAutoApply. It is the difference between a bot that proposes
// and one that writes to somebody's team, so an operator scanning this page is
// entitled to see whose rosters the deployment is actually touching.
func TestTenants_ShowsAutoApply(t *testing.T) {
	h, secret := tenantsFixture(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))

	var body TenantsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	byID := map[UserID]TenantSummary{}
	for _, tn := range body.Tenants {
		byID[tn.ID] = tn
	}
	if !byID["admin1"].AutoApply {
		t.Error("admin1's auto_apply reported false; the page would understate what the bot writes to")
	}
	if byID["bob"].AutoApply {
		t.Error("bob's auto_apply reported true; the page would overstate it")
	}
}

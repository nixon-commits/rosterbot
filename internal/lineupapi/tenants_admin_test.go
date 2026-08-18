package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// adminFixture is tenantsFixture with the store exposed, so tests can assert
// on what the management routes actually wrote.
func adminFixture(t *testing.T) (http.Handler, []byte, *FileUserStore) {
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
		WebAuthn: testWebAuthn(t)}), secret, users
}

// postJSON builds an authenticated JSON POST the way signedReq builds a GET.
func postJSON(t *testing.T, secret []byte, path, uid string, body string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, UserID(uid), 0, time.Now())
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(http.MethodPost, path, rd)
	r.Header.Set("Content-Type", "application/json")
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

// TestTenantStatus_ParkBlocksTheirSessionAndReactivateRestores is the whole
// point of the park control: a parked tenant's existing session must stop
// working immediately (authz refuses non-active users on every request), and
// reactivation must restore it — no token_version churn, no re-enrollment.
func TestTenantStatus_ParkBlocksTheirSessionAndReactivateRestores(t *testing.T) {
	h, secret := tenantsFixture(t)

	// bob works before the park.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "bob", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("pre-park GET /v1/me as bob = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/status", "admin1", `{"status":"parked"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("park bob = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "bob", 0))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("parked bob GET /v1/me = %d, want 401 — a parked tenant's live "+
			"session must stop working, not linger until expiry", rec.Code)
	}

	// The parked tenant must STAY VISIBLE on the admin page, as parked. The
	// job fan-out's ListActive rightly drops them; if this page used the same
	// listing, parking would make the row vanish and there would be no unpark
	// control anywhere in the UI.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	var listed TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode tenants: %v (%s)", err, rec.Body)
	}
	var parked *TenantSummary
	for i := range listed.Tenants {
		if listed.Tenants[i].ID == "bob" {
			parked = &listed.Tenants[i]
		}
	}
	if parked == nil {
		t.Fatal("parked tenant missing from GET /v1/tenants — the row an operator " +
			"needs in order to reactivate them")
	} else if parked.Status != UserParked {
		t.Errorf("parked tenant listed with status %q, want %q", parked.Status, UserParked)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/status", "admin1", `{"status":"active"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate bob = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "bob", 0))
	if rec.Code != http.StatusOK {
		t.Errorf("reactivated bob GET /v1/me = %d, want 200 — reactivation must "+
			"restore the same session, not require a new passkey", rec.Code)
	}
}

// TestTenantStatus_RefusesParkingYourself: parking your own account with your
// own session locks you out with no dashboard path back; the CLI or bearer
// token would be the only recovery. Refusing is cheaper than the support call.
func TestTenantStatus_RefusesParkingYourself(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/admin1/status", "admin1", `{"status":"parked"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-park = %d, want 400", rec.Code)
	}

	// The account must be untouched.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Errorf("admin1 locked out after refused self-park: GET /v1/me = %d", rec.Code)
	}
}

// TestTenantStatus_MemberForbidden pins the allowlist coverage: the management
// routes live under /v1/tenants/, so the existing adminOnlyRoutes prefix entry
// must gate them without a new entry — but only a test proves the prefix
// actually reaches them.
func TestTenantStatus_MemberForbidden(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/admin1/status", "bob", `{"status":"parked"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member park admin = %d, want 403", rec.Code)
	}
}

// TestTenantStatus_RejectsUnknownStatus: the status vocabulary is two words,
// and anything else must be refused rather than written — a typo'd status
// would make the user invisible to ListActive with no way to see why.
func TestTenantStatus_RejectsUnknownStatus(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/status", "admin1", `{"status":"banana"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// TestTenantStatus_UnknownTenant404s.
func TestTenantStatus_UnknownTenant404s(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/nobody/status", "admin1", `{"status":"parked"}`))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("park unknown tenant = %d %q, want a handler 404 naming the missing "+
			"user (a bare mux 404 means the route itself is absent)", rec.Code, rec.Body)
	}
}

// TestTenantAutoApply_AdminKillSwitch: the operator can cut lineup writes for
// one tenant without parking them — the tenant keeps their dashboard, the bot
// keeps proposing, it just stops writing.
func TestTenantAutoApply_AdminKillSwitch(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/admin1/auto-apply", "admin1", `{"auto_apply":false}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("kill switch = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	var body TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	for _, tn := range body.Tenants {
		if tn.ID == "admin1" && tn.AutoApply {
			t.Error("auto_apply still true after the kill switch wrote false")
		}
	}

	// Absent field must be refused, not read as false.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/admin1/auto-apply", "admin1", `{}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty auto-apply body = %d, want 400", rec.Code)
	}
}

// TestTenantInvite_MintsARedeemableEnrollment: the dashboard invite must
// produce exactly what the CLI produces — a member-role, propose-only user and
// a token that redeems for that user's id — because the two paths will be used
// interchangeably and a drift between them is invisible until an invitee hits
// it.
func TestTenantInvite_MintsARedeemableEnrollment(t *testing.T) {
	h, secret, users := adminFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/invite", "admin1",
		`{"email":"new@e.test","name":"New Tester","team_id":"t9"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d, want 200: %s", rec.Code, rec.Body)
	}

	var resp struct {
		UserID    UserID    `json:"user_id"`
		Email     string    `json:"email"`
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if resp.Token == "" || resp.UserID == "" {
		t.Fatalf("invite response missing token or user_id: %s", rec.Body)
	}
	if resp.ExpiresAt.IsZero() || !resp.ExpiresAt.After(time.Now()) {
		t.Errorf("expires_at = %v, want a future time", resp.ExpiresAt)
	}

	// The user exists with the safe defaults.
	u, ok, err := users.GetUser(context.Background(), resp.UserID)
	if err != nil || !ok {
		t.Fatalf("invited user not in store: ok=%v err=%v", ok, err)
	}
	if u.Role != RoleMember || u.Status != UserActive || u.AutoApply {
		t.Errorf("invited user = role %q status %q auto_apply %v, want member/active/false",
			u.Role, u.Status, u.AutoApply)
	}
	if u.EmailAttestedBy != "admin1" {
		t.Errorf("EmailAttestedBy = %q, want the minting admin — the invite is "+
			"the attestation, so it must record who vouched", u.EmailAttestedBy)
	}

	// The token redeems for exactly that user.
	e, err := users.RedeemEnrollment(context.Background(),
		HashEnrollmentToken(EnrollmentToken(resp.Token)), time.Now())
	if err != nil {
		t.Fatalf("redeem minted token: %v", err)
	}
	if e.UserID != resp.UserID {
		t.Errorf("enrollment redeems for %q, want %q", e.UserID, resp.UserID)
	}
}

// TestTenantInvite_DuplicateEmailConflicts: the uniqueness failure must surface
// while the admin is looking at the form, as a comprehensible 409.
func TestTenantInvite_DuplicateEmailConflicts(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/invite", "admin1", `{"email":"bob@e.test"}`))
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate email invite = %d, want 409: %s", rec.Code, rec.Body)
	}
}

// TestTenantRecovery_MintsForTheExistingUser: recovery is the same primitive as
// invite scoped to an existing account — it must not create a second user, and
// its token must redeem for the account being recovered.
func TestTenantRecovery_MintsForTheExistingUser(t *testing.T) {
	h, secret, users := adminFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/recovery", "admin1", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery = %d, want 200: %s", rec.Code, rec.Body)
	}

	var resp struct {
		UserID UserID `json:"user_id"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if resp.UserID != "bob" {
		t.Errorf("recovery user_id = %q, want bob", resp.UserID)
	}

	e, err := users.RedeemEnrollment(context.Background(),
		HashEnrollmentToken(EnrollmentToken(resp.Token)), time.Now())
	if err != nil {
		t.Fatalf("redeem recovery token: %v", err)
	}
	if e.UserID != "bob" {
		t.Errorf("recovery enrollment redeems for %q, want bob", e.UserID)
	}
}

// TestTenantRecovery_UnknownTenant404s: a recovery link for a nonexistent user
// would mint a token that can never be redeemed into a working account.
func TestTenantRecovery_UnknownTenant404s(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/nobody/recovery", "admin1", ""))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("recovery for unknown tenant = %d %q, want a handler 404 naming the "+
			"missing user (a bare mux 404 means the route itself is absent)", rec.Code, rec.Body)
	}
}

// TestTenants_ReportsPasskeyCount: the row must distinguish "invited but never
// registered" (0 passkeys) from a registered tenant, and a credentials-store
// hiccup must read as unknown (absent), never as zero — a masked error that
// prints 0 would tell the operator to re-invite someone who is fine.
func TestTenants_ReportsPasskeyCount(t *testing.T) {
	h, secret, users := adminFixture(t)

	if err := users.PutCredential(context.Background(), "bob",
		webauthn.Credential{ID: []byte("cred-1")}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	var body TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	byID := map[UserID]TenantSummary{}
	for _, tn := range body.Tenants {
		byID[tn.ID] = tn
	}
	if byID["bob"].Passkeys == nil || *byID["bob"].Passkeys != 1 {
		t.Errorf("bob passkeys = %v, want 1", byID["bob"].Passkeys)
	}
	if byID["admin1"].Passkeys == nil || *byID["admin1"].Passkeys != 0 {
		t.Errorf("admin1 passkeys = %v, want 0 — an invited-but-unregistered "+
			"tenant is exactly what this column exists to show", byID["admin1"].Passkeys)
	}
}

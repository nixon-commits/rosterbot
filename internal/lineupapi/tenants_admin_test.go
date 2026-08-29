package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
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

// deleteReq builds an authenticated DELETE the way postJSON builds a POST.
func deleteReq(t *testing.T, secret []byte, path, uid string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, UserID(uid), 0, time.Now())
	r := httptest.NewRequest(http.MethodDelete, path, nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

// TestTenantDelete_RemovesTheTenantAndReleasesTheirEmail: delete is the
// removal park cannot be — the profile, credentials and claims all go, their
// live session stops working, and the email can be re-invited. The claim
// release is the load-bearing half: without it a deleted tester's email and
// team are poisoned forever.
func TestTenantDelete_RemovesTheTenantAndReleasesTheirEmail(t *testing.T) {
	h, secret, _ := adminFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deleteReq(t, secret, "/v1/tenants/bob", "admin1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete bob = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "bob", 0))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted bob's session still works (%d)", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	var listed TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode tenants: %v (%s)", err, rec.Body)
	}
	for _, tn := range listed.Tenants {
		if tn.ID == "bob" {
			t.Error("deleted tenant still listed")
		}
	}

	// The email claim is released: the same address can be invited again.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/invite", "admin1", `{"email":"bob@e.test"}`))
	if rec.Code != http.StatusOK {
		t.Errorf("re-inviting a deleted tenant's email = %d, want 200 — the claim "+
			"was not released: %s", rec.Code, rec.Body)
	}
}

// TestTenantDelete_RefusesDeletingYourself, on the same reasoning as
// self-park: irreversible self-lockout with no dashboard path back.
func TestTenantDelete_RefusesDeletingYourself(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deleteReq(t, secret, "/v1/tenants/admin1", "admin1"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-delete = %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Errorf("admin gone after refused self-delete: GET /v1/me = %d", rec.Code)
	}
}

// TestTenantDelete_MemberForbidden pins the allowlist prefix onto the DELETE
// verb too.
func TestTenantDelete_MemberForbidden(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deleteReq(t, secret, "/v1/tenants/admin1", "bob"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member delete = %d, want 403", rec.Code)
	}
}

// TestTenantDelete_UnknownTenant404s, with the handler's own body so a bare
// mux 404 (route absent) cannot pass.
func TestTenantDelete_UnknownTenant404s(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, deleteReq(t, secret, "/v1/tenants/nobody", "admin1"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("delete unknown tenant = %d %q, want a handler 404 naming the missing user", rec.Code, rec.Body)
	}
}

// TestTenantSetTeam_AssignsAndClaims is the happy path: the operator binds a
// Fantrax team to a tenant invited without one, and the binding must land in
// the CLAIM INDEX, not merely on the profile — a profile-only write would leave
// the team assignable to a second tenant, which is the one thing ErrTeamTaken
// exists to prevent.
func TestTenantSetTeam_AssignsAndClaims(t *testing.T) {
	h, secret, users := adminFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/team", "admin1", `{"team_id":"team-7"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("set team = %d, want 200: %s", rec.Code, rec.Body)
	}

	u, ok, err := users.GetUser(context.Background(), "bob")
	if err != nil || !ok {
		t.Fatalf("read bob: %v ok=%v", err, ok)
	}
	if u.TeamID != "team-7" {
		t.Errorf("bob holds team %q, want team-7", u.TeamID)
	}
	// The claim index is the half a profile write would silently skip.
	if err := users.ClaimTeam(context.Background(), "admin1", "team-7"); !errors.Is(err, ErrTeamTaken) {
		t.Errorf("a second user claiming team-7 = %v, want ErrTeamTaken — the "+
			"route wrote the profile without taking the claim", err)
	}
}

// TestTenantSetTeam_RefusesReassignment pins cmd/user_set_team.go's setTeam
// guard: ClaimTeam writes the new claim but never releases the old one, so a
// move would leave the previous team recorded as this tenant's in the index
// while the profile says otherwise — permanently unassignable, with nothing on
// the page explaining why.
func TestTenantSetTeam_RefusesReassignment(t *testing.T) {
	h, secret, users := adminFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/team", "admin1", `{"team_id":"team-7"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("first assign = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/team", "admin1", `{"team_id":"team-9"}`))
	if rec.Code != http.StatusConflict {
		t.Errorf("reassign = %d, want 409: %s", rec.Code, rec.Body)
	}

	u, _, err := users.GetUser(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if u.TeamID != "team-7" {
		t.Errorf("bob holds team %q after a refused reassignment, want team-7 "+
			"untouched", u.TeamID)
	}

	// Re-asserting the team a tenant already holds is not a reassignment and
	// must stay a no-op success, so a double-click is not an error.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/team", "admin1", `{"team_id":"team-7"}`))
	if rec.Code != http.StatusOK {
		t.Errorf("re-assert the same team = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// TestTenantSetTeam_TeamHeldByAnotherUser: the uniqueness claim is the whole
// reason this route cannot just write the profile field.
func TestTenantSetTeam_TeamHeldByAnotherUser(t *testing.T) {
	h, secret, users := adminFixture(t)

	if err := users.ClaimTeam(context.Background(), "admin1", "team-7"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/team", "admin1", `{"team_id":"team-7"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("claim a held team = %d, want 409: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "already claimed") {
		t.Errorf("body %q does not name the taken team — the operator cannot "+
			"tell this from the reassignment refusal otherwise", rec.Body)
	}
}

// TestTenantSetTeam_RefusesEmptyTeam: an empty team is exactly the state
// connect refuses as no_team, so writing one would be a control whose only
// effect is recreating the wall it exists to clear.
func TestTenantSetTeam_RefusesEmptyTeam(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/bob/team", "admin1", `{"team_id":""}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty team = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// TestTenantSetTeam_UnknownTenant404s — a handler 404, not a mux one: a bare
// mux 404 would mean the route itself is absent.
func TestTenantSetTeam_UnknownTenant404s(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/nobody/team", "admin1", `{"team_id":"team-7"}`))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no such user") {
		t.Errorf("set team on unknown tenant = %d %q, want a handler 404 naming "+
			"the missing user", rec.Code, rec.Body)
	}
}

// TestTenantSetTeam_MemberForbidden mirrors TestTenantStatus_MemberForbidden:
// the route inherits the adminOnlyRoutes /v1/tenants prefix, and only a test
// proves the prefix actually reaches this path. Assigning teams is an admin
// assertion — a member reaching it could point their own record at somebody
// else's team.
func TestTenantSetTeam_MemberForbidden(t *testing.T) {
	h, secret := tenantsFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postJSON(t, secret, "/v1/tenants/admin1/team", "bob", `{"team_id":"team-7"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member set team = %d, want 403", rec.Code)
	}
}

// TestTenantsList_TeamIDMergesTheConnectionRecord pins the constraint
// tenants.js's team control has to live with: TenantSummary.TeamID falls back
// to the CONNECTION record's team when the profile carries none, while this
// route writes the profile. So a present team_id is not evidence the route has
// ever run for that tenant, and a UI that branched on it would label an
// unassigned profile "Change team" — walking the operator past the one control
// that repairs the tenant sitting at connect's no_team wall.
//
// Telling assignment from inheritance needs a second field on TenantSummary
// (internal/lineupapi/tenants.go). Until that exists this test is what stops
// the merge changing silently underneath the comment in tenants.js that
// justifies the unconditional label.
func TestTenantsList_TeamIDMergesTheConnectionRecord(t *testing.T) {
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
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t),
		Connections: &memConnections{conn: &FantraxConnection{
			UserID: "bob", TeamID: "team-9", Status: ConnVerified}}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/tenants", "admin1", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("list tenants = %d, want 200: %s", rec.Code, rec.Body)
	}
	var listed TenantsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode tenants: %v (%s)", err, rec.Body)
	}
	var bob *TenantSummary
	for i := range listed.Tenants {
		if listed.Tenants[i].ID == "bob" {
			bob = &listed.Tenants[i]
		}
	}
	if bob == nil {
		t.Fatal("bob missing from GET /v1/tenants")
	}
	if bob.TeamID != "team-9" {
		t.Errorf("listed team_id = %q, want the connection's team-9 — if this "+
			"merge is gone, tenants.js may label the control by team_id again",
			bob.TeamID)
	}

	u, ok, err := users.GetUser(context.Background(), "bob")
	if err != nil || !ok {
		t.Fatalf("read bob: %v ok=%v", err, ok)
	}
	if u.TeamID != "" {
		t.Errorf("bob's PROFILE team = %q, want empty — the whole hazard is that "+
			"the listed value can come from somewhere this route never wrote",
			u.TeamID)
	}
}

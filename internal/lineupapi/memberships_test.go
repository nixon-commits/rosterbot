package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// membershipFixture builds a real store over a temp dir, per me_test.go's
// convention. Emails are defaulted per user because CreateUser claims the
// address, so two users sharing an empty one collide with ErrEmailTaken.
func membershipFixture(t *testing.T, users ...*User) *FileUserStore {
	t.Helper()
	store := NewFileUserStore(t.TempDir())
	for _, u := range users {
		if u.Email == "" {
			u.Email = string(u.ID) + "@example.test"
		}
		if err := store.CreateUser(context.Background(), u); err != nil {
			t.Fatalf("CreateUser %s: %v", u.ID, err)
		}
	}
	return store
}

func TestListMembershipsIncludesProjectedFantrax(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", TeamID: "7"})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/memberships", nil)
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleListMemberships(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var body struct {
		Memberships []Membership `json:"memberships"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Memberships) != 1 || body.Memberships[0].Platform != PlatformFantrax {
		t.Errorf("memberships = %+v, want one fantrax entry", body.Memberships)
	}
}

func TestAddMembershipStoresSleeperLeague(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", TeamID: "7"})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships",
		strings.NewReader(`{"platform":"sleeper","league_id":"123","team_id":"456","display_name":"Dynasty"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	u, _, err := store.GetUser(req.Context(), "u1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(u.Memberships) != 1 {
		t.Fatalf("stored = %d memberships, want 1", len(u.Memberships))
	}
	if u.Memberships[0].LeagueID != "123" {
		t.Errorf("LeagueID = %q, want 123", u.Memberships[0].LeagueID)
	}
	if u.Memberships[0].Writable {
		t.Error("Writable = true; Sleeper has no write API and this must never be set")
	}
	if u.Memberships[0].AddedAt.IsZero() {
		t.Error("AddedAt is zero, want a stamp")
	}
}

// The whole reason this route refuses fantrax: TeamID is proven against
// Fantrax's own MyTeamIDs by the connect task, and a caller naming their own
// team would be asserting exactly the thing that proof exists to establish.
func TestAddMembershipRefusesFantrax(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1"})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships",
		strings.NewReader(`{"platform":"fantrax","league_id":"L","team_id":"9"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	u, _, _ := store.GetUser(req.Context(), "u1")
	if len(u.Memberships) != 0 {
		t.Error("a fantrax membership was stored")
	}
}

func TestAddMembershipRequiresLeagueID(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1"})}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships", strings.NewReader(`{"platform":"sleeper"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAddMembershipDuplicateIsConflict(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", Memberships: []Membership{
		{Platform: PlatformSleeper, LeagueID: "123"},
	}})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/memberships",
		strings.NewReader(`{"platform":"sleeper","league_id":"123"}`))
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

// Two tenants in the same Sleeper league must both be able to add it. The
// league data is world-readable, so exclusivity would protect nothing and
// break the common case.
func TestAddMembershipAllowsTheSameLeagueForTwoUsers(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1"}, &User{ID: "u2"})
	cfg := Config{Users: store}

	for _, uid := range []UserID{"u1", "u2"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/memberships",
			strings.NewReader(`{"platform":"sleeper","league_id":"123"}`))
		req = req.WithContext(withCaller(req.Context(), Caller{UserID: uid}))
		cfg.handleAddMembership(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", uid, rec.Code)
		}
	}
}

func TestDeleteMembershipRemovesIt(t *testing.T) {
	store := membershipFixture(t, &User{ID: "u1", Memberships: []Membership{
		{Platform: PlatformSleeper, LeagueID: "123"},
		{Platform: PlatformSleeper, LeagueID: "999"},
	}})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/memberships/sleeper/123", nil)
	req.SetPathValue("platform", "sleeper")
	req.SetPathValue("leagueID", "123")
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleDeleteMembership(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	u, _, _ := store.GetUser(req.Context(), "u1")
	if len(u.Memberships) != 1 || u.Memberships[0].LeagueID != "999" {
		t.Errorf("remaining = %+v, want only 999", u.Memberships)
	}
}

// Same rule as UserStore.DeleteCredential: the caller's intent is satisfied
// either way, and erroring invites a check-then-act race.
func TestDeleteMembershipAbsentIsNotAnError(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1"})}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/memberships/sleeper/nope", nil)
	req.SetPathValue("platform", "sleeper")
	req.SetPathValue("leagueID", "nope")
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleDeleteMembership(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestDeleteMembershipRefusesFantrax(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1", TeamID: "7"})}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/v1/memberships/fantrax/L", nil)
	req.SetPathValue("platform", "fantrax")
	req.SetPathValue("leagueID", "L")
	req = req.WithContext(withCaller(req.Context(), Caller{UserID: "u1"}))
	cfg.handleDeleteMembership(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// adminOnlyRoutes is an allowlist, so a route's absence is what makes it
// tenant-reachable. That is the quiet direction, so pin it.
func TestMembershipRoutesAreNotAdminOnly(t *testing.T) {
	for _, p := range []string{
		"/v1/memberships",
		"/v1/memberships/sleeper/123",
		"/v1/sleeper/leagues",
	} {
		if isAdminOnlyPath(p) {
			t.Errorf("isAdminOnlyPath(%q) = true; members must reach their own leagues", p)
		}
	}
}

// addMembershipReq builds a POST /v1/memberships request for the given caller.
func addMembershipReq(uid UserID, body string) *http.Request {
	req := httptest.NewRequest("POST", "/v1/memberships", strings.NewReader(body))
	return req.WithContext(withCaller(req.Context(), Caller{UserID: uid}))
}

// The stored list is re-read by resolveCaller on EVERY authenticated request,
// so an unbounded field is a per-request cost for the whole deployment, not
// just for the tenant who grew it.
func TestAddMembershipRejectsOversizedFields(t *testing.T) {
	for name, body := range map[string]string{
		"league_id": `{"platform":"sleeper","league_id":"` + strings.Repeat("1", maxLeagueIDLen+1) + `"}`,
		"team_id": `{"platform":"sleeper","league_id":"123","team_id":"` +
			strings.Repeat("4", maxTeamIDLen+1) + `"}`,
		"display_name": `{"platform":"sleeper","league_id":"123","display_name":"` +
			strings.Repeat("n", maxDisplayNameLen+1) + `"}`,
	} {
		store := membershipFixture(t, &User{ID: "u1"})
		cfg := Config{Users: store}
		rec := httptest.NewRecorder()
		req := addMembershipReq("u1", body)
		cfg.handleAddMembership(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("oversized %s: status = %d, want 400 (body %s)", name, rec.Code, rec.Body)
		}
		if u, _, _ := store.GetUser(req.Context(), "u1"); len(u.Memberships) != 0 {
			t.Errorf("oversized %s was stored anyway: %+v", name, u.Memberships)
		}
	}
}

// A field exactly at the bound is legal; the check must be > and not >=, or
// the documented limit is off by one from the enforced one.
func TestAddMembershipAcceptsFieldsAtTheBound(t *testing.T) {
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1"})}
	rec := httptest.NewRecorder()
	cfg.handleAddMembership(rec, addMembershipReq("u1",
		`{"platform":"sleeper","league_id":"`+strings.Repeat("1", maxLeagueIDLen)+
			`","team_id":"`+strings.Repeat("4", maxTeamIDLen)+
			`","display_name":"`+strings.Repeat("n", maxDisplayNameLen)+`"}`))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

// 409 rather than 400, matching the duplicate case above it: both are a
// conflict with the caller's current state, not a malformed request, so a
// client handles the whole family one way.
func TestAddMembershipCapsTheStoredList(t *testing.T) {
	full := make([]Membership, maxMemberships)
	for i := range full {
		full[i] = Membership{Platform: PlatformSleeper, LeagueID: strconv.Itoa(i)}
	}
	store := membershipFixture(t, &User{ID: "u1", Memberships: full})
	cfg := Config{Users: store}

	rec := httptest.NewRecorder()
	req := addMembershipReq("u1", `{"platform":"sleeper","league_id":"one-too-many"}`)
	cfg.handleAddMembership(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if u, _, _ := store.GetUser(req.Context(), "u1"); len(u.Memberships) != maxMemberships {
		t.Errorf("stored = %d memberships, want the cap of %d", len(u.Memberships), maxMemberships)
	}
}

// One below the cap must still succeed, so the limit admits exactly
// maxMemberships entries rather than one fewer.
func TestAddMembershipAllowsTheLastSlotUnderTheCap(t *testing.T) {
	full := make([]Membership, maxMemberships-1)
	for i := range full {
		full[i] = Membership{Platform: PlatformSleeper, LeagueID: strconv.Itoa(i)}
	}
	cfg := Config{Users: membershipFixture(t, &User{ID: "u1", Memberships: full})}

	rec := httptest.NewRecorder()
	cfg.handleAddMembership(rec, addMembershipReq("u1",
		`{"platform":"sleeper","league_id":"last"}`))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
}

// Everything above drives cfg.handleX directly and hands the DELETE its path
// values by hand. That can never see the one failure this feature is most
// exposed to: a route that was never registered, a pattern scoped to the wrong
// method, or a {leagueID} in handler.go that disagrees with the
// r.PathValue("leagueID") read out of it. Each of those compiles, passes every
// direct-call test in this file, and in production either 404s or silently
// deletes nothing. TestMembershipRoutesAreNotAdminOnly does not cover it
// either — it tests the isAdminOnlyPath predicate, and would pass with all
// four routes unregistered.
//
// So the four tests below go through Handler, exactly as pushroutes_test.go
// does for the analogous push trio.
func membershipRouter(t *testing.T, seed ...*User) (http.Handler, []byte, *FileUserStore, *fakeSleeperDir) {
	t.Helper()
	for _, u := range seed {
		u.Role, u.Status = RoleMember, UserActive
	}
	store := membershipFixture(t, seed...)
	dir := &fakeSleeperDir{leagues: []SleeperLeague{
		{LeagueID: "1", Name: "Dynasty", Season: "2026", TotalRosters: 12, TeamID: "456"},
	}}
	secret := []byte("s")
	h := Handler(Config{
		Token: "op-token", Users: store, Enrollments: store, SleeperDir: dir,
		SessionSecret: secret, WebAuthn: testWebAuthn(t),
	})
	return h, secret, store, dir
}

func TestSleeperLeaguesRouteIsRegistered(t *testing.T) {
	h, secret, _, dir := membershipRouter(t, &User{ID: "u1"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet,
		"/v1/sleeper/leagues?username=jnixon", "u1", 0))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if dir.gotUser != "jnixon" {
		t.Errorf("directory saw username %q, want jnixon", dir.gotUser)
	}
	if !strings.Contains(rec.Body.String(), "Dynasty") {
		t.Errorf("body = %s, want the discovered league", rec.Body)
	}
}

func TestListMembershipsRouteIsRegistered(t *testing.T) {
	h, secret, _, _ := membershipRouter(t, &User{ID: "u1", TeamID: "7"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/memberships", "u1", 0))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"platform":"fantrax"`) {
		t.Errorf("body = %s, want the projected fantrax membership", rec.Body)
	}
}

func TestAddMembershipRouteIsRegistered(t *testing.T) {
	h, secret, store, _ := membershipRouter(t, &User{ID: "u1"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pushJSON(t, secret, http.MethodPost, "/v1/memberships", "u1",
		`{"platform":"sleeper","league_id":"123","display_name":"Dynasty"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	u, _, _ := store.GetUser(t.Context(), "u1")
	if len(u.Memberships) != 1 || u.Memberships[0].LeagueID != "123" {
		t.Errorf("stored = %+v, want the posted league", u.Memberships)
	}
}

// The one that matters most: {platform} and {leagueID} in the pattern have to
// arrive as the values handleDeleteMembership reads by those exact names, and
// only the named league may go.
func TestDeleteMembershipRouteCarriesItsPathValues(t *testing.T) {
	h, secret, store, _ := membershipRouter(t, &User{ID: "u1", Memberships: []Membership{
		{Platform: PlatformSleeper, LeagueID: "123"},
		{Platform: PlatformSleeper, LeagueID: "999"},
	}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodDelete, "/v1/memberships/sleeper/123", "u1", 0))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	u, _, _ := store.GetUser(t.Context(), "u1")
	if len(u.Memberships) != 1 || u.Memberships[0].LeagueID != "999" {
		t.Fatalf("remaining = %+v, want only 999 — the path values did not arrive intact",
			u.Memberships)
	}

	// And the patterns are method-scoped: the two-segment path exists only
	// under DELETE, so a GET must not fall through to the list handler and
	// answer 200 with somebody's memberships.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/memberships/sleeper/123", "u1", 0))
	if rec.Code == http.StatusOK {
		t.Errorf("GET on the delete path answered 200 (%s); the patterns are not method-scoped",
			rec.Body)
	}
}

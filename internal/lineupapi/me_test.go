package lineupapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func meFixture(t *testing.T, u *User) (http.Handler, *FileUserStore, []byte) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t), Connections: &memConnections{}})
	return h, users, secret
}

func getMe(t *testing.T, h http.Handler, secret []byte, uid UserID) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", uid, 0))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestMe_ReturnsTheCallersOwnProfile. The settings page and the admin tab both
// need to know who the caller is; nothing served that today, which is why the
// SPA had no admin-conditional UI at all.
func TestMe_ReturnsTheCallersOwnProfile(t *testing.T) {
	h, _, secret := meFixture(t, &User{ID: "alice", DisplayName: "Alice",
		Email: "alice@example.test", Role: RoleMember, Status: UserActive,
		TeamID: "team-7", AutoApply: true})

	code, body := getMe(t, h, secret, "alice")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for k, want := range map[string]any{
		"id": "alice", "display_name": "Alice", "email": "alice@example.test",
		"role": "member", "team_id": "team-7", "auto_apply": true,
	} {
		if body[k] != want {
			t.Errorf("%s = %v, want %v", k, body[k], want)
		}
	}
}

// TestMe_NeverReturnsSecrets. This response is the natural place for a future
// field to be added carelessly, and it is the one a browser sees. The
// ciphertexts are opaque, but an endpoint that emits them invites a client that
// stores them; token_version is an internal revocation counter.
func TestMe_NeverReturnsSecrets(t *testing.T) {
	h, _, secret := meFixture(t, &User{ID: "alice", Email: "a@example.test",
		Role: RoleMember, Status: UserActive})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/me", "alice", 0))

	for _, forbidden := range []string{"creds_ct", "fx_rm_ct", "token_version", "password"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("GET /v1/me leaked %q: %s", forbidden, rec.Body)
		}
	}
}

// TestMe_CarriesTheRoleSoTheAdminTabCanExist. The SPA never learned the
// caller's role, so an admin-only Tenants tab had no signal to key on. This is
// that signal, and it is deliberately advisory: the server still enforces
// admin-only routes itself (adminOnlyRoutes), so a member editing this response
// in their browser gains nothing but a tab that 403s.
func TestMe_CarriesTheRoleSoTheAdminTabCanExist(t *testing.T) {
	for _, tc := range []struct {
		role Role
		want string
	}{{RoleAdmin, "admin"}, {RoleMember, "member"}} {
		h, _, secret := meFixture(t, &User{ID: "u", Email: string(tc.role) + "@e.test",
			Role: tc.role, Status: UserActive})
		_, body := getMe(t, h, secret, "u")
		if body["role"] != tc.want {
			t.Errorf("role = %v, want %v", body["role"], tc.want)
		}
	}
}

// TestMe_RefusesTheTokenCaller. The bearer token has no UserID by construction,
// so "me" has no answer for it. Resolving it to the operator would make a
// break-glass credential look like a person.
func TestMe_RefusesTheTokenCaller(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	h := Handler(Config{Token: "tok", Users: users, Enrollments: users,
		SessionSecret: []byte("s"), WebAuthn: testWebAuthn(t)})

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/me", nil)
	r.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("token caller GET /v1/me = %d, want 403", rec.Code)
	}
}

// TestSetAutoApply_IsTheUsersOwnDecision is the write half of the settings
// page, and the only control that changes whether the bot writes to a roster.
func TestSetAutoApply_IsTheUsersOwnDecision(t *testing.T) {
	h, users, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive, AutoApply: false})

	rec := httptest.NewRecorder()
	r := signedReq(t, secret, http.MethodPost, "/v1/me/preferences", "alice", 0)
	r.Body = http.NoBody
	r2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/me/preferences",
		strings.NewReader(`{"auto_apply":true}`))
	r2.Header = r.Header
	h.ServeHTTP(rec, r2)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := users.GetUser(context.Background(), "alice")
	if !got.AutoApply {
		t.Error("auto_apply not persisted; the toggle would silently do nothing")
	}
}

// TestSetAutoApply_CannotChangeAnyoneElse. The caller is taken from the
// session, never from the body — otherwise the one control that decides whether
// the bot writes to a roster would be settable for somebody else's.
func TestSetAutoApply_CannotChangeAnyoneElse(t *testing.T) {
	h, users, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive})
	if err := users.CreateUser(context.Background(), &User{ID: "bob",
		Email: "b@e.test", Role: RoleMember, Status: UserActive}); err != nil {
		t.Fatal(err)
	}

	r := signedReq(t, secret, http.MethodPost, "/v1/me/preferences", "alice", 0)
	r2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/me/preferences",
		strings.NewReader(`{"auto_apply":true,"user_id":"bob","id":"bob"}`))
	r2.Header = r.Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r2)

	bob, _, _ := users.GetUser(context.Background(), "bob")
	if bob.AutoApply {
		t.Error("alice turned on auto_apply for bob; the subject must come from the session")
	}
}

// postPrefs sends a signed POST /v1/me/preferences with the given JSON body.
func postPrefs(t *testing.T, h http.Handler, secret []byte, uid UserID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := signedReq(t, secret, http.MethodPost, "/v1/me/preferences", uid, 0)
	r2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/me/preferences", strings.NewReader(body))
	r2.Header = r.Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r2)
	return rec
}

// TestSetProjectionPrefs_PersistsPerRoleChoice: the settings page lets a user
// pick the projection system their lineup runs use, per role, so the two
// choices must land on the record independently (rosterbot-5qvs).
func TestSetProjectionPrefs_PersistsPerRoleChoice(t *testing.T) {
	h, users, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive})

	rec := postPrefs(t, h, secret, "alice",
		`{"hitter_projection":"atc","pitcher_projection":"steamer"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := users.GetUser(context.Background(), "alice")
	if got.HitterProjection != "atc" || got.PitcherProjection != "steamer" {
		t.Errorf("stored prefs = %q/%q, want atc/steamer",
			got.HitterProjection, got.PitcherProjection)
	}
}

// TestSetProjectionPrefs_RejectsUnknownSystem: write-time validation is the
// contract that lets the run side treat a stored value as trustworthy. A name
// the loaders don't know must never reach the record.
func TestSetProjectionPrefs_RejectsUnknownSystem(t *testing.T) {
	for _, bad := range []string{"zips", "depthcharts-ros"} {
		h, users, secret := meFixture(t, &User{ID: "u", Email: bad + "@e.test",
			Role: RoleMember, Status: UserActive})

		rec := postPrefs(t, h, secret, "u", `{"hitter_projection":"`+bad+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", bad, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "projection") {
			t.Errorf("%q: error %s does not name the projection field", bad, rec.Body)
		}
		got, _, _ := users.GetUser(context.Background(), "u")
		if got.HitterProjection != "" {
			t.Errorf("%q reached the record", bad)
		}
	}
}

// TestSetProjectionPrefs_EmptyStringResetsToDefault: "" means "follow the
// deployment default" (empty-means-default was the design choice, so future
// default changes reach everyone who never chose). Absent means untouched —
// the auto_apply pointer rule extended to these fields.
func TestSetProjectionPrefs_EmptyStringResetsToDefault(t *testing.T) {
	h, users, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive,
		HitterProjection: "atc", PitcherProjection: "steamer"})

	rec := postPrefs(t, h, secret, "alice", `{"hitter_projection":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := users.GetUser(context.Background(), "alice")
	if got.HitterProjection != "" {
		t.Errorf("hitter_projection = %q, want cleared", got.HitterProjection)
	}
	if got.PitcherProjection != "steamer" {
		t.Errorf("pitcher_projection = %q; an absent field must stay untouched",
			got.PitcherProjection)
	}
}

// TestSetPreferences_AbsentFieldsLeavePrefsUntouched: a request changing only
// auto_apply must not clear a projection choice — the same reason auto_apply
// itself is a pointer in the body.
func TestSetPreferences_AbsentFieldsLeavePrefsUntouched(t *testing.T) {
	h, users, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive, HitterProjection: "thebatx"})

	rec := postPrefs(t, h, secret, "alice", `{"auto_apply":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got, _, _ := users.GetUser(context.Background(), "alice")
	if got.HitterProjection != "thebatx" || !got.AutoApply {
		t.Errorf("got prefs %q auto_apply=%v; want thebatx/true",
			got.HitterProjection, got.AutoApply)
	}
}

// TestSetPreferences_EmptyBodyRejected pins the "no preference supplied"
// refusal now that the body has three optional fields.
func TestSetPreferences_EmptyBodyRejected(t *testing.T) {
	h, _, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive})
	if rec := postPrefs(t, h, secret, "alice", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: status %d, want 400", rec.Code)
	}
}

// TestMe_CarriesProjectionPrefs: the settings page renders the dropdowns from
// GET /v1/me, so a saved choice must round-trip or the UI shows Default
// forever.
func TestMe_CarriesProjectionPrefs(t *testing.T) {
	h, _, secret := meFixture(t, &User{ID: "alice", Email: "a@e.test",
		Role: RoleMember, Status: UserActive,
		HitterProjection: "atc", PitcherProjection: "steamer"})

	_, body := getMe(t, h, secret, "alice")
	if body["hitter_projection"] != "atc" || body["pitcher_projection"] != "steamer" {
		t.Errorf("me = %v/%v, want atc/steamer",
			body["hitter_projection"], body["pitcher_projection"])
	}
}

// TestSetAutoApply_PreservesTheRestOfTheProfile: PutUser stores a whole record,
// so a handler that rebuilds one erases the proven team binding, the attested
// email and the role.
func TestSetAutoApply_PreservesTheRestOfTheProfile(t *testing.T) {
	h, users, secret := meFixture(t, &User{ID: "alice", DisplayName: "Alice",
		Email: "a@e.test", Role: RoleAdmin, Status: UserActive, TeamID: "team-7"})

	r := signedReq(t, secret, http.MethodPost, "/v1/me/preferences", "alice", 0)
	r2 := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/me/preferences",
		strings.NewReader(`{"auto_apply":true}`))
	r2.Header = r.Header
	h.ServeHTTP(httptest.NewRecorder(), r2)

	got, _, _ := users.GetUser(context.Background(), "alice")
	if got.TeamID != "team-7" || got.Role != RoleAdmin || got.DisplayName != "Alice" {
		t.Errorf("profile lost fields: %+v", got)
	}
}

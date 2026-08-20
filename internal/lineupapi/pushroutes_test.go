package lineupapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pushFixture wires a handler whose Users and PushDevices share one file
// store, with an active member to hold a session.
func pushFixture(t *testing.T) (http.Handler, []byte, *FileUserStore) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	for _, u := range []*User{
		{ID: "u1", Email: "u1@example.test", Role: RoleMember, Status: UserActive},
		{ID: "u2", Email: "u2@example.test", Role: RoleMember, Status: UserActive},
	} {
		if err := users.CreateUser(t.Context(), u); err != nil {
			t.Fatalf("CreateUser(%s): %v", u.ID, err)
		}
	}
	secret := []byte("s")
	h := Handler(Config{Token: "op-token", Users: users, Enrollments: users,
		PushDevices: users, SessionSecret: secret, WebAuthn: testWebAuthn(t)})
	return h, secret, users
}

// pushJSON builds an authenticated JSON request with a method, the way
// postJSON does for POST.
func pushJSON(t *testing.T, secret []byte, method, path, uid, body string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	setSessionCookie(rec, secret, UserID(uid), 0, time.Now())
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

const validRegisterBody = `{"token":"aabbcc","environment":"sandbox","bundle_id":"dev.rosterbot.app.debug","model":"iPhone17,1"}`

func TestRegisterDevice_RequiresASession(t *testing.T) {
	h, _, _ := pushFixture(t)

	// No credentials at all: refused at the gate.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/push/devices",
		strings.NewReader(validRegisterBody)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without a session, got %d", rec.Code)
	}

	// The operator bearer token authenticates but has no UserID by
	// construction, so there is no account a device could belong to. Same
	// rule handleConnect enforces.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/push/devices", strings.NewReader(validRegisterBody))
	req.Header.Set("Authorization", "Bearer op-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 for the bearer token, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestRegisterDevice_RejectsBadInput(t *testing.T) {
	h, secret, _ := pushFixture(t)
	for name, body := range map[string]string{
		// Only sandbox and production exist. Accepting anything else stores a
		// record the sender cannot route, which surfaces later as an
		// undeliverable device rather than as a rejected registration.
		"unknown environment": `{"token":"aa","environment":"staging","bundle_id":"dev.rosterbot.app"}`,
		"missing token":       `{"environment":"sandbox","bundle_id":"dev.rosterbot.app"}`,
		"missing bundle_id":   `{"token":"aa","environment":"sandbox"}`,
		"not json":            `not json`,
		"oversized token":     `{"token":"` + strings.Repeat("a", 600) + `","environment":"sandbox","bundle_id":"dev.rosterbot.app"}`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, pushJSON(t, secret, http.MethodPost, "/v1/push/devices", "u1", body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestRegisterThenListThenRevoke(t *testing.T) {
	h, secret, _ := pushFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pushJSON(t, secret, http.MethodPost, "/v1/push/devices", "u1", validRegisterBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("register: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var reg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if reg.ID == "" {
		t.Fatal("register must return the device id; the client persists it to revoke on sign-out")
	}

	// Re-registering the same token must answer the SAME id: the shipped
	// client relies on this to keep its persisted id stable across launches.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, pushJSON(t, secret, http.MethodPost, "/v1/push/devices", "u1", validRegisterBody))
	var again struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if again.ID != reg.ID {
		t.Fatalf("re-registration answered a different id: %q then %q", reg.ID, again.ID)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/push/devices", "u1", 0))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), reg.ID) {
		t.Fatalf("list: want the registered device, got %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodDelete, "/v1/push/devices/"+reg.ID, "u1", 0))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d", rec.Code)
	}
	// The client's helper tolerates an empty 2xx and nothing more; a 204 must
	// carry no body at all.
	if rec.Body.Len() != 0 {
		t.Fatalf("revoke: 204 must have an empty body, got %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/push/devices", "u1", 0))
	if strings.Contains(rec.Body.String(), reg.ID) {
		t.Fatalf("revoked device still listed: %s", rec.Body.String())
	}
}

func TestRevoke_IsScopedToTheCaller(t *testing.T) {
	h, secret, _ := pushFixture(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pushJSON(t, secret, http.MethodPost, "/v1/push/devices", "u1", validRegisterBody))
	var reg struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reg)

	// u2 deleting u1's device id must delete nothing: the store key is
	// (caller, id), so someone else's id simply is not there.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodDelete, "/v1/push/devices/"+reg.ID, "u2", 0))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cross-user revoke: want 204 (absent = success), got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/push/devices", "u1", 0))
	if !strings.Contains(rec.Body.String(), reg.ID) {
		t.Fatalf("u1's device must survive u2's revoke: %s", rec.Body.String())
	}
}

func TestList_NeverLeaksTheAPNsToken(t *testing.T) {
	h, secret, _ := pushFixture(t)

	h.ServeHTTP(httptest.NewRecorder(), pushJSON(t, secret, http.MethodPost, "/v1/push/devices", "u1",
		`{"token":"secret-token-value","environment":"sandbox","bundle_id":"dev.rosterbot.app.debug"}`))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(t, secret, http.MethodGet, "/v1/push/devices", "u1", 0))

	// The listing exists so a person can see and revoke their devices. The
	// raw token is a delivery credential and has no business in that view.
	if strings.Contains(rec.Body.String(), "secret-token-value") {
		t.Fatalf("device listing leaked the APNs token: %s", rec.Body.String())
	}
}

func TestPushRoutes_UnconfiguredStoreAnswers501(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(t.Context(), &User{ID: "u1", Email: "u1@example.test",
		Role: RoleMember, Status: UserActive}); err != nil {
		t.Fatal(err)
	}
	secret := []byte("s")
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: secret,
		WebAuthn: testWebAuthn(t)}) // PushDevices deliberately nil

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pushJSON(t, secret, http.MethodPost, "/v1/push/devices", "u1", validRegisterBody))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 with no device store, got %d", rec.Code)
	}
}

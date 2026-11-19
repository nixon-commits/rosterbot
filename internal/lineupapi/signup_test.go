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

// signupFixture is a handler over a real file store with NO caller on any
// request: signup is the route nobody is signed in for.
// signupConns is a per-user connection fake: memConnections holds one record
// for whoever asks, which cannot express "this account has a connection and
// that one does not".
type signupConns struct {
	byUser map[UserID]*FantraxConnection
	getErr error
}

func (c *signupConns) GetConnection(_ context.Context, id UserID) (*FantraxConnection, bool, error) {
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	conn, ok := c.byUser[id]
	return conn, ok, nil
}

func (c *signupConns) PutConnection(context.Context, *FantraxConnection) error { return nil }

func signupFixture(t *testing.T) (http.Handler, *FileUserStore, *signupConns, *Config) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	conns := &signupConns{}
	cfg := &Config{Users: users, Enrollments: users, SessionSecret: []byte("s"),
		WebAuthn: testWebAuthn(t), Connections: conns}
	return Handler(*cfg), users, conns, cfg
}

func postSignup(t *testing.T, h http.Handler, body string) (*httptest.ResponseRecorder, signupOut) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/signup", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out signupOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// seedUser writes a user directly, as an invite or an earlier signup would
// have, with a chosen age.
func seedSignupUser(t *testing.T, users *FileUserStore, u *User, passkey bool) {
	t.Helper()
	if u.Role == "" {
		u.Role = RoleMember
	}
	if u.Status == "" {
		u.Status = UserActive
	}
	if err := users.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("seed %s: %v", u.ID, err)
	}
	if passkey {
		if err := users.PutCredential(context.Background(), u.ID,
			webauthn.Credential{ID: []byte("cred-" + string(u.ID))}); err != nil {
			t.Fatalf("seed credential for %s: %v", u.ID, err)
		}
	}
}

func userExists(t *testing.T, users *FileUserStore, id UserID) bool {
	t.Helper()
	_, ok, err := users.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUser(%s): %v", id, err)
	}
	return ok
}

func atTime(t *testing.T, now time.Time) {
	t.Helper()
	prev := signupNow
	signupNow = func() time.Time { return now }
	t.Cleanup(func() { signupNow = prev })
}

// TestSignup_CreatesAnInertAccountWithARedeemableEnrollment is the bead's first
// acceptance line, and the reachability claim: NO cookie, NO token, 201.
func TestSignup_CreatesAnInertAccountWithARedeemableEnrollment(t *testing.T) {
	h, users, _, cfg := signupFixture(t)

	rec, out := postSignup(t, h, `{"email":"  New@Example.Test ","display_name":" New Person "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup = %d, want 201: %s", rec.Code, rec.Body)
	}
	if out.UserID == "" || out.EnrollmentToken == "" {
		t.Fatalf("response missing user_id or enrollment_token: %s", rec.Body)
	}

	u, ok, err := users.GetUser(context.Background(), out.UserID)
	if err != nil || !ok {
		t.Fatalf("signed-up user not in store: ok=%v err=%v", ok, err)
	}
	if u.Status != UserActive || u.Role != RoleMember || u.TeamID != "" || u.AutoApply {
		t.Errorf("user = status %q role %q team %q auto_apply %v, want active/member/no team/false",
			u.Status, u.Role, u.TeamID, u.AutoApply)
	}
	if u.Origin != UserOriginSignup {
		t.Errorf("Origin = %q, want %q — the cap and cleanup key on it", u.Origin, UserOriginSignup)
	}
	if u.Email != "new@example.test" || u.DisplayName != "New Person" {
		t.Errorf("stored email %q name %q, want them trimmed and the email lower-cased", u.Email, u.DisplayName)
	}
	if u.EmailAttestedBy != "" || !u.EmailAttestedAt.IsZero() {
		t.Errorf("email attested by %q at %v; nobody vouched for a self-entered address",
			u.EmailAttestedBy, u.EmailAttestedAt)
	}

	// The token admits exactly that user to the passkey ceremony...
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
	subject, ok := cfg.registrationSubject(req, out.EnrollmentToken)
	if !ok || subject.ID != out.UserID {
		t.Fatalf("registrationSubject = (%v, %v), want the signed-up user", subject, ok)
	}
	// ...and is short-lived: it is spent at once, unlike a 72-hour invite.
	e, ok, err := users.GetEnrollment(context.Background(), HashEnrollmentToken(EnrollmentToken(out.EnrollmentToken)))
	if err != nil || !ok {
		t.Fatalf("GetEnrollment: ok=%v err=%v", ok, err)
	}
	if ttl := time.Until(e.ExpiresAt); ttl <= 0 || ttl > signupEnrollmentTTL+time.Minute {
		t.Errorf("enrollment expires in %v, want at most %v", ttl, signupEnrollmentTTL)
	}
	if e.TeamID != "" {
		t.Errorf("enrollment asserts team %q; signup names none", e.TeamID)
	}
}

// TestSignup_AFreshAccountCannotRun pins spec D2: the safety property lives on
// the connection record, so a signed-up account is denied at AuthorizeRun's
// first check.
func TestSignup_AFreshAccountCannotRun(t *testing.T) {
	h, _, conns, _ := signupFixture(t)
	rec, out := postSignup(t, h, `{"email":"new@example.test"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup = %d: %s", rec.Code, rec.Body)
	}

	conn, ok, err := conns.GetConnection(context.Background(), out.UserID)
	if err != nil || ok {
		t.Fatalf("a fresh signup has a connection record (ok=%v err=%v); signup must not write one", ok, err)
	}
	_, reason := AuthorizeRun(out.UserID, conn)
	if reason != RunDeniedNoConnection {
		t.Fatalf("AuthorizeRun reason = %q, want %q", reason, RunDeniedNoConnection)
	}
}

// TestSignup_IsTheOnlyRouteThatSkipsAuthorization: the bypass is an exact
// path. A session-less request anywhere else still answers 401, including
// paths that merely start with the same text.
func TestSignup_IsTheOnlyRouteThatSkipsAuthorization(t *testing.T) {
	h, _, _, _ := signupFixture(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/me"},
		{http.MethodPost, "/v1/me/preferences"},
		{http.MethodPost, "/v1/connect"},
		{http.MethodGet, "/v1/tenants"},
		{http.MethodPost, "/v1/signup/"},
		{http.MethodPost, "/v1/signup/anything"},
		{http.MethodPost, "/v1/signups"},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no caller = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}

	// And the open path creates nothing on any other verb.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/signup", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/signup = %d, want 405", rec.Code)
	}
}

func TestSignup_RefusesBadInput(t *testing.T) {
	h, users, _, _ := signupFixture(t)
	for name, body := range map[string]string{
		"not json":            `{`,
		"no email":            `{"display_name":"x"}`,
		"blank email":         `{"email":"   "}`,
		"no at sign":          `{"email":"nobody"}`,
		"no dotted domain":    `{"email":"a@localhost"}`,
		"display-name form":   `{"email":"Mallory <m@example.test>"}`,
		"two addresses":       `{"email":"a@example.test, b@example.test"}`,
		"overlong email":      `{"email":"` + strings.Repeat("a", 250) + `@example.test"}`,
		"overlong name":       `{"email":"a@example.test","display_name":"` + strings.Repeat("n", 81) + `"}`,
		"body over the limit": `{"email":"a@example.test","display_name":"` + strings.Repeat("n", 5000) + `"}`,
	} {
		rec, _ := postSignup(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, rec.Code, rec.Body)
		}
	}
	all, err := users.ListUsers(context.Background())
	if err != nil || len(all) != 0 {
		t.Fatalf("refused requests left %d users behind (err %v)", len(all), err)
	}
}

func TestSignup_Unconfigured501s(t *testing.T) {
	h := Handler(Config{SessionSecret: []byte("s")})
	rec, _ := postSignup(t, h, `{"email":"a@example.test"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d, want 501", rec.Code)
	}
}

// TestSignup_DuplicateEmail covers the 409 and the one exception to it: an
// attempt that signup itself created and that never got a passkey is replaced.
func TestSignup_DuplicateEmail(t *testing.T) {
	now := time.Now().UTC()

	t.Run("an invited account conflicts and survives", func(t *testing.T) {
		h, users, _, _ := signupFixture(t)
		seedSignupUser(t, users, &User{ID: "invited", Email: "Taken@Example.Test", CreatedAt: now}, false)

		rec, _ := postSignup(t, h, `{"email":"taken@example.test"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
		}
		if !userExists(t, users, "invited") {
			t.Fatal("signup deleted an INVITED account that had no passkey yet")
		}
	})

	t.Run("a signed-up account with a passkey conflicts and survives", func(t *testing.T) {
		h, users, _, _ := signupFixture(t)
		seedSignupUser(t, users, &User{ID: "real", Email: "taken@example.test",
			Origin: UserOriginSignup, CreatedAt: now}, true)

		rec, _ := postSignup(t, h, `{"email":"taken@example.test"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
		}
		if !userExists(t, users, "real") {
			t.Fatal("signup deleted an account that has a passkey")
		}
	})

	t.Run("a signed-up account with a proven team conflicts and survives", func(t *testing.T) {
		h, users, _, _ := signupFixture(t)
		seedSignupUser(t, users, &User{ID: "proven", Email: "taken@example.test",
			Origin: UserOriginSignup, TeamID: "team-7", CreatedAt: now}, false)

		rec, _ := postSignup(t, h, `{"email":"taken@example.test"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
		}
		if !userExists(t, users, "proven") {
			t.Fatal("signup deleted an account that holds a team")
		}
	})

	t.Run("an attempt still in its ceremony is NOT replaced", func(t *testing.T) {
		// Somebody who merely knows the address must not be able to delete an
		// account out from under an open passkey sheet.
		start := time.Now().UTC()
		atTime(t, start)
		h, users, _, cfg := signupFixture(t)
		first, a := postSignup(t, h, `{"email":"me@example.test"}`)
		if first.Code != http.StatusCreated {
			t.Fatalf("first signup = %d: %s", first.Code, first.Body)
		}

		atTime(t, start.Add(signupReplaceAfter-time.Second))
		second, _ := postSignup(t, h, `{"email":"ME@example.test"}`)
		if second.Code != http.StatusConflict {
			t.Fatalf("repeat signup inside the window = %d, want 409: %s", second.Code, second.Body)
		}
		if !userExists(t, users, a.UserID) {
			t.Fatal("the in-progress attempt was deleted")
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
		if _, ok := cfg.registrationSubject(req, a.EnrollmentToken); !ok {
			t.Error("the in-progress attempt's token stopped working")
		}
	})

	t.Run("an abandoned attempt is replaced and its token dies", func(t *testing.T) {
		start := time.Now().UTC()
		atTime(t, start)
		h, users, _, cfg := signupFixture(t)
		first, a := postSignup(t, h, `{"email":"me@example.test"}`)
		if first.Code != http.StatusCreated {
			t.Fatalf("first signup = %d: %s", first.Code, first.Body)
		}

		atTime(t, start.Add(signupReplaceAfter+time.Second))
		second, b := postSignup(t, h, `{"email":"ME@example.test"}`)
		if second.Code != http.StatusCreated {
			t.Fatalf("repeat signup = %d, want 201: %s", second.Code, second.Body)
		}
		if b.UserID == a.UserID {
			t.Fatal("the repeat signup reused the abandoned account's id")
		}
		if userExists(t, users, a.UserID) {
			t.Error("the abandoned account was not removed")
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/auth/register/begin", nil)
		if _, ok := cfg.registrationSubject(req, a.EnrollmentToken); ok {
			t.Error("the abandoned attempt's token still admits a passkey ceremony")
		}
		if _, ok := cfg.registrationSubject(req, b.EnrollmentToken); !ok {
			t.Error("the new token does not admit a passkey ceremony")
		}
	})
}

// TestSignup_Cleanup pins exactly which accounts the in-request cleanup may
// delete. Every row that survives here is a row a wrong predicate would have
// taken from a real person.
func TestSignup_Cleanup(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	atTime(t, now)
	h, users, conns, _ := signupFixture(t)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	// Signup accounts.
	seedSignupUser(t, users, &User{ID: "s-nokey-2h", Email: "a@x.test", Origin: UserOriginSignup, CreatedAt: ago(2 * time.Hour)}, false)
	seedSignupUser(t, users, &User{ID: "s-nokey-30m", Email: "b@x.test", Origin: UserOriginSignup, CreatedAt: ago(30 * time.Minute)}, false)
	seedSignupUser(t, users, &User{ID: "s-key-8d-noconn", Email: "c@x.test", Origin: UserOriginSignup, CreatedAt: ago(8 * 24 * time.Hour)}, true)
	seedSignupUser(t, users, &User{ID: "s-key-6d-noconn", Email: "d@x.test", Origin: UserOriginSignup, CreatedAt: ago(6 * 24 * time.Hour)}, true)
	seedSignupUser(t, users, &User{ID: "s-key-8d-conn", Email: "e@x.test", Origin: UserOriginSignup, CreatedAt: ago(8 * 24 * time.Hour)}, true)
	seedSignupUser(t, users, &User{ID: "s-key-6d-conn", Email: "e2@x.test", Origin: UserOriginSignup, CreatedAt: ago(6 * 24 * time.Hour)}, true)
	seedSignupUser(t, users, &User{ID: "s-key-nodate", Email: "e3@x.test", Origin: UserOriginSignup}, true)
	seedSignupUser(t, users, &User{ID: "s-proven-old", Email: "f@x.test", Origin: UserOriginSignup, TeamID: "team-1", CreatedAt: ago(90 * 24 * time.Hour)}, false)
	seedSignupUser(t, users, &User{ID: "s-nokey-nodate", Email: "g@x.test", Origin: UserOriginSignup}, false)
	// Accounts an admin made: never counted, never deleted.
	seedSignupUser(t, users, &User{ID: "invited-old", Email: "h@x.test", CreatedAt: ago(30 * 24 * time.Hour)}, false)
	seedSignupUser(t, users, &User{ID: "admin", Email: "i@x.test", Role: RoleAdmin, CreatedAt: ago(300 * 24 * time.Hour)}, false)

	conns.byUser = map[UserID]*FantraxConnection{
		"s-key-8d-conn": {UserID: "s-key-8d-conn", Status: ConnNeedsReconnect},
		"s-key-6d-conn": {UserID: "s-key-6d-conn", Status: ConnNeedsReconnect},
	}

	rec, _ := postSignup(t, h, `{"email":"new@x.test"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup = %d: %s", rec.Code, rec.Body)
	}

	for id, want := range map[UserID]bool{
		"s-nokey-2h":      false, // no passkey after an hour
		"s-nokey-30m":     true,  // still inside the hour
		"s-key-8d-noconn": false, // no proven team in a week
		"s-key-6d-noconn": true,  // still inside the week
		"s-key-8d-conn":   false, // a FAILED connection does not hold a slot for ever
		"s-key-6d-conn":   true,  // still inside the week
		"s-key-nodate":    true,  // age unknown: keep
		"s-proven-old":    true,  // holds a team
		"s-nokey-nodate":  true,  // age unknown: keep
		"invited-old":     true,
		"admin":           true,
	} {
		if got := userExists(t, users, id); got != want {
			t.Errorf("%s exists = %v, want %v", id, got, want)
		}
	}
}

// TestSignup_TheCapCannotBeHeldFullForEver is the review finding that changed
// the cleanup: fifty accounts with a passkey and a failed connection used to
// count against the cap at any age, closing signup until an admin intervened.
func TestSignup_TheCapCannotBeHeldFullForEver(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	atTime(t, now)
	users := NewFileUserStore(t.TempDir())
	conns := &signupConns{byUser: map[UserID]*FantraxConnection{}}
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: []byte("s"),
		WebAuthn: testWebAuthn(t), Connections: conns, SignupMaxUnproven: 3})
	for _, id := range []UserID{"h1", "h2", "h3"} {
		seedSignupUser(t, users, &User{ID: id, Email: string(id) + "@x.test",
			Origin: UserOriginSignup, CreatedAt: now.Add(-time.Hour)}, true)
		conns.byUser[id] = &FantraxConnection{UserID: id, Status: ConnNeedsReconnect, LastError: ConnErrBadCredentials}
	}

	if rec, _ := postSignup(t, h, `{"email":"new@x.test"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("signup at the cap = %d, want 429", rec.Code)
	}
	atTime(t, now.Add(signupUnprovenTTL))
	if rec, _ := postSignup(t, h, `{"email":"new@x.test"}`); rec.Code != http.StatusCreated {
		t.Fatalf("signup a week later = %d, want 201: the held slots must expire: %s", rec.Code, rec.Body)
	}
}

// TestSignup_Cap: the route stops creating rows at the cap, counts only
// unproven signup accounts toward it, and frees up when the cleanup does.
func TestSignup_Cap(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	atTime(t, now)
	users := NewFileUserStore(t.TempDir())
	h := Handler(Config{Users: users, Enrollments: users, SessionSecret: []byte("s"),
		WebAuthn: testWebAuthn(t), Connections: &memConnections{}, SignupMaxUnproven: 2})

	// Neither of these counts: one is invited, one holds a team.
	seedSignupUser(t, users, &User{ID: "invited", Email: "inv@x.test", CreatedAt: now}, false)
	seedSignupUser(t, users, &User{ID: "proven", Email: "p@x.test", Origin: UserOriginSignup, TeamID: "t1", CreatedAt: now}, true)

	for _, email := range []string{"one@x.test", "two@x.test"} {
		if rec, _ := postSignup(t, h, `{"email":"`+email+`"}`); rec.Code != http.StatusCreated {
			t.Fatalf("signup %s = %d, want 201 under the cap: %s", email, rec.Code, rec.Body)
		}
	}

	before, _ := users.ListUsers(context.Background())
	rec, _ := postSignup(t, h, `{"email":"three@x.test"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("signup at the cap = %d, want 429: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 carries no Retry-After")
	}
	after, _ := users.ListUsers(context.Background())
	if len(after) != len(before) {
		t.Fatalf("a refused signup changed the user count from %d to %d", len(before), len(after))
	}

	// An hour later the two passkey-less attempts are abandoned, and the cap
	// opens without anyone intervening.
	atTime(t, now.Add(signupNoPasskeyTTL+time.Minute))
	if rec, _ := postSignup(t, h, `{"email":"three@x.test"}`); rec.Code != http.StatusCreated {
		t.Fatalf("signup after the cleanup window = %d, want 201: %s", rec.Code, rec.Body)
	}
}

// TestSignup_ResponseCarriesOnlyTheTwoFields: the body is shown to an
// unauthenticated caller, so it must not grow by accident.
func TestSignup_ResponseCarriesOnlyTheTwoFields(t *testing.T) {
	h, _, _, _ := signupFixture(t)
	rec, _ := postSignup(t, h, `{"email":"new@example.test"}`)
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 2 || raw["user_id"] == nil || raw["enrollment_token"] == nil {
		t.Fatalf("body = %s, want exactly user_id and enrollment_token", rec.Body)
	}
}

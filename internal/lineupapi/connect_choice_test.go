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
)

// The ambiguous-team choice (rosterbot-eq6n): GET /v1/connect offers the owned
// set, and POST /v1/connect {"team_id"} answers it. This is the one place a
// caller names their own team, so most of what follows is about refusals.

func ambiguousConn() *FantraxConnection {
	return &FantraxConnection{
		UserID:          "alice",
		Status:          ConnNeedsReconnect,
		LastError:       ConnErrAmbiguousTeam,
		OwnedTeams:      []OwnedTeam{{ID: "7", Name: "Dingers"}, {ID: "12", Name: "Sliders"}},
		CredsCiphertext: []byte("sealed:creds-fantrax-accepted"),
	}
}

func asAlice(t *testing.T, method, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, "/v1/connect", strings.NewReader(body))
	r.Header = reqWithSession(t, method, "/v1/connect", "alice", 0).Header
	return r
}

func connectStatusBody(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asAlice(t, http.MethodGet, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/connect = %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return body
}

func TestConnectStatus_OwnedTeamsAppearOnlyForAnAmbiguousTeam(t *testing.T) {
	t.Run("offered while a choice is pending", func(t *testing.T) {
		h, conns, _, _ := connectFixture(t, "")
		conns.conn = ambiguousConn()

		body := connectStatusBody(t, h)
		teams, _ := body["owned_teams"].([]any)
		if len(teams) != 2 {
			t.Fatalf("owned_teams = %v, want both teams", body["owned_teams"])
		}
		first, _ := teams[0].(map[string]any)
		if first["id"] != "7" || first["name"] != "Dingers" {
			t.Errorf("owned_teams[0] = %v, want id 7 name Dingers", first)
		}
		if body["last_error"] != ConnErrAmbiguousTeam || body["connected"] != false {
			t.Errorf("status body = %v, want ambiguous_team and not connected", body)
		}
	})

	// A stale set left in the store must not be offered beside any other
	// state, whatever the connect task did or did not clear.
	for name, mutate := range map[string]func(*FantraxConnection){
		"verified":        func(c *FantraxConnection) { c.Status, c.LastError, c.TeamID = ConnVerified, "", "7" },
		"pending":         func(c *FantraxConnection) { c.Status, c.LastError = ConnPending, "" },
		"bad credentials": func(c *FantraxConnection) { c.LastError = ConnErrBadCredentials },
		"interrupted":     func(c *FantraxConnection) { c.Status = ConnInterrupted },
	} {
		t.Run("absent when "+name, func(t *testing.T) {
			h, conns, _, _ := connectFixture(t, "")
			conns.conn = ambiguousConn()
			mutate(conns.conn)

			if _, present := connectStatusBody(t, h)["owned_teams"]; present {
				t.Fatal("owned_teams was served for a record that is not awaiting a team choice")
			}
		})
	}
}

func TestConnect_TeamChoiceRerunsTheCheckOnTheSavedCredentials(t *testing.T) {
	h, conns, jobs, sealer := connectFixture(t, "")
	conns.conn = ambiguousConn()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asAlice(t, http.MethodPost, `{"team_id":"12"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("choice = %d (%s), want 202", rec.Code, rec.Body)
	}

	got := conns.conn
	if got.Status != ConnPending || got.TeamID != "12" {
		t.Fatalf("record = status %q team %q, want pending with the chosen team named", got.Status, got.TeamID)
	}
	if string(got.CredsCiphertext) != "sealed:creds-fantrax-accepted" {
		t.Errorf("credentials = %q, want the SAME sealed bytes carried over", got.CredsCiphertext)
	}
	if sealer.seen != nil {
		t.Error("a team choice sealed something; it carries no credentials to seal")
	}
	if len(got.OwnedTeams) != 0 || got.LastError != "" {
		t.Errorf("record still carries owned %v / error %q; a new check is running", got.OwnedTeams, got.LastError)
	}
	// Nothing is CLAIMED here: the handler has no way to verify, so the user
	// record's team is untouched and the connect task is what proves it.
	if !jobs.launched || len(jobs.argv) < 3 || jobs.argv[0] != "connect" || jobs.argv[2] != "alice" {
		t.Fatalf("launched=%v argv=%v, want the connect task for alice", jobs.launched, jobs.argv)
	}
	if jobs.statusAt != ConnPending {
		t.Errorf("record was %q when the task launched, want pending written first", jobs.statusAt)
	}
}

func TestConnect_TeamChoiceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userTeam string
		conn     func() *FantraxConnection
		body     string
		want     int
	}{
		{
			name: "a team Fantrax did not report",
			conn: ambiguousConn, body: `{"team_id":"99"}`, want: http.StatusBadRequest,
		},
		{
			name: "no connection record at all",
			conn: func() *FantraxConnection { return nil }, body: `{"team_id":"7"}`, want: http.StatusConflict,
		},
		{
			name: "a verified record",
			conn: func() *FantraxConnection {
				c := ambiguousConn()
				c.Status, c.LastError, c.TeamID = ConnVerified, "", "12"
				return c
			},
			body: `{"team_id":"7"}`, want: http.StatusConflict,
		},
		{
			name: "a different failure, with a stale owned set still stored",
			conn: func() *FantraxConnection {
				c := ambiguousConn()
				c.LastError = ConnErrBadCredentials
				return c
			},
			body: `{"team_id":"7"}`, want: http.StatusConflict,
		},
		{
			name: "the saved credentials are gone",
			conn: func() *FantraxConnection {
				c := ambiguousConn()
				c.CredsCiphertext = nil
				return c
			},
			body: `{"team_id":"7"}`, want: http.StatusConflict,
		},
		{
			name:     "the user record already names a team",
			userTeam: "3",
			conn:     ambiguousConn, body: `{"team_id":"7"}`, want: http.StatusConflict,
		},
		{
			name: "a team beside credentials is not a choice",
			conn: ambiguousConn, body: `{"username":"a","password":"b","team_id":"7"}`, want: http.StatusBadRequest,
		},
		{
			name: "a team beside half the credentials",
			conn: ambiguousConn, body: `{"username":"a","team_id":"7"}`, want: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, conns, jobs, _ := connectFixture(t, tc.userTeam)
			conns.conn = tc.conn()
			var before FantraxConnection
			if conns.conn != nil {
				before = *conns.conn
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, asAlice(t, http.MethodPost, tc.body))

			if rec.Code != tc.want {
				t.Fatalf("status %d (%s), want %d", rec.Code, rec.Body, tc.want)
			}
			if jobs.launched {
				t.Error("a refused choice launched the connect task — a chromedp login against a real account")
			}
			if conns.puts != 0 {
				t.Errorf("a refused choice wrote the record %d time(s)", conns.puts)
			}
			if conns.conn != nil && (conns.conn.Status != before.Status || conns.conn.TeamID != before.TeamID) {
				t.Errorf("record moved from %q/%q to %q/%q", before.Status, before.TeamID,
					conns.conn.Status, conns.conn.TeamID)
			}
		})
	}
}

// A choice is one connect run like any other, so the in-flight guard applies:
// a double tap must not stack two logins.
func TestConnect_TeamChoiceRespectsTheInFlightGuard(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "")
	conns.conn = ambiguousConn()

	first := httptest.NewRecorder()
	h.ServeHTTP(first, asAlice(t, http.MethodPost, `{"team_id":"7"}`))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first choice = %d: %s", first.Code, first.Body)
	}
	jobs.launched = false

	second := httptest.NewRecorder()
	h.ServeHTTP(second, asAlice(t, http.MethodPost, `{"team_id":"12"}`))
	if second.Code != http.StatusConflict {
		t.Fatalf("second choice while pending = %d, want 409: %s", second.Code, second.Body)
	}
	if jobs.launched || conns.conn.TeamID != "7" {
		t.Errorf("the second choice launched=%v and left team %q; the first must stand", jobs.launched, conns.conn.TeamID)
	}
}

// If the record moves between the read and the write, the choice is validated
// again against the record that won — not written on the strength of the one
// that lost.
func TestConnect_TeamChoiceIsRevalidatedAfterALostRace(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "")
	conns.conn = ambiguousConn()
	moved := false
	conns.afterGet = func() {
		if moved {
			return
		}
		moved = true
		// A connect run lands a different verdict underneath the request.
		conns.conn.LastError = ConnErrBadCredentials
		conns.conn.OwnedTeams = nil
		conns.moveUnderneath()
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asAlice(t, http.MethodPost, `{"team_id":"7"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d (%s), want 409 once the ambiguous result was replaced", rec.Code, rec.Body)
	}
	if jobs.launched {
		t.Error("launched a connect run for a choice the current record no longer offers")
	}
	if conns.conn.TeamID != "" || conns.conn.Status == ConnPending {
		t.Errorf("record = %q/%q; the stale choice must not have been written", conns.conn.Status, conns.conn.TeamID)
	}
}

// unreadableUsers resolves the session and then fails the handler's own read
// of the user record — the DynamoDB blip between authorize and handleConnect.
type unreadableUsers struct {
	*FileUserStore
	reads int
}

func (u *unreadableUsers) GetUser(ctx context.Context, id UserID) (*User, bool, error) {
	u.reads++
	if u.reads > 1 {
		return nil, false, errors.New("dynamodb: throughput exceeded")
	}
	return u.FileUserStore.GetUser(ctx, id)
}

// TestConnect_AnUnreadableUserRecordFailsClosed: before discovery an
// unreadable record left the team empty and the connect task refused with
// no_team. Now an empty team is DISCOVERED, so tolerating the failed read
// would turn "verify the invited team" into "adopt whatever the credentials
// control" on a store blip.
func TestConnect_AnUnreadableUserRecordFailsClosed(t *testing.T) {
	files := NewFileUserStore(t.TempDir())
	if err := files.CreateUser(context.Background(), &User{ID: "alice", Email: "alice@example.test",
		Role: RoleMember, Status: UserActive, TeamID: "team-7"}); err != nil {
		t.Fatal(err)
	}
	conns := &memConnections{}
	jobs := &recordingJobs{conns: conns}
	h := Handler(Config{Token: testToken, SessionSecret: testSecret,
		Users: &unreadableUsers{FileUserStore: files}, Connections: conns,
		Sealer: &recordingSealer{}, Jobs: jobs})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asAlice(t, http.MethodPost, `{"username":"a@fantrax","password":"pw"}`))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d (%s), want 502", rec.Code, rec.Body)
	}
	if jobs.launched || conns.puts != 0 {
		t.Errorf("launched=%v puts=%d; nothing may be written or started without the user's team",
			jobs.launched, conns.puts)
	}
}

// --- the connect budget -------------------------------------------------------

func submitCreds(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, asAlice(t, http.MethodPost, `{"username":"a@fantrax","password":"guess"}`))
	return rec
}

// settle stands in for the connect task finishing with a tenant fault: it puts
// the record it read back, whole, with a new status — which is what carries
// the budget fields across a run.
func settle(conns *memConnections) {
	conns.conn.Status, conns.conn.LastError = ConnNeedsReconnect, ConnErrBadCredentials
}

// TestConnect_AnAccountWithNoProvenTeamHasABudget: with public signup, anyone
// can reach this route, and each submission is a Fantrax login from our
// address whose verdict we then report.
func TestConnect_AnAccountWithNoProvenTeamHasABudget(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "")

	for i := 1; i <= connectBudgetAttempts; i++ {
		if rec := submitCreds(t, h); rec.Code != http.StatusAccepted {
			t.Fatalf("attempt %d = %d, want 202: %s", i, rec.Code, rec.Body)
		}
		if conns.conn.ConnectAttempts != i {
			t.Fatalf("after attempt %d the record counts %d", i, conns.conn.ConnectAttempts)
		}
		settle(conns)
	}

	jobs.launched = false
	puts := conns.puts
	rec := submitCreds(t, h)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt over budget = %d, want 429: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 carries no Retry-After")
	}
	if jobs.launched || conns.puts != puts {
		t.Error("an over-budget attempt still wrote the record or launched a Fantrax login")
	}

	// A team choice spends from the same budget: it launches the same login.
	conns.conn = ambiguousConn()
	conns.conn.ConnectAttempts, conns.conn.ConnectWindowStart = connectBudgetAttempts, time.Now().Unix()
	choice := httptest.NewRecorder()
	h.ServeHTTP(choice, asAlice(t, http.MethodPost, `{"team_id":"7"}`))
	if choice.Code != http.StatusTooManyRequests {
		t.Fatalf("team choice over budget = %d, want 429: %s", choice.Code, choice.Body)
	}

	// The window expires.
	conns.conn.Status, conns.conn.LastError = ConnNeedsReconnect, ConnErrBadCredentials
	conns.conn.ConnectWindowStart = time.Now().Add(-connectBudgetWindow - time.Minute).Unix()
	if rec := submitCreds(t, h); rec.Code != http.StatusAccepted {
		t.Fatalf("attempt after the window = %d, want 202: %s", rec.Code, rec.Body)
	}
	if conns.conn.ConnectAttempts != 1 {
		t.Errorf("a new window opened at %d attempts, want 1", conns.conn.ConnectAttempts)
	}
}

// TestConnect_AProvenTenantIsNeverBudgeted: a working tenant re-entering a
// changed password must not be counted, however often.
func TestConnect_AProvenTenantIsNeverBudgeted(t *testing.T) {
	h, conns, _, _ := connectFixture(t, "team-7")
	for i := 1; i <= connectBudgetAttempts+3; i++ {
		if rec := submitCreds(t, h); rec.Code != http.StatusAccepted {
			t.Fatalf("attempt %d for a tenant with a team = %d, want 202: %s", i, rec.Code, rec.Body)
		}
		settle(conns)
	}
	if conns.conn.ConnectAttempts != 0 {
		t.Errorf("a proven tenant's record counts %d attempts, want none", conns.conn.ConnectAttempts)
	}
}

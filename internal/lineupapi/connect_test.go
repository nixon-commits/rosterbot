package lineupapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- doubles -----------------------------------------------------------------
//
// The three collaborators POST /v1/connect needs are faked rather than mocked:
// each records what it was actually asked to do, so the assertions below are
// about the handler's behaviour and never about a call count on a mock.

// recordingSealer stands in for KMS. It returns a marked ciphertext so a test
// can prove the bytes that reached the store are the sealed ones and not the
// plaintext password.
type recordingSealer struct {
	uid  UserID
	seen []byte
	err  error
}

func (s *recordingSealer) Seal(_ context.Context, uid UserID, plaintext []byte) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.uid, s.seen = uid, append([]byte(nil), plaintext...)
	return append([]byte("sealed:"), plaintext...), nil
}

type memConnections struct {
	conn *FantraxConnection
	err  error
}

func (m *memConnections) GetConnection(context.Context, UserID) (*FantraxConnection, bool, error) {
	if m.conn == nil {
		return nil, false, nil
	}
	return m.conn, true, nil
}

func (m *memConnections) PutConnection(_ context.Context, c *FantraxConnection) error {
	if m.err != nil {
		return m.err
	}
	cp := *c
	m.conn = &cp
	return nil
}

// recordingJobs captures the argv the handler launches, and — crucially — what
// the connection store held at the moment of launch. That snapshot is what lets
// TestConnect_WritesTheRecordBeforeLaunchingTheTask assert an ordering rather
// than just a pair of end states.
type recordingJobs struct {
	callers    []UserID
	conns      *memConnections
	argv       []string
	statusAt   ConnStatus
	launched   bool
	err        error
	returnedID string
}

func (j *recordingJobs) Run(_ context.Context, caller UserID, command []string) (string, error) {
	j.callers = append(j.callers, caller)
	j.launched = true
	j.argv = append([]string(nil), command...)
	if j.conns != nil && j.conns.conn != nil {
		j.statusAt = j.conns.conn.Status
	}
	if j.err != nil {
		return "", j.err
	}
	if j.returnedID == "" {
		j.returnedID = "run-abc123"
	}
	return j.returnedID, nil
}

// connectFixture wires a Handler with a real FileUserStore (so the session
// actually resolves the way production does) plus the three doubles.
func connectFixture(t *testing.T, teamID string) (http.Handler, *memConnections, *recordingJobs, *recordingSealer) {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	u := &User{ID: "alice", Email: "alice@example.test", Role: RoleMember, Status: UserActive, TeamID: teamID}
	if err := users.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	conns := &memConnections{}
	jobs := &recordingJobs{conns: conns}
	sealer := &recordingSealer{}
	h := Handler(Config{
		Token:         testToken,
		SessionSecret: testSecret,
		Users:         users,
		Connections:   conns,
		Sealer:        sealer,
		Jobs:          jobs,
	})
	return h, conns, jobs, sealer
}

func connectBody(t *testing.T, user, pass string) *strings.Reader {
	t.Helper()
	b, err := json.Marshal(map[string]string{"username": user, "password": pass})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return strings.NewReader(string(b))
}

// --- tests -------------------------------------------------------------------

// TestConnect_TokenCallerIsRefused pins the property the handler's own comment
// calls structural rather than a policy choice.
//
// The bearer token authenticates "the operator", not a person: a token caller
// carries no UserID, so there is no account whose Fantrax login is being
// attached. Were this to pass, a break-glass credential could bind a Fantrax
// account to whichever tenant the request happened to name — the one thing the
// connect flow exists to make impossible.
//
// The failure this catches: any change that resolves a token caller to a
// default or operator UserID instead of leaving it empty.
func TestConnect_TokenCallerIsRefused(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")

	r := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, "u", "p"))
	r.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("token caller POST /v1/connect = %d, want 403", rec.Code)
	}
	if conns.conn != nil {
		t.Error("a refused connect wrote a connection record; the refusal must happen before any state is touched")
	}
	if jobs.launched {
		t.Error("a refused connect launched the verification task")
	}
}

// TestConnect_UnconfiguredReturns501 covers the local `serve` shape, where
// there is no ECS and no CMK. It must be a clean 501 rather than a panic on a
// nil Sealer.
func TestConnect_UnconfiguredReturns501(t *testing.T) {
	users := NewFileUserStore(t.TempDir())
	if err := users.CreateUser(context.Background(),
		&User{ID: "alice", Email: "a@example.test", Role: RoleMember, Status: UserActive}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	h := Handler(Config{Token: testToken, SessionSecret: testSecret, Users: users})

	r := reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0)
	r.Body = http.NoBody
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("connect with no Sealer/Connections/Jobs = %d, want 501", rec.Code)
	}
}

// TestConnect_BodyOverTheSizeCapIsRefused exercises the 8 KiB MaxBytesReader.
//
// The cap matters because the body carries a password: an unbounded read on a
// public Function URL is a memory-exhaustion lever that needs no credentials to
// pull, since the size check necessarily precedes any authentication of the
// content.
//
// It also pins the deliberate silence of the error. The handler must not echo
// the decode error, because a truncated body can leave a password fragment in
// the message — and from there into logs.
func TestConnect_BodyOverTheSizeCapIsRefused(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")

	huge := strings.Repeat("x", 9<<10) // > the 8 KiB cap
	r := reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0)
	r.Body = http.NoBody
	r2 := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, "alice", huge))
	r2.Header = r.Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r2)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized connect body = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "xxxx") {
		t.Error("the error response echoed body content; a truncated password must never reach the response or the logs")
	}
	if conns.conn != nil || jobs.launched {
		t.Error("an oversized body still wrote a record or launched the task")
	}
}

// TestConnect_MissingCredentialsAreRefused: a well-formed body that omits
// either field is a 400, not a sealed empty credential.
func TestConnect_MissingCredentialsAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, user, pass string }{
		{"no password", "alice", ""},
		{"no username", "", "secret"},
		{"neither", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, conns, jobs, sealer := connectFixture(t, "team-7")
			r := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, tc.user, tc.pass))
			r.Header = reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0).Header
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("connect with %s = %d, want 400", tc.name, rec.Code)
			}
			if sealer.seen != nil {
				t.Error("an incomplete credential pair was sealed")
			}
			if conns.conn != nil || jobs.launched {
				t.Error("an incomplete credential pair wrote a record or launched the task")
			}
		})
	}
}

// TestConnect_HappyPathWritesPendingAndReturnsRunID is the shape the browser
// depends on: 202 with a run id it can follow via GET /v1/runs/{id}/progress,
// and a record left at ConnPending for the task to pick up.
//
// It also asserts the two properties that make the record safe to store: the
// ciphertext is what the Sealer produced (never the plaintext), and the seal
// named this caller's UserID — the encryption context that stops one tenant's
// ciphertext from opening as another's.
func TestConnect_HappyPathWritesPendingAndReturnsRunID(t *testing.T) {
	h, conns, jobs, sealer := connectFixture(t, "team-7")

	r := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, "alice@fantrax", "hunter2"))
	r.Header = reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0).Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("connect = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["run_id"] != "run-abc123" {
		t.Errorf("run_id = %q, want the id the runner returned", got["run_id"])
	}
	if got["status"] != string(ConnPending) {
		t.Errorf("status = %q, want %q", got["status"], ConnPending)
	}

	if conns.conn == nil {
		t.Fatal("no connection record was written")
	}
	if conns.conn.Status != ConnPending {
		t.Errorf("stored status = %q, want %q", conns.conn.Status, ConnPending)
	}
	if conns.conn.UserID != "alice" {
		t.Errorf("stored UserID = %q, want alice", conns.conn.UserID)
	}
	if sealer.uid != "alice" {
		t.Errorf("Seal named uid %q, want alice: the encryption context is what stops "+
			"one tenant's ciphertext opening as another's", sealer.uid)
	}
	if !strings.HasPrefix(string(conns.conn.CredsCiphertext), "sealed:") {
		t.Fatal("the stored credential is not what the Sealer returned")
	}
	if strings.Contains(string(conns.conn.CredsCiphertext), "hunter2") &&
		!strings.HasPrefix(string(conns.conn.CredsCiphertext), "sealed:") {
		t.Error("the plaintext password reached the store")
	}
	if want := []string{"connect", "--user", "alice"}; !equalArgv(jobs.argv, want) {
		t.Errorf("launched %v, want %v", jobs.argv, want)
	}
}

// TestConnect_WritesTheRecordBeforeLaunchingTheTask pins the ordering the
// handler documents as deliberate.
//
// Record-then-launch fails recoverably: a pending record with no run is fixed
// by retrying. Launch-then-record fails unrecoverably — the task starts, reads
// the store, finds no credentials, and reports ErrNoConnection for a user who
// just submitted perfectly good ones.
//
// Asserting the two end states would not catch a reordering, since both exist
// either way. The runner therefore snapshots the store at launch time, which is
// the only moment the difference is observable.
func TestConnect_WritesTheRecordBeforeLaunchingTheTask(t *testing.T) {
	h, _, jobs, _ := connectFixture(t, "team-7")

	r := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, "u", "p"))
	r.Header = reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0).Header
	h.ServeHTTP(httptest.NewRecorder(), r)

	if !jobs.launched {
		t.Fatal("the verification task never launched")
	}
	if jobs.statusAt != ConnPending {
		t.Fatalf("at launch the store held %q, want %q: the task reads the record it "+
			"was launched for, so the record must exist first", jobs.statusAt, ConnPending)
	}
}

// TestConnect_StoreFailureDoesNotLaunchTheTask is the other half of that
// ordering: if the record cannot be stored there is nothing for the task to
// read, so launching one burns an ECS task to reach a guaranteed failure.
func TestConnect_StoreFailureDoesNotLaunchTheTask(t *testing.T) {
	h, conns, jobs, _ := connectFixture(t, "team-7")
	conns.err = errors.New("dynamo unavailable")

	r := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, "u", "p"))
	r.Header = reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0).Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("connect with a failing store = %d, want 502", rec.Code)
	}
	if jobs.launched {
		t.Error("the verification task launched even though the credentials were never stored")
	}
}

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestConnect_LaunchesTheTaskAsTheCaller.
//
// --user tells the connect command whose credentials to decrypt; the caller
// argument sets the task's ROSTERBOT_USER_ID, which decides the S3 prefixes it
// writes under. They are not redundant. Without the second, the FX_RM cookie
// cache the task mints lands in the OPERATOR's session/ prefix — the
// cross-tenant credential leak cmd/sync_tenant_test.go exists to prevent,
// arriving through the one flow whose whole job is handling somebody else's
// credentials.
func TestConnect_LaunchesTheTaskAsTheCaller(t *testing.T) {
	h, _, jobs, _ := connectFixture(t, "team-7")

	// Mirror TestConnect_BodyOverTheSizeCapIsRefused: build the request with the
	// real body, then borrow the session headers.
	sess := reqWithSession(t, http.MethodPost, "/v1/connect", "alice", 0)
	r := httptest.NewRequest(http.MethodPost, "/v1/connect", connectBody(t, "u", "p"))
	r.Header = sess.Header
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(jobs.callers) != 1 {
		t.Fatalf("launched %d tasks, want 1", len(jobs.callers))
	}
	if jobs.callers[0] != "alice" {
		t.Errorf("connect task launched as %q, want alice — its session cookie would be "+
			"written under the wrong tenant's prefix", jobs.callers[0])
	}
}

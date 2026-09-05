package lineupapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The bead in one sentence: a `connect --user …` run that fails for a reason
// only the tenant can fix exits 0 on purpose (so opsalert does not page the
// operator), and the Runs tab renders that exit status alone — SUCCESS, above
// an activity entry saying the connection failed.
//
// These tests pin the join that fixes it and, just as importantly, the four
// ways it must refuse to answer.

func connectRunFixture(t *testing.T, runs []Run, detail *RunDetail, conns *memConnections) http.Handler {
	t.Helper()
	users := NewFileUserStore(t.TempDir())
	seedUser(t, users, "alice", RoleMember, UserActive)
	return Handler(Config{
		Token:         testToken,
		SessionSecret: testSecret,
		Users:         users,
		Connections:   conns,
		Runs:          fakeRuns{runs: runs, detail: detail},
	})
}

func decodeRuns(t *testing.T, h http.Handler, r *http.Request) (int, RunsResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out RunsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode runs: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

func runByID(t *testing.T, resp RunsResponse, id string) Run {
	t.Helper()
	for _, r := range resp.Runs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("run %s missing from the response (%d rows)", id, len(resp.Runs))
	return Run{}
}

// TestApplyConnectOutcome_ExactMatchOnly is the whole read rule: no match means
// no chip, never a wrong one.
func TestApplyConnectOutcome_ExactMatchOnly(t *testing.T) {
	stamp := &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrNoTeam}

	cases := []struct {
		name string
		run  Run
		lr   *ConnectRun
		want bool
	}{
		{"the connect row the stamp names", Run{ID: "task-1", Command: "connect --user alice"}, stamp, true},
		{"a bare connect command still matches", Run{ID: "task-1", Command: "connect"}, stamp, true},
		{"a different connect run", Run{ID: "task-9", Command: "connect --user alice"}, stamp, false},
		// The id guard alone is not enough: entrypoint.sh falls back to
		// local-<timestamp>, so two tasks in one second can share an id.
		{"a non-connect row with the same id", Run{ID: "task-1", Command: "optimize --matchup"}, stamp, false},
		{"a command that merely starts with the word", Run{ID: "task-1", Command: "connect-foo"}, stamp, false},
		{"no stamp at all", Run{ID: "task-1", Command: "connect --user alice"}, nil, false},
		// An unstamped record beside a ledger row that somehow carries no id:
		// two empty strings compare equal, so the id test alone would match
		// them. "" is not a run id, and absence must not join to absence.
		{"an empty stamp against a row with no id", Run{Command: "connect --user alice"},
			&ConnectRun{Verdict: ConnectVerdictFailed}, false},
		{"a stamp with no verdict", Run{ID: "task-1", Command: "connect --user alice"},
			&ConnectRun{RunID: "task-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.run
			applyConnectOutcome(&r, tc.lr)
			if got := r.Connect != nil; got != tc.want {
				t.Fatalf("Connect present = %v, want %v", got, tc.want)
			}
			if tc.want {
				if r.Connect.Verdict != tc.lr.Verdict || r.Connect.LastError != tc.lr.LastError {
					t.Fatalf("Connect = %+v, want verdict %q error %q",
						*r.Connect, tc.lr.Verdict, tc.lr.LastError)
				}
			}
		})
	}
}

// TestRunsList_ConnectFailureIsChippedOnItsOwnRow is the bead's own screen: the
// ledger row still says SUCCESS (which is the constraint — changing it would
// page the operator) and the connect verdict rides beside it.
func TestRunsList_ConnectFailureIsChippedOnItsOwnRow(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID:    "alice",
		Status:    ConnNeedsReconnect,
		LastError: ConnErrNoTeam,
		LastConnectRun: &ConnectRun{
			RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrNoTeam,
		},
	}}
	h := connectRunFixture(t, []Run{
		{ID: "task-1", Command: "connect --user alice", Status: "SUCCESS", Trigger: "manual"},
		{ID: "task-2", Command: "optimize --matchup", Status: "SUCCESS", Trigger: "schedule"},
	}, nil, conns)

	code, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0))
	if code != http.StatusOK {
		t.Fatalf("GET /v1/runs = %d, want 200", code)
	}
	connect := runByID(t, resp, "task-1")
	if connect.Status != "SUCCESS" {
		t.Fatalf("ledger status = %q, want SUCCESS: the exit-0 routing must not be disturbed", connect.Status)
	}
	if connect.Connect == nil {
		t.Fatal("the connect row carries no verdict; the Runs tab still reads SUCCESS with nothing beside it")
	}
	if connect.Connect.Verdict != ConnectVerdictFailed {
		t.Fatalf("verdict = %q, want %q", connect.Connect.Verdict, ConnectVerdictFailed)
	}
	if connect.Connect.LastError != ConnErrNoTeam {
		t.Fatalf("last_error = %q, want %q", connect.Connect.LastError, ConnErrNoTeam)
	}
	if other := runByID(t, resp, "task-2"); other.Connect != nil {
		t.Fatalf("the optimize row carries a connection verdict: %+v", *other.Connect)
	}
}

// TestRunsList_OperatorActionableFailureIsNeverGreen is the mirror of the bead.
//
// On the operator-actionable route (a Cloudflare block) cmd/connect.go
// deliberately leaves the tenant's Status alone — so a re-verify of an
// already-verified tenant keeps Status "verified" while the run exits non-zero
// and the ledger reads FAILED. Anything that derived the verdict from Status
// would render a green "connected" chip on that FAILED row.
func TestRunsList_OperatorActionableFailureIsNeverGreen(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID: "alice",
		// Untouched by the operator-actionable route: still verified.
		Status:    ConnVerified,
		LastError: ConnErrBotChallenge,
		LastConnectRun: &ConnectRun{
			RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrBotChallenge,
		},
	}}
	h := connectRunFixture(t, []Run{
		{ID: "task-1", Command: "connect --user alice", Status: "FAILED", Trigger: "manual"},
	}, nil, conns)

	_, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0))
	row := runByID(t, resp, "task-1")
	if row.Connect == nil {
		t.Fatal("no verdict on a FAILED connect row")
	}
	if row.Connect.Verdict != ConnectVerdictFailed {
		t.Fatalf("verdict = %q on a FAILED connect row, want %q — a run that paged the operator "+
			"must never render as connected", row.Connect.Verdict, ConnectVerdictFailed)
	}
	if row.Connect.LastError != ConnErrBotChallenge {
		t.Fatalf("last_error = %q, want %q", row.Connect.LastError, ConnErrBotChallenge)
	}
}

// TestRunsList_StaleStampIsNotRenderedOnANewerRun is what makes "exact match
// only" worth having: the connection record holds ONE stamp, so a newer connect
// run that has not written yet must read as "we cannot say", not as the last
// run's outcome.
func TestRunsList_StaleStampIsNotRenderedOnANewerRun(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID:         "alice",
		LastConnectRun: &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrNoTeam},
	}}
	h := connectRunFixture(t, []Run{
		{ID: "task-9", Command: "connect --user alice", Status: "RUNNING", Trigger: "manual"},
		{ID: "task-1", Command: "connect --user alice", Status: "SUCCESS", Trigger: "manual"},
	}, nil, conns)

	_, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0))
	if newer := runByID(t, resp, "task-9"); newer.Connect != nil {
		t.Fatalf("the newer connect run carries the older run's verdict: %+v", *newer.Connect)
	}
	if older := runByID(t, resp, "task-1"); older.Connect == nil {
		t.Fatal("the run the stamp actually names lost its verdict")
	}
}

// TestRunsList_AVerdictShowsOnARunningRow is the deliberate decision on the
// window between the two writes.
//
// connect writes the connection record (and therefore the stamp) and only then
// exits; entrypoint.sh writes the terminal ledger row afterwards. A 5s poll
// landing in between sees RUNNING beside a verdict. That is rendered on
// purpose: connect writes its record exactly once per run, so the stamp is the
// run's concluded verdict and is FRESHER than the ledger row, and suppressing
// it would replace a true statement with a spinner.
func TestRunsList_AVerdictShowsOnARunningRow(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID:         "alice",
		LastConnectRun: &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrBadCredentials},
	}}
	h := connectRunFixture(t, []Run{
		{ID: "task-1", Command: "connect --user alice", Status: "RUNNING", Trigger: "manual"},
	}, nil, conns)

	_, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0))
	row := runByID(t, resp, "task-1")
	if row.Connect == nil || row.Connect.Verdict != ConnectVerdictFailed {
		t.Fatalf("a RUNNING connect row lost the verdict its own task already wrote: %+v", row)
	}
}

// TestRunDetail_CarriesTheSameChip makes the "RunDetail embeds Run, so both
// surfaces cost the same" claim true rather than assumed. A detail-only or
// list-only fix leaves the two screens contradicting each other.
func TestRunDetail_CarriesTheSameChip(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID:         "alice",
		LastConnectRun: &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrNoTeam},
	}}
	detail := &RunDetail{Run: Run{ID: "task-1", Command: "connect --user alice", Status: "SUCCESS"}}
	h := connectRunFixture(t, nil, detail, conns)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithSession(t, http.MethodGet, "/v1/runs/task-1", "alice", 0))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/runs/task-1 = %d, want 200", rec.Code)
	}
	var got RunDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if got.Connect == nil || got.Connect.Verdict != ConnectVerdictFailed ||
		got.Connect.LastError != ConnErrNoTeam {
		t.Fatalf("run detail carries no connect verdict: %+v", got)
	}
	// The store's own record must not have been annotated in place.
	if detail.Connect != nil {
		t.Fatal("handleRunDetail mutated the store's RunDetail; a later reader would see a stale chip")
	}
}

// TestRunsList_AnnotationDoesNotMutateTheStoresSlice pins the copy.
//
// fakeRuns.List returns its own field, which is the natural shape for a test
// fixture. Annotating in place would leak one request's chip into the next and
// let a second, differently-configured request pass on state from the first.
func TestRunsList_AnnotationDoesNotMutateTheStoresSlice(t *testing.T) {
	stored := []Run{{ID: "task-1", Command: "connect --user alice", Status: "SUCCESS"}}
	conns := &memConnections{conn: &FantraxConnection{
		UserID:         "alice",
		LastConnectRun: &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrNoTeam},
	}}
	h := connectRunFixture(t, stored, nil, conns)

	if _, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0)); resp.Runs[0].Connect == nil {
		t.Fatal("first request produced no verdict")
	}
	if stored[0].Connect != nil {
		t.Fatalf("the store's own slice was annotated in place: %+v", *stored[0].Connect)
	}
}

// TestRunsList_BearerTokenGetsNoChip: the break-glass token carries no UserID
// by construction, so there is no person whose connection this would be.
func TestRunsList_BearerTokenGetsNoChip(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID:         "alice",
		LastConnectRun: &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed, LastError: ConnErrNoTeam},
	}}
	h := connectRunFixture(t, []Run{
		{ID: "task-1", Command: "connect --user alice", Status: "SUCCESS"},
	}, nil, conns)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/runs", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	code, resp := decodeRuns(t, h, r)
	if code != http.StatusOK {
		t.Fatalf("token GET /v1/runs = %d, want 200", code)
	}
	for _, run := range resp.Runs {
		if run.Connect != nil {
			t.Fatalf("the bearer token got a connection verdict for run %s: %+v", run.ID, *run.Connect)
		}
	}
}

// TestRunsList_ConnectionStoreFailureStillServesTheRuns: the rows are the
// answer, the verdict is a decoration, and a decoration must not take the page
// down. Degrade to a missing chip, never to a 502.
func TestRunsList_ConnectionStoreFailureStillServesTheRuns(t *testing.T) {
	conns := &memConnections{getErr: http.ErrServerClosed}
	h := connectRunFixture(t, []Run{
		{ID: "task-1", Command: "connect --user alice", Status: "SUCCESS"},
		{ID: "task-2", Command: "optimize --matchup", Status: "SUCCESS"},
	}, nil, conns)

	code, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0))
	if code != http.StatusOK {
		t.Fatalf("GET /v1/runs with a broken connection store = %d, want 200", code)
	}
	if len(resp.Runs) != 2 {
		t.Fatalf("got %d rows, want 2 — the run history is the answer", len(resp.Runs))
	}
	for _, run := range resp.Runs {
		if run.Connect != nil {
			t.Fatalf("run %s got a verdict from a store that errored: %+v", run.ID, *run.Connect)
		}
	}
}

// TestRunsList_NoConnectRowSkipsTheStoreRead pins the one cost mitigation there
// is. live.js polls this route every 5s for the whole session, so on a window
// with no connect row the read must not happen at all.
func TestRunsList_NoConnectRowSkipsTheStoreRead(t *testing.T) {
	conns := &memConnections{conn: &FantraxConnection{
		UserID:         "alice",
		LastConnectRun: &ConnectRun{RunID: "task-1", Verdict: ConnectVerdictFailed},
	}}
	h := connectRunFixture(t, []Run{
		{ID: "task-2", Command: "optimize --matchup", Status: "SUCCESS"},
		{ID: "task-3", Command: "grade", Status: "SUCCESS"},
	}, nil, conns)

	if _, resp := decodeRuns(t, h, reqWithSession(t, http.MethodGet, "/v1/runs", "alice", 0)); len(resp.Runs) != 2 {
		t.Fatalf("got %d rows, want 2", len(resp.Runs))
	}
	if conns.gets != 0 {
		t.Fatalf("GetConnection called %d times for a runs window with no connect row", conns.gets)
	}
}

package lineupapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	callers  []UserID
	commands [][]string
}

func (r *recordingRunner) Run(_ context.Context, caller UserID, command []string) (string, error) {
	r.callers = append(r.callers, caller)
	r.commands = append(r.commands, command)
	return "task-" + string(caller), nil
}

func jobRequest(t *testing.T, name string, caller Caller) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+name, strings.NewReader("{}"))
	r.SetPathValue("name", name)
	return r.WithContext(withCaller(r.Context(), caller))
}

// TestHandleJob_LaunchesAsTheCaller is the critical finding from the
// half-mechanism audit.
//
// The scheduled fan-out passes the tenant into the task; the API's launcher did
// not. ROSTERBOT_USER_ID lives on the SHARED task definition, so a launch that
// omits it does not fail or produce an empty prefix — it silently resolves to a
// real, working, PRIVILEGED tenant. A member pressing "Optimize Lineup"
// therefore got a task that decrypted the OPERATOR's Fantrax credentials, read
// the OPERATOR's auto_apply, and applied a lineup to the OPERATOR's roster,
// with every layer reporting success.
//
// It was reachable by design, not by accident: authz.go says a member may
// launch jobs scoped to their own tenant, `optimize` is Mutating and not
// league-wide, and the SPA renders every job to everyone.
func TestHandleJob_LaunchesAsTheCaller(t *testing.T) {
	runner := &recordingRunner{}
	cfg := Config{Jobs: runner}

	for _, uid := range []UserID{"alice", "bob"} {
		rec := httptest.NewRecorder()
		cfg.handleJob(rec, jobRequest(t, "optimize", Caller{UserID: uid, Role: RoleMember}))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d body %s", uid, rec.Code, rec.Body)
		}
	}

	if len(runner.callers) != 2 {
		t.Fatalf("launched %d jobs, want 2", len(runner.callers))
	}
	for i, want := range []UserID{"alice", "bob"} {
		if runner.callers[i] != want {
			t.Errorf("job %d launched as %q, want %q — it would run against that tenant's "+
				"credentials and roster", i, runner.callers[i], want)
		}
	}
}

// TestHandleJob_CallerIsAParameterNotAConvention.
//
// The launcher and the dispatcher differed only by a map literal, so
// correctness was a convention one of them followed. Making the caller an
// argument means the compiler rejects a launcher that forgets it — the same
// move this repo already made for WeeklyPeriod/DailyPeriod and for
// Identity.Version, and for the same reason.
//
// This test exists to fail at COMPILE time if the parameter is ever dropped;
// the body merely pins that it is the value actually forwarded.
func TestHandleJob_CallerIsAParameterNotAConvention(t *testing.T) {
	var _ JobRunner = (*recordingRunner)(nil)

	runner := &recordingRunner{}
	cfg := Config{Jobs: runner}
	rec := httptest.NewRecorder()
	cfg.handleJob(rec, jobRequest(t, "optimize", Caller{UserID: "carol", Role: RoleMember}))

	if len(runner.callers) != 1 || runner.callers[0] != "carol" {
		t.Fatalf("callers = %v, want [carol]", runner.callers)
	}
}

// TestHandleJob_TokenCallerHasNoTenantOfItsOwn.
//
// The bearer token is break-glass admin access with an empty UserID by
// construction — it deliberately never touches the user store. The launcher
// must forward that emptiness rather than inventing a tenant; resolving it to
// the deployment default is the ECS side's decision, made once, where the
// default actually lives.
func TestHandleJob_TokenCallerHasNoTenantOfItsOwn(t *testing.T) {
	runner := &recordingRunner{}
	cfg := Config{Jobs: runner}
	rec := httptest.NewRecorder()
	cfg.handleJob(rec, jobRequest(t, "optimize", Caller{Role: RoleAdmin, ViaToken: true}))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if len(runner.callers) != 1 || runner.callers[0] != "" {
		t.Errorf("callers = %v, want [\"\"] — the token caller has no UserID and the "+
			"launcher must not invent one", runner.callers)
	}
}

// TestHandleJob_DuplicateGuardIsScopedToTheCaller.
//
// inFlightRun answers "is this job already running?" and was left reading
// cfg.Runs when every other data route moved to the per-caller view. That was
// harmless only while every launch was operator-scoped: once launches are
// per-tenant, an operator run of `optimize` would block a member's, and a
// member's would be invisible to the guard entirely.
func TestHandleJob_DuplicateGuardIsScopedToTheCaller(t *testing.T) {
	// alice has a RUNNING optimize; bob has nothing.
	tenants := runsPerTenant{
		"alice": []Run{{ID: "r1", Command: "optimize", Status: "RUNNING", StartedAt: nowRFC3339()}},
	}
	runner := &recordingRunner{}
	cfg := Config{Jobs: runner, Tenants: tenants}

	rec := httptest.NewRecorder()
	cfg.handleJob(rec, jobRequest(t, "optimize", Caller{UserID: "alice", Role: RoleMember}))
	if rec.Code != http.StatusConflict {
		t.Errorf("alice: status %d, want 409 — her own run is in flight", rec.Code)
	}

	rec = httptest.NewRecorder()
	cfg.handleJob(rec, jobRequest(t, "optimize", Caller{UserID: "bob", Role: RoleMember}))
	if rec.Code != http.StatusAccepted {
		t.Errorf("bob: status %d, want 202 — another tenant's run must not block his", rec.Code)
	}
}

// runsPerTenant is a TenantStores whose Runs store differs per tenant, so the
// duplicate-run guard can be observed to consult the right one.
type runsPerTenant map[UserID][]Run

func (m runsPerTenant) For(_ context.Context, uid UserID) (TenantView, error) {
	return TenantView{Runs: staticRuns(m[uid])}, nil
}

type staticRuns []Run

func (s staticRuns) List(context.Context, int) ([]Run, error) { return []Run(s), nil }
func (s staticRuns) Get(context.Context, string) (*RunDetail, bool, error) {
	return nil, false, nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

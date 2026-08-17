package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

type stubTenants struct {
	users []*lineupapi.User
	err   error
}

func (s stubTenants) ListActive(context.Context) ([]*lineupapi.User, error) {
	return s.users, s.err
}

type launch struct {
	command []string
	env     map[string]string
}

type stubLauncher struct {
	launches []launch
	failFor  map[string]error
}

func (l *stubLauncher) RunWithEnv(_ context.Context, command []string, env map[string]string) (string, error) {
	l.launches = append(l.launches, launch{command: command, env: env})
	if err, ok := l.failFor[env["ROSTERBOT_USER_ID"]]; ok {
		return "", err
	}
	return "task-" + env["ROSTERBOT_USER_ID"], nil
}

func users(ids ...string) []*lineupapi.User {
	out := make([]*lineupapi.User, 0, len(ids))
	for _, id := range ids {
		out = append(out, &lineupapi.User{ID: lineupapi.UserID(id)})
	}
	return out
}

// TestDispatch_OneTaskPerActiveTenant is the shape of the whole feature. One
// ECS task per tenant per job is FORCED, not chosen: auth_client keeps its
// credentials in package-global state and reads them from the process
// environment, so two tenants cannot coexist in one process without forking it.
func TestDispatch_OneTaskPerActiveTenant(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{users: users("alice", "bob")}, launcher: l}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize", "--matchup"}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(l.launches) != 2 {
		t.Fatalf("launched %d tasks, want 2", len(l.launches))
	}
	if res.Launched != 2 || res.Failed != 0 {
		t.Errorf("result = %+v, want 2 launched / 0 failed", res)
	}
}

// TestDispatch_TenantTravelsInTheEnvironmentNeverTheCommand is the constraint
// that a reasonable-looking implementation gets wrong.
//
// infra.go builds JOB_SCHEDULES by space-joining each job's argv;
// entrypoint.sh records the run as CMD="$*"; internal/opsalert looks the
// schedule up by that exact string. Appending "--user alice" would make the
// recorded command stop matching its JOB_SCHEDULES entry, and every Overdue and
// Streak assertion for that job would silently stop firing.
func TestDispatch_TenantTravelsInTheEnvironmentNeverTheCommand(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{users: users("alice")}, launcher: l}
	want := []string{"optimize", "--matchup", "--archive-projections"}

	if _, err := d.dispatch(context.Background(), event{Command: want}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	got := l.launches[0].command
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("command = %q, want %q byte-identical — opsalert matches on this string",
			strings.Join(got, " "), strings.Join(want, " "))
	}
	for _, arg := range got {
		if strings.Contains(arg, "alice") {
			t.Errorf("tenant leaked into argv (%q); it must travel only in the environment", arg)
		}
	}
}

// TestDispatch_SetsBothTenantVariables closes the naming trap.
//
// ROSTERBOT_USER_ID decides the S3 prefixes every PerTenant artifact is written
// under. RUN_USER_ID is what entrypoint.sh puts on the run-ledger RECORD. They
// are different variables read by different code, and setting only the first
// produces correctly-partitioned keys with every record still carrying
// user_id:"" — collapsing all N tenants into one opsalert bucket, which is the
// exact inversion rosterbot-crq.1 was built to prevent.
func TestDispatch_SetsBothTenantVariables(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{users: users("alice")}, launcher: l}

	if _, err := d.dispatch(context.Background(), event{Command: []string{"grade"}}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	env := l.launches[0].env
	if env["ROSTERBOT_USER_ID"] != "alice" {
		t.Errorf("ROSTERBOT_USER_ID = %q; the task would write to the wrong tenant's prefixes",
			env["ROSTERBOT_USER_ID"])
	}
	if env["RUN_USER_ID"] != "alice" {
		t.Errorf("RUN_USER_ID = %q; the ledger record would carry an empty tenant and "+
			"opsalert would grade every tenant as one", env["RUN_USER_ID"])
	}
	if env["RUN_TRIGGER"] != "schedule" {
		t.Errorf("RUN_TRIGGER = %q, want schedule", env["RUN_TRIGGER"])
	}
}

// TestDispatch_OneTenantsFailureDoesNotStopTheOthers: a RunTask failure is
// per-tenant (a capacity or throttle error), so it must not become an outage
// for everyone else on the schedule.
func TestDispatch_OneTenantsFailureDoesNotStopTheOthers(t *testing.T) {
	l := &stubLauncher{failFor: map[string]error{"bob": errors.New("throttled")}}
	d := dispatcher{tenants: stubTenants{users: users("alice", "bob", "carol")}, launcher: l}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(l.launches) != 3 {
		t.Fatalf("attempted %d launches, want all 3 attempted", len(l.launches))
	}
	if res.Launched != 2 || res.Failed != 1 {
		t.Errorf("result = %+v, want 2 launched / 1 failed", res)
	}
}

// TestDispatch_PartialFailureIsNotReturnedAsAnError is the one that looks
// wrong and is not.
//
// Returning an error makes Lambda's async invoke path RETRY the whole
// dispatch. Every tenant that already launched would get a SECOND task for the
// same job — two concurrent `optimize` runs applying lineups to the same
// roster. Duplicate roster writes are strictly worse than a delayed alert, and
// a tenant that failed to launch is exactly what opsalert.Overdue exists to
// notice.
//
// The residual gap is real and belongs to rosterbot-crq.4: Overdue discovers
// tenants FROM THE LEDGER, so a tenant whose launch fails on every attempt has
// no record to be missed from and stays invisible.
func TestDispatch_PartialFailureIsNotReturnedAsAnError(t *testing.T) {
	l := &stubLauncher{failFor: map[string]error{
		"alice": errors.New("throttled"),
		"bob":   errors.New("throttled"),
	}}
	d := dispatcher{tenants: stubTenants{users: users("alice", "bob")}, launcher: l}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
	if err != nil {
		t.Fatalf("every launch failed and dispatch returned %v; a retry would double-launch "+
			"any tenant that HAD succeeded", err)
	}
	if res.Failed != 2 {
		t.Errorf("result = %+v, want 2 failed", res)
	}
}

// TestDispatch_NoActiveTenantsIsAQuietSuccess: a deployment between pilots has
// nobody to run for. That is a valid state, not a failure, and must not page.
func TestDispatch_NoActiveTenantsIsAQuietSuccess(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{}, launcher: l}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(l.launches) != 0 || res.Launched != 0 {
		t.Errorf("launched %d tasks for zero tenants", len(l.launches))
	}
}

// TestDispatch_ListFailureIsAnError, unlike a launch failure.
//
// The two are not symmetrical. A launch failure is per-tenant and the others
// still ran, so retrying would duplicate their work. A list failure means NO
// tenant was dispatched, so there is nothing to duplicate and a retry is the
// correct and only recovery.
func TestDispatch_ListFailureIsAnError(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{err: errors.New("dynamo down")}, launcher: l}

	if _, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}}); err == nil {
		t.Fatal("a failed tenant listing must be retryable; nothing was launched to duplicate")
	}
	if len(l.launches) != 0 {
		t.Errorf("launched %d tasks despite an unusable tenant list", len(l.launches))
	}
}

// TestDispatch_EmptyCommandIsRejected: the command comes from the EventBridge
// rule's static input, so an empty one is a CDK wiring mistake. Failing loudly
// beats launching a task that runs the container's default entrypoint against
// every tenant.
func TestDispatch_EmptyCommandIsRejected(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{users: users("alice")}, launcher: l}

	if _, err := d.dispatch(context.Background(), event{}); err == nil {
		t.Fatal("expected an empty command to be refused")
	}
	if len(l.launches) != 0 {
		t.Errorf("launched %d tasks with no command", len(l.launches))
	}
}

// TestDispatch_SkipsATenantWithNoID guards against a malformed directory row
// launching a task with an empty ROSTERBOT_USER_ID — which would not fail, it
// would write to the UN-SEGMENTED legacy prefix and silently mix that tenant's
// state into the pre-migration layout.
func TestDispatch_SkipsATenantWithNoID(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{tenants: stubTenants{users: users("alice", "")}, launcher: l}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(l.launches) != 1 {
		t.Fatalf("launched %d tasks, want 1 — the empty tenant must be skipped", len(l.launches))
	}
	if res.Skipped != 1 {
		t.Errorf("result = %+v, want 1 skipped", res)
	}
}

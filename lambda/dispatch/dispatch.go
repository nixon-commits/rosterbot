package main

import (
	"context"
	"fmt"
	"log"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// event is the EventBridge rule's static input. The rule carries the job's
// argv; the dispatcher supplies the tenant.
type event struct {
	Command []string `json:"command"`
}

// result is what one dispatch did, for the log line and for tests.
type result struct {
	Launched int
	Failed   int
	Skipped  int
}

// tenantLister enumerates the tenants a scheduled job should run for.
type tenantLister interface {
	ListActive(ctx context.Context) ([]*lineupapi.User, error)
}

// connectionChecker answers whether a tenant's Fantrax connection can carry a
// run. Satisfied by ddbuser.Store, the same store tenantLister already is.
type connectionChecker interface {
	GetConnection(ctx context.Context, id lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error)
}

// taskLauncher launches one tenant's task.
type taskLauncher interface {
	RunWithEnv(ctx context.Context, command []string, env map[string]string) (string, error)
}

// rosterPublisher stores the active tenant list for the ops notifier
// (rosterbot-crq.4).
type rosterPublisher interface {
	PutJSON(ctx context.Context, key string, data []byte) error
}

// dispatcher turns one scheduled event into one ECS task per active tenant.
//
// One task per tenant per job is FORCED rather than chosen: auth_client holds
// its credentials in package-global state and reads them from the process
// environment, so two tenants cannot share a process without forking it. That
// same constraint is why the tenant is passed as environment rather than as an
// argument — see dispatch below.
type dispatcher struct {
	tenants  tenantLister
	launcher taskLauncher
	roster   rosterPublisher

	// conns gates the fan-out on connection state and is REQUIRED — dispatch
	// errors without it. Optional wiring here would be the fallback-is-a-
	// valid-value shape: a dispatcher built without the store would silently
	// launch every tenant, reintroducing the page-storm the gate exists to
	// stop.
	conns connectionChecker
}

func (d dispatcher) dispatch(ctx context.Context, ev event) (result, error) {
	var res result

	if len(ev.Command) == 0 {
		// The command comes from the rule's static input, so this is a CDK
		// wiring mistake rather than anything runtime. Refusing loudly beats
		// launching the container's default entrypoint once per tenant.
		return res, fmt.Errorf("dispatch: rule supplied no command")
	}

	if d.conns == nil {
		// A wiring mistake, like an empty command: without the connection
		// store the gate below silently admits everyone.
		return res, fmt.Errorf("dispatch: no connection store wired")
	}

	tenants, err := d.tenants.ListActive(ctx)
	if err != nil {
		// RETRYABLE, unlike a launch failure below. Nothing was dispatched, so
		// there is nothing a retry could duplicate, and a retry is the only
		// recovery available.
		return res, fmt.Errorf("dispatch: list active tenants: %w", err)
	}

	// The connection gate: a pre-flight of the refusal every launched task
	// applies anyway (AuthorizeRun). A tenant who has never connected, is
	// mid-connect, or needs to reconnect structurally cannot run — launching
	// them buys a guaranteed FAILED record and a Started/Escalated ops push
	// per (command, tenant), starting the moment their invite is minted. Their
	// state stays visible where it belongs: the admin Tenants tab and their
	// own settings page.
	//
	// A connection-read FAILURE launches (fail open): the task's own
	// AuthorizeRun is the authority and fails loudly, while a DynamoDB blip
	// silently skipping every tenant would be the invisible direction.
	launchable := make([]*lineupapi.User, 0, len(tenants))
	for _, u := range tenants {
		if u == nil || u.ID == "" {
			// An empty tenant would not fail — it would write to the
			// un-segmented legacy prefix and quietly mix this tenant's state
			// into the pre-migration layout.
			res.Skipped++
			log.Printf("dispatch: skipping a directory row with no user id")
			continue
		}
		conn, ok, cerr := d.conns.GetConnection(ctx, u.ID)
		if cerr != nil {
			log.Printf("dispatch: tenant %s: connection read failed (%v); launching anyway — "+
				"the task's own gate decides", u.ID, cerr)
		} else if !ok || !conn.Usable() {
			status := "never connected"
			if ok {
				status = string(conn.Status)
			}
			res.Skipped++
			log.Printf("dispatch: tenant %s: skipped — Fantrax connection %s; the run could only refuse", u.ID, status)
			continue
		}
		launchable = append(launchable, u)
	}

	// The roster carries only the tenants this dispatch actually launches:
	// it feeds opsalert.Overdue's never-ran discovery, and publishing every
	// active tenant would make the heartbeat page for exactly the
	// never-connected testers the gate just stopped paging about.
	d.publishRoster(ctx, launchable)

	for _, u := range launchable {
		uid := string(u.ID)

		// BOTH VARIABLES, DELIBERATELY. ROSTERBOT_USER_ID decides the S3
		// prefixes every PerTenant artifact is written under; RUN_USER_ID is
		// what entrypoint.sh stamps on the run-ledger record. Setting only the
		// first yields correctly-partitioned keys with every record carrying
		// user_id:"", which collapses all N tenants into one opsalert bucket —
		// a permanently-failing tenant would then grade healthy on its
		// neighbours' successes (rosterbot-crq.1).
		//
		// The COMMAND is passed through untouched. infra.go builds
		// JOB_SCHEDULES by space-joining argv and opsalert looks the schedule
		// up by that exact string, so adding a --user flag here would silently
		// switch off every Overdue and Streak assertion for the job.
		task, err := d.launcher.RunWithEnv(ctx, ev.Command, map[string]string{
			"ROSTERBOT_USER_ID": uid,
			"RUN_USER_ID":       uid,
			"RUN_TRIGGER":       "schedule",
		})
		if err != nil {
			res.Failed++
			log.Printf("dispatch: tenant %s: launch failed: %v", uid, err)
			continue
		}
		res.Launched++
		log.Printf("dispatch: tenant %s: launched task %s", uid, task)
	}

	return res, nil
}

// publishRoster stores the active tenant list so opsalert.Overdue can assert on
// a tenant that has NEVER run (rosterbot-crq.4). Without it Overdue derives its
// tenant set from the ledger and can only ever surface a tenant that ran and
// then stopped — an onboarded tenant whose job never launched once is silent.
//
// BEST EFFORT, ALWAYS. A storage failure must never stop the launches below:
// the roster improves an alerting check, while the launches are the job itself,
// and trading the second for the first would be exactly backwards. Publishing
// here rather than on a schedule of its own is what keeps it fresh for free —
// the dispatcher has just read the authoritative list in order to do its work.
func (d dispatcher) publishRoster(ctx context.Context, tenants []*lineupapi.User) {
	if d.roster == nil {
		return
	}
	ids := make([]string, 0, len(tenants))
	for _, u := range tenants {
		if u != nil && u.ID != "" {
			ids = append(ids, string(u.ID))
		}
	}
	data, err := opsalert.MarshalRoster(ids)
	if err != nil {
		log.Printf("dispatch: could not encode tenant roster: %v", err)
		return
	}
	if err := d.roster.PutJSON(ctx, opsalert.RosterKey, data); err != nil {
		log.Printf("dispatch: could not publish tenant roster (%v); the heartbeat falls back "+
			"to its ledger-derived tenant set", err)
	}
}

// handle is the Lambda entry point.
//
// IT RETURNS NIL EVEN WHEN EVERY LAUNCH FAILED, and that is the deliberate
// half. Returning an error puts the invocation on Lambda's async retry path,
// which re-runs the WHOLE dispatch — so every tenant that already launched gets
// a second task for the same job, meaning two concurrent `optimize` runs
// applying lineups to the same roster. Duplicate roster writes are worse than a
// delayed alert, and a tenant that did not run is precisely what
// opsalert.Overdue exists to notice.
//
// That trade is only safe because the missed run is genuinely noticed. A tenant
// whose launch fails on EVERY attempt writes no ledger record at all, so
// Overdue could not have discovered it from run history — which is why
// publishRoster above exists (rosterbot-crq.4). The roster is what makes "no
// record" mean "did not run" rather than "not a tenant".
func (d dispatcher) handle(ctx context.Context, ev event) error {
	res, err := d.dispatch(ctx, ev)
	if err != nil {
		return err
	}
	log.Printf("dispatch: command=%q launched=%d failed=%d skipped=%d",
		ev.Command, res.Launched, res.Failed, res.Skipped)
	return nil
}

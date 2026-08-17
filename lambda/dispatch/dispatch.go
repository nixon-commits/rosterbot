package main

import (
	"context"
	"fmt"
	"log"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
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

// taskLauncher launches one tenant's task.
type taskLauncher interface {
	RunWithEnv(ctx context.Context, command []string, env map[string]string) (string, error)
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
}

func (d dispatcher) dispatch(ctx context.Context, ev event) (result, error) {
	var res result

	if len(ev.Command) == 0 {
		// The command comes from the rule's static input, so this is a CDK
		// wiring mistake rather than anything runtime. Refusing loudly beats
		// launching the container's default entrypoint once per tenant.
		return res, fmt.Errorf("dispatch: rule supplied no command")
	}

	tenants, err := d.tenants.ListActive(ctx)
	if err != nil {
		// RETRYABLE, unlike a launch failure below. Nothing was dispatched, so
		// there is nothing a retry could duplicate, and a retry is the only
		// recovery available.
		return res, fmt.Errorf("dispatch: list active tenants: %w", err)
	}

	for _, u := range tenants {
		if u == nil || u.ID == "" {
			// An empty tenant would not fail — it would write to the
			// un-segmented legacy prefix and quietly mix this tenant's state
			// into the pre-migration layout.
			res.Skipped++
			log.Printf("dispatch: skipping a directory row with no user id")
			continue
		}
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
// The residual gap is real and belongs to rosterbot-crq.4: Overdue discovers
// tenants from the ledger, so a tenant whose launch fails on EVERY attempt
// never writes a record and stays invisible. Closing that needs the active
// roster fed to the notifier, which is why the ticket asks for it alongside
// this change.
func (d dispatcher) handle(ctx context.Context, ev event) error {
	res, err := d.dispatch(ctx, ev)
	if err != nil {
		return err
	}
	log.Printf("dispatch: command=%q launched=%d failed=%d skipped=%d",
		ev.Command, res.Launched, res.Failed, res.Skipped)
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

// heartbeatDetailType is the detail-type the CDK's scheduled rule puts on its
// constant input. It is not an AWS event type — EventBridge scheduled rules
// deliver whatever object the target's input transformer says — so it is
// namespaced to make that obvious next to the two real AWS sources.
const heartbeatDetailType = "Rosterbot Heartbeat"

// schedulesEnv carries the job cadence table, serialized by the CDK from the
// same `jobs` slice that declares the EventBridge rules. Passing it through the
// environment rather than restating it in Go is what makes drift impossible:
// there is one table, and the deployment that owns the crons owns it.
const schedulesEnv = "JOB_SCHEDULES"

// schedules is parsed once at cold start; nil means the heartbeat is not
// configured and every heartbeat tick is a no-op.
var schedules []opsalert.Schedule

// now is the clock seam, replaced in tests.
var now = time.Now

// loadSchedules reads the cadence table from the environment. A malformed table
// is logged and dropped rather than fatal: the failure and crash paths are the
// load-bearing ones, and a typo in the heartbeat's config must not take them
// down with it.
func loadSchedules() []opsalert.Schedule {
	raw := os.Getenv(schedulesEnv)
	if raw == "" {
		return nil
	}
	var out []opsalert.Schedule
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("parse %s: %v (heartbeat disabled)", schedulesEnv, err)
		return nil
	}
	return out
}

// handleHeartbeat asserts that every scheduled job has run within its cadence,
// and alerts once per outage for those that have not.
//
// This is the one path that can see a job which never launched. Every other
// alert in this Lambda is reactive — an ECS task has to stop for handleTask to
// hear about it — so a disabled rule, a broken cron or an unreachable cluster
// produces perfect silence, which is indistinguishable from perfect health.
//
// It returns nil on a send failure rather than propagating, because an
// async-invoke retry would recompute every overdue job and re-send the ones that
// already got through. The next tick covers a genuinely missed one.
// activeRoster reads the tenant list the fan-out dispatcher publishes.
//
// Every failure returns nil, and nil is a supported answer: Overdue falls back
// to its ledger-derived tenant set, which is exactly what it used before
// rosterbot-crq.4. The roster narrows a blind spot, so failing the heartbeat
// over it would trade that narrow gap for total silence — the one failure this
// whole check exists to prevent.
func activeRoster(ctx context.Context) []string {
	if rosterBlob == nil {
		return nil
	}
	data, found, err := rosterBlob.Get(ctx, opsalert.RosterKey)
	if err != nil || !found {
		if err != nil {
			log.Printf("heartbeat: tenant roster unreadable (%v); using ledger-derived tenants", err)
		}
		return nil
	}
	return opsalert.ParseRoster(data)
}

func handleHeartbeat(ctx context.Context) error {
	if len(schedules) == 0 {
		log.Printf("no %s configured; heartbeat is a no-op", schedulesEnv)
		return nil
	}
	if ledger == nil {
		log.Printf("no ledger reader configured (STATE_BUCKET unset); heartbeat is a no-op")
		return nil
	}

	t := now().UTC()
	recs, err := ledger.since(ctx, opsalert.Horizon(schedules, t))
	if err != nil {
		return err
	}

	missed := opsalert.Overdue(recs, schedules, activeRoster(ctx), t)
	if len(missed) == 0 {
		log.Printf("heartbeat: all %d jobs within cadence", len(schedules))
		return nil
	}

	for _, m := range missed {
		// Unlike the task path, "already alerted" here is not "a marker
		// exists" — the marker outlives any single run, so what it stores is
		// which run the last alert was about.
		prev, _ := markers.token(ctx, m.MarkerKey())
		if !m.NeedsAlert(prev) {
			log.Printf("heartbeat: %s%s still overdue, already reported (%s)",
				logSafe(m.Command), logSafe(opsalert.TenantTag(m.UserID)), logSafe(prev))
			continue
		}
		title, body := opsalert.FormatMissed(m)
		a := alert{key: m.MarkerKey(), note: m.AlertToken(), title: title, body: body}
		if err := send1(ctx, markers, a); err != nil {
			log.Printf("heartbeat send %s%s: %v",
				logSafe(m.Command), logSafe(opsalert.TenantTag(m.UserID)), err)
		}
	}
	return nil
}

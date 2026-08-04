package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nixon-commits/rosterbot/internal/opsalert"
	"github.com/nixon-commits/rosterbot/internal/s3blob/s3blobtest"
)

var hbNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// freezeClock pins now() so cadence arithmetic is deterministic.
func freezeClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })
}

// withSchedules installs a cadence table for the duration of the test.
func withSchedules(t *testing.T, s []opsalert.Schedule) {
	t.Helper()
	prev := schedules
	schedules = s
	t.Cleanup(func() { schedules = prev })
}

// timedLedger seeds the ledger with records carrying real timestamps, newest
// first. Unlike fakeLedger's records these must have StartedAt, because the
// heartbeat's whole question is "how long ago".
func timedLedger(t *testing.T, recs []opsalert.Record) *s3blobtest.Fake {
	t.Helper()
	objects := map[string][]byte{}
	for i, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		objects[fmt.Sprintf("runledger/%010d-%s.json", i, r.ID)] = b
	}
	fake := s3blobtest.With(objects)
	prev := ledger
	ledger = &ledgerReader{blob: fake.Blob("state", "runledger/")}
	t.Cleanup(func() { ledger = prev })
	return fake
}

func ranAt(id, command string, ago time.Duration, status string) opsalert.Record {
	return opsalert.Record{
		ID:        id,
		Command:   command,
		Status:    status,
		StartedAt: hbNow.Add(-ago).Format(time.RFC3339),
	}
}

var dailySched = []opsalert.Schedule{{Command: "grade", MaxGapSeconds: 26 * 3600}}

func TestHandleHeartbeat_QuietWhenEveryJobIsWithinCadence(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, dailySched)
	timedLedger(t, []opsalert.Record{ranAt("r1", "grade", 3*time.Hour, opsalert.StatusSuccess)})
	got := capture(t)
	fakeMarkers(t)

	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0: %v", len(*got), *got)
	}
}

func TestHandleHeartbeat_AlertsOnAnOverdueJob(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, dailySched)
	timedLedger(t, []opsalert.Record{ranAt("r1", "grade", 40*time.Hour, opsalert.StatusSuccess)})
	got := capture(t)
	fakeMarkers(t)

	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	for _, want := range []string{"grade has not run", "40h ago", "26h"} {
		if !strings.Contains((*got)[0], want) {
			t.Errorf("send %q missing %q", (*got)[0], want)
		}
	}
}

// The case the failure path structurally cannot see: nothing ever launched, so
// no ECS event fired and handleTask was never called at all.
func TestHandleHeartbeat_AlertsWhenAJobHasNeverRun(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, dailySched)
	timedLedger(t, nil)
	got := capture(t)
	fakeMarkers(t)

	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
	if !strings.Contains((*got)[0], "no run on record") {
		t.Errorf("send = %q", (*got)[0])
	}
}

// One alert per outage, not one per tick — the property that keeps a job dead
// for a week from producing 28 pushes.
func TestHandleHeartbeat_AlertsOncePerOutage(t *testing.T) {
	withSchedules(t, dailySched)
	timedLedger(t, []opsalert.Record{ranAt("r1", "grade", 40*time.Hour, opsalert.StatusSuccess)})
	got := capture(t)
	fakeMarkers(t)

	for i := 0; i < 5; i++ {
		freezeClock(t, hbNow.Add(time.Duration(i)*6*time.Hour))
		if err := handleHeartbeat(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends across 5 ticks, want 1: %v", len(*got), *got)
	}
}

// The subtle half of "once per outage": eventually the last run falls outside
// the lookback horizon and the job reads as "never ran". A dedup key derived
// from that run would move at exactly that moment and push a second time — once
// per horizon, forever — which is what this test pins against.
func TestHandleHeartbeat_StaysQuietAfterTheLastRunAgesOutOfTheHorizon(t *testing.T) {
	withSchedules(t, dailySched)
	timedLedger(t, []opsalert.Record{ranAt("r1", "grade", 40*time.Hour, opsalert.StatusSuccess)})
	got := capture(t)
	fakeMarkers(t)

	freezeClock(t, hbNow)
	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends on the first tick, want 1", len(*got))
	}

	// Horizon is 2x the 26h cadence, so by now the 40h-old run is out of view
	// and Overdue reports the job as having no run on record at all.
	freezeClock(t, hbNow.Add(48*time.Hour))
	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1 — the outage never ended: %v", len(*got), *got)
	}
}

// ...and a *later* outage must still alert, or the first one silences the job
// forever.
func TestHandleHeartbeat_ASecondOutageAlertsAgain(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, dailySched)
	got := capture(t)
	fakeMarkers(t)

	timedLedger(t, []opsalert.Record{ranAt("r1", "grade", 40*time.Hour, opsalert.StatusSuccess)})
	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The job recovered (r2) and then went dark again.
	timedLedger(t, []opsalert.Record{ranAt("r2", "grade", 40*time.Hour, opsalert.StatusSuccess)})
	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 2 {
		t.Fatalf("got %d sends, want 2 (one per outage): %v", len(*got), *got)
	}
}

// A weekly job's healthy run sits deep behind the hourly job's records. A
// count-bounded read would drop it and report a live job as never having run;
// the time-bounded horizon is what prevents that.
func TestHandleHeartbeat_WeeklyJobSurvivesABusyHourlyLedger(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, []opsalert.Schedule{
		{Command: "optimize --matchup", MaxGapSeconds: 13 * 3600},
		{Command: "recap-site --out dist", MaxGapSeconds: 8 * 86400},
	})

	// Six days of hourly runs, newest first, then the weekly run behind them.
	var recs []opsalert.Record
	for i := 0; i < 6*24; i++ {
		recs = append(recs, ranAt(fmt.Sprintf("h%03d", i), "optimize --matchup",
			time.Duration(i)*time.Hour, opsalert.StatusSuccess))
	}
	recs = append(recs, ranAt("weekly", "recap-site --out dist", 6*24*time.Hour, opsalert.StatusSuccess))
	timedLedger(t, recs)

	got := capture(t)
	fakeMarkers(t)
	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("got %d sends, want 0 — the weekly run is 6 days old and healthy: %v", len(*got), *got)
	}
}

// Every job dark at once is the disabled-schedules case, and each one is worth
// naming: an operator seeing 13 alerts knows it is the cluster, not the job.
func TestHandleHeartbeat_AlertsPerJobWhenEverythingIsDark(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, []opsalert.Schedule{
		{Command: "grade", MaxGapSeconds: 26 * 3600},
		{Command: "waivers", MaxGapSeconds: 26 * 3600},
		{Command: "claims", MaxGapSeconds: 26 * 3600},
	})
	timedLedger(t, nil)
	got := capture(t)
	fakeMarkers(t)

	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 3 {
		t.Fatalf("got %d sends, want 3: %v", len(*got), *got)
	}
}

// A misconfigured heartbeat must not take the failure path down with it.
func TestHandleHeartbeat_NoSchedulesIsAQuietNoOp(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, nil)
	timedLedger(t, nil)
	got := capture(t)
	fakeMarkers(t)

	if err := handleHeartbeat(context.Background()); err != nil {
		t.Fatalf("want a quiet no-op, got %v", err)
	}
	if len(*got) != 0 {
		t.Errorf("got %d sends, want 0", len(*got))
	}
}

func TestLoadSchedules(t *testing.T) {
	t.Run("parses the CDK's table", func(t *testing.T) {
		t.Setenv(schedulesEnv, `[{"command":"grade","max_gap_seconds":93600}]`)
		got := loadSchedules()
		if len(got) != 1 || got[0].Command != "grade" || got[0].MaxGap() != 26*time.Hour {
			t.Errorf("loadSchedules() = %+v", got)
		}
	})

	t.Run("a malformed table disables the heartbeat rather than crashing", func(t *testing.T) {
		t.Setenv(schedulesEnv, `{not json`)
		if got := loadSchedules(); got != nil {
			t.Errorf("loadSchedules() = %+v, want nil", got)
		}
	})

	t.Run("unset is not configured", func(t *testing.T) {
		t.Setenv(schedulesEnv, "")
		if got := loadSchedules(); got != nil {
			t.Errorf("loadSchedules() = %+v, want nil", got)
		}
	})
}

// The scheduled rule's constant input has to reach handleHeartbeat through the
// same envelope decode the two AWS sources use.
func TestDispatch_RoutesHeartbeat(t *testing.T) {
	freezeClock(t, hbNow)
	withSchedules(t, dailySched)
	timedLedger(t, []opsalert.Record{ranAt("r1", "grade", 40*time.Hour, opsalert.StatusSuccess)})
	got := capture(t)
	fakeMarkers(t)

	ev := `{"version":"0","detail-type":"Rosterbot Heartbeat","source":"rosterbot.ops","detail":{}}`
	if err := dispatch(context.Background(), json.RawMessage(ev)); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("got %d sends, want 1: %v", len(*got), *got)
	}
}

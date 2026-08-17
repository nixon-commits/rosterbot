package opsalert

import (
	"testing"
	"time"
)

func at(ts string) string { return ts }

func ranAt(cmd, uid, ts string) Record {
	return Record{ID: cmd + "-" + uid, Command: cmd, UserID: uid, Status: "SUCCESS", StartedAt: at(ts)}
}

func perTenant(cmd string, gapHours int) Schedule {
	return Schedule{Command: cmd, MaxGapSeconds: gapHours * 3600, PerTenant: true}
}

func singleton(cmd string, gapHours int) Schedule {
	return Schedule{Command: cmd, MaxGapSeconds: gapHours * 3600}
}

func missedFor(ms []Missed, cmd, uid string) bool {
	for _, m := range ms {
		if m.Command == cmd && m.UserID == uid {
			return true
		}
	}
	return false
}

// TestOverdue_RosterDiscoversATenantThatNeverRan is rosterbot-crq.4.
//
// Overdue derived its tenant set from the ledger, so it could only ever report
// a tenant that ran and then stopped. A newly onboarded tenant whose job never
// launched even once — a misconfigured fan-out, a directory row the dispatcher
// skipped — produced no record, was never discovered, and was therefore
// perfectly silent. That is the exact blindness this check exists to remove,
// one level down.
func TestOverdue_RosterDiscoversATenantThatNeverRan(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC)
	recs := []Record{ranAt("optimize", "alice", "2026-08-17T19:00:00Z")}
	scheds := []Schedule{perTenant("optimize", 13)}

	// bob is active in the directory but has never produced a record.
	missed := Overdue(recs, scheds, []string{"alice", "bob"}, now)

	if !missedFor(missed, "optimize", "bob") {
		t.Errorf("bob was not reported; a tenant whose job never launched stays invisible")
	}
	if missedFor(missed, "optimize", "alice") {
		t.Errorf("alice reported overdue despite running an hour ago")
	}
}

// TestOverdue_RosterDoesNotApplyToSingletonJobs is the trap.
//
// Eleven of the sixteen schedules are league-wide: waivers, transactions,
// gs-check, archive, team-values and the rest run ONCE, not once per tenant.
// Unioning the roster into every command would assert those for each tenant,
// and since no tenant will ever run them under their own id, every one would be
// reported permanently dark — a flood of alerts about jobs that are working.
func TestOverdue_RosterDoesNotApplyToSingletonJobs(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC)
	recs := []Record{ranAt("waivers", "", "2026-08-17T13:00:00Z")}
	scheds := []Schedule{singleton("waivers", 26)}

	missed := Overdue(recs, scheds, []string{"alice", "bob"}, now)

	for _, uid := range []string{"alice", "bob"} {
		if missedFor(missed, "waivers", uid) {
			t.Errorf("singleton job waivers asserted for tenant %q; it runs once, not per tenant", uid)
		}
	}
	if len(missed) != 0 {
		t.Errorf("missed = %+v, want none — the singleton ran within its cadence", missed)
	}
}

// TestOverdue_RosterUnionsWithTheLedger: a tenant that has run must still be
// judged on its real last run, not merely "present in the roster". The roster
// ADDS tenants to assert on; it never replaces the ledger's evidence.
func TestOverdue_RosterUnionsWithTheLedger(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC)
	recs := []Record{
		ranAt("optimize", "alice", "2026-08-17T19:00:00Z"), // fresh
		ranAt("optimize", "carol", "2026-08-15T19:00:00Z"), // stale, and NOT in the roster
	}
	scheds := []Schedule{perTenant("optimize", 13)}

	missed := Overdue(recs, scheds, []string{"alice", "bob"}, now)

	if !missedFor(missed, "optimize", "carol") {
		t.Errorf("carol dropped; a tenant that ran and stopped must still be caught even " +
			"after leaving the active roster — that is the original guarantee")
	}
	if !missedFor(missed, "optimize", "bob") {
		t.Errorf("bob not reported from the roster")
	}
	if missedFor(missed, "optimize", "alice") {
		t.Errorf("alice reported despite a fresh run")
	}
}

// TestOverdue_NoRosterIsTheOldBehaviour. The roster reaches the notifier over
// the network and can be missing, empty or stale. None of those may make
// alerting worse than it was: with no roster the function must behave exactly
// as it did when the tenant set came from the ledger alone.
func TestOverdue_NoRosterIsTheOldBehaviour(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC)
	recs := []Record{ranAt("optimize", "alice", "2026-08-15T19:00:00Z")}
	scheds := []Schedule{perTenant("optimize", 13)}

	for _, roster := range [][]string{nil, {}} {
		missed := Overdue(recs, scheds, roster, now)
		if !missedFor(missed, "optimize", "alice") {
			t.Errorf("roster=%v: alice not reported; a missing roster must not weaken the "+
				"ledger-derived check", roster)
		}
		if len(missed) != 1 {
			t.Errorf("roster=%v: missed = %+v, want exactly alice", roster, missed)
		}
	}
}

// TestOverdue_RosterDoesNotResurrectTheUntaggedTenant.
//
// Before fan-out every record carried an empty user_id, and those records age
// out of the horizon after the cutover. Once they are gone the ledger yields
// only real tenants, and the roster must not cause "" to be asserted again —
// that would reinstate the very cutover burst the cutover was supposed to end.
func TestOverdue_RosterDoesNotResurrectTheUntaggedTenant(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC)
	recs := []Record{ranAt("optimize", "alice", "2026-08-17T19:30:00Z")}
	scheds := []Schedule{perTenant("optimize", 13)}

	missed := Overdue(recs, scheds, []string{"alice"}, now)

	if missedFor(missed, "optimize", "") {
		t.Errorf("the empty tenant was asserted after the cutover completed")
	}
	if len(missed) != 0 {
		t.Errorf("missed = %+v, want none", missed)
	}
}

// TestOverdue_APerTenantJobNobodyRunsIsReportedOncePerTenant: with the roster
// available and no records at all, every active tenant is reported — not one
// untagged assertion standing in for all of them.
func TestOverdue_APerTenantJobNobodyRunsIsReportedOncePerTenant(t *testing.T) {
	now := time.Date(2026, 8, 17, 20, 0, 0, 0, time.UTC)
	scheds := []Schedule{perTenant("grade", 26)}

	missed := Overdue(nil, scheds, []string{"alice", "bob"}, now)

	if len(missed) != 2 {
		t.Fatalf("missed = %+v, want one per active tenant", missed)
	}
	if missedFor(missed, "grade", "") {
		t.Errorf("reported the untagged tenant alongside real ones")
	}
}

// TestRosterRoundTrip pins the wire format shared by two modules that cannot
// see each other's code.
func TestRosterRoundTrip(t *testing.T) {
	b, err := MarshalRoster([]string{"alice", "bob"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := ParseRoster(b)
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("round trip = %v, want [alice bob]", got)
	}
}

// TestParseRoster_DegradesToNothingNeverToAnError is the availability rule.
//
// Every caller falls back to the ledger-derived tenant set, so an unreadable
// roster must weaken the check to what it was before crq.4 and no further.
// Surfacing an error would invite a caller to abort the heartbeat, trading a
// narrow blind spot for total silence — the failure this whole check exists to
// prevent.
func TestParseRoster_DegradesToNothingNeverToAnError(t *testing.T) {
	for _, in := range [][]byte{nil, {}, []byte("not json"), []byte("{}"), []byte(`{"user_ids":null}`)} {
		if got := ParseRoster(in); len(got) != 0 {
			t.Errorf("ParseRoster(%q) = %v, want empty", in, got)
		}
	}
}

// TestParseRoster_DropsEmptyIDs: an empty id would be asserted as the untagged
// tenant, resurrecting exactly the pre-fan-out assertion the cutover retires.
func TestParseRoster_DropsEmptyIDs(t *testing.T) {
	b, _ := MarshalRoster([]string{"alice", "", "bob"})
	if got := ParseRoster(b); len(got) != 2 {
		t.Errorf("ParseRoster = %v, want the empty id dropped", got)
	}
}

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/opsalert"
)

type stubConns struct {
	conns map[string]*lineupapi.FantraxConnection
	err   error
}

func (s stubConns) GetConnection(_ context.Context, id lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	c, ok := s.conns[string(id)]
	return c, ok, nil
}

// allConnected is the permissive stub for tests about other properties.
func allConnected(ids ...string) stubConns {
	m := map[string]*lineupapi.FantraxConnection{}
	for _, id := range ids {
		m[id] = &lineupapi.FantraxConnection{Status: lineupapi.ConnVerified}
	}
	return stubConns{conns: m}
}

// TestDispatch_SkipsTenantsWithoutAUsableConnection is the pre-flight of the
// gate every launched task applies anyway (AuthorizeRun): a tenant who has
// never connected, is mid-connect, or needs to reconnect structurally cannot
// run, so launching them buys a guaranteed FAILED ledger record and a Started/
// Escalated ops push per (command, tenant) — the page-storm that would begin
// the moment a pilot invite is minted, before the tester has done anything.
func TestDispatch_SkipsTenantsWithoutAUsableConnection(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{
		tenants: stubTenants{users: users("alice", "bob", "carol", "dave")},
		conns: stubConns{conns: map[string]*lineupapi.FantraxConnection{
			"alice": {Status: lineupapi.ConnVerified},
			"carol": {Status: lineupapi.ConnNeedsReconnect},
			"dave":  {Status: lineupapi.ConnPending},
			// bob: never connected — no record at all.
		}},
		launcher: l,
	}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(l.launches) != 1 || l.launches[0].env["ROSTERBOT_USER_ID"] != "alice" {
		t.Fatalf("launches = %+v, want exactly alice — every other tenant's run "+
			"could only refuse at AuthorizeRun and page the operator", l.launches)
	}
	if res.Launched != 1 || res.Skipped != 3 {
		t.Errorf("result = %+v, want 1 launched / 3 skipped", res)
	}
}

// TestDispatch_LaunchesWhenTheConnectionReadFails: the gate fails OPEN. The
// task's own AuthorizeRun is the authority and fails loudly; a DynamoDB blip
// silently skipping every tenant would be the invisible-failure direction.
func TestDispatch_LaunchesWhenTheConnectionReadFails(t *testing.T) {
	l := &stubLauncher{}
	d := dispatcher{
		tenants:  stubTenants{users: users("alice", "bob")},
		conns:    stubConns{err: errors.New("dynamo down")},
		launcher: l,
	}

	res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(l.launches) != 2 || res.Launched != 2 {
		t.Fatalf("launches = %d (%+v), want 2 — a store hiccup must not silently "+
			"stop every tenant's jobs", len(l.launches), res)
	}
}

// TestDispatch_RefusesANilConnectionStore: the gate is structural, not
// optional. A dispatcher built without it would silently launch every tenant —
// the fallback-is-a-valid-value shape this repo keeps finding — so a missing
// store is a wiring error, loud, like an empty command.
func TestDispatch_RefusesANilConnectionStore(t *testing.T) {
	d := dispatcher{tenants: stubTenants{users: users("alice")}, launcher: &stubLauncher{}}
	if _, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}}); err == nil {
		t.Fatal("dispatch with no connection store succeeded; the gate must be wired, not optional")
	}
}

// TestDispatch_RosterCarriesOnlyLaunchableTenants: the roster feeds
// opsalert.Overdue's "tenant that never ran" discovery. Publishing every
// active tenant would make the heartbeat page for exactly the never-connected
// testers the gate just stopped paging about — the same storm one layer up.
func TestDispatch_RosterCarriesOnlyLaunchableTenants(t *testing.T) {
	l := &stubLauncher{}
	pub := &stubRoster{}
	d := dispatcher{
		tenants: stubTenants{users: users("alice", "bob")},
		conns: stubConns{conns: map[string]*lineupapi.FantraxConnection{
			"alice": {Status: lineupapi.ConnVerified},
		}},
		launcher: l,
		roster:   pub,
	}

	if _, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(pub.puts) != 1 {
		t.Fatalf("published %d rosters, want 1", len(pub.puts))
	}
	got := string(pub.puts[opsalert.RosterKey])
	if !strings.Contains(got, `"alice"`) || strings.Contains(got, `"bob"`) {
		t.Errorf("roster = %s, want alice only — Overdue must not assert on a "+
			"tenant the dispatcher deliberately does not launch", got)
	}
}

package main

import (
	"context"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// TestDispatchAgreesWithSchedulerSkips is a contract test, and it exists
// because the thing it pins had already broken once.
//
// The Infra page must exempt exactly the tenants this dispatcher declines to
// launch for. A tenant nothing runs for has no producer, so judging their
// frozen slice against MaxAge yields a row that is red forever with no action
// able to clear it — rosterbot-5lkj, and rosterbot-1oai before it by the other
// route. lineupapi.SchedulerSkips is the single definition both sides use.
//
// The failure this guards is specific and was NOT hypothetical. The launch gate
// is a CONJUNCTION spread across two statements — ListActive, which filters on
// Status, and then Usable() — and it does not look like one at the call site.
// The Infra half was first written reading only the connection call, which left
// `parked AND ConnVerified` judged. That is not a corner: the Tenants tab parks
// by writing Status alone and never touches the connection record. Every unit
// test on both sides passed, because both encoded the same half-predicate.
//
// So this asserts the two agree ACROSS THE MATRIX rather than on the one case
// somebody happened to think of, and it asserts it by driving the real
// dispatcher rather than by re-reading its source. A future edit to either side
// that reintroduces a half-copy fails here.
//
// It lives in this package because only this module can see both: lambda/ is a
// separate module importing the root, so the dependency runs one way only and
// internal/lineupapi could never import the dispatcher back. Same shape, and
// same reason, as internal/lineupapi/opsalert_contract_test.go.
func TestDispatchAgreesWithSchedulerSkips(t *testing.T) {
	// Every combination that reaches the gate. The connection-read FAILURE is
	// deliberately absent: the two sides fail open in OPPOSITE directions there
	// (dispatch launches, because the task's own AuthorizeRun is the authority
	// and fails loudly; the Infra page judges, because excusing on a blind read
	// blanks its ability to report an outage), and both are correct.
	// SchedulerSkips refuses to answer that case at all — see its doc comment —
	// so asserting agreement on it would be asserting something false.
	cases := []struct {
		name   string
		id     lineupapi.UserID
		status lineupapi.UserStatus
		conn   *lineupapi.FantraxConnection
	}{
		{"active and verified", "tenant", lineupapi.UserActive, &lineupapi.FantraxConnection{Status: lineupapi.ConnVerified}},
		{"active, needs reconnect", "tenant", lineupapi.UserActive, &lineupapi.FantraxConnection{Status: lineupapi.ConnNeedsReconnect}},
		{"active, mid-connect", "tenant", lineupapi.UserActive, &lineupapi.FantraxConnection{Status: lineupapi.ConnPending}},
		{"active, never connected", "tenant", lineupapi.UserActive, nil},

		// The half the Infra page originally missed. A parked tenant's
		// connection is perfectly fine, so any predicate that consults only the
		// connection store calls this one launchable — and it is not.
		{"parked but verified", "tenant", lineupapi.UserParked, &lineupapi.FantraxConnection{Status: lineupapi.ConnVerified}},
		{"parked and needs reconnect", "tenant", lineupapi.UserParked, &lineupapi.FantraxConnection{Status: lineupapi.ConnNeedsReconnect}},
		{"parked, never connected", "tenant", lineupapi.UserParked, nil},

		// dispatch's third refusal, and the row this matrix originally missed.
		// Unreachable on the Infra side today — PrefixFor("") is the
		// un-segmented legacy path, so an empty tenant never produces a user=
		// segment to judge — but a matrix that omits it is agreeing by luck,
		// and this test exists precisely because "both sides look the same" was
		// how the parked half went missing.
		{"empty id, active and verified", "", lineupapi.UserActive, &lineupapi.FantraxConnection{Status: lineupapi.ConnVerified}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uid := string(tc.id)
			u := &lineupapi.User{
				ID:     tc.id,
				Email:  "tenant@example.test",
				Role:   lineupapi.RoleMember,
				Status: tc.status,
			}

			conns := stubConns{conns: map[string]*lineupapi.FantraxConnection{}}
			if tc.conn != nil {
				conns.conns[uid] = tc.conn
			}

			// stubTenants stands in for ListActive, so it must apply the same
			// Status filter the real stores do (FileUserStore.ListActive,
			// ddbuser). Handing the dispatcher a parked user it would never
			// have been given is how a contract test quietly stops testing the
			// contract.
			var active []*lineupapi.User
			if u.Status == lineupapi.UserActive {
				active = append(active, u)
			}

			l := &stubLauncher{}
			d := dispatcher{tenants: stubTenants{users: active}, conns: conns, launcher: l}

			res, err := d.dispatch(context.Background(), event{Command: []string{"optimize"}})
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}

			launched := len(l.launches) > 0
			skips := lineupapi.SchedulerSkips(u, tc.conn, tc.conn != nil)

			if launched == skips {
				t.Fatalf("dispatch launched=%v but SchedulerSkips=%v (result %+v).\n"+
					"The Infra page exempts a tenant from its health verdict on "+
					"SchedulerSkips alone, so any disagreement here is a row that "+
					"is either permanently red for a tenant no job runs for, or "+
					"silently green for one whose producer has genuinely died.",
					launched, skips, res)
			}
		})
	}
}

// TestSchedulerSkipsIsNotVacuous guards the direction the table above cannot:
// a predicate hard-wired to one answer would agree with the dispatcher on every
// row where that answer happened to be right, and a `return true` would agree
// on six of the seven. Both values must actually occur.
func TestSchedulerSkipsIsNotVacuous(t *testing.T) {
	active := &lineupapi.User{ID: "u", Status: lineupapi.UserActive}
	verified := &lineupapi.FantraxConnection{Status: lineupapi.ConnVerified}

	if lineupapi.SchedulerSkips(active, verified, true) {
		t.Error("an active tenant with a verified connection is skipped — the " +
			"fan-out launches for them, so exempting their slice from the Infra " +
			"page's verdict would hide a genuinely dead producer")
	}
	if !lineupapi.SchedulerSkips(active, nil, false) {
		t.Error("a tenant with no connection record is not skipped")
	}
}

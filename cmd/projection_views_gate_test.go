package cmd

import "testing"

// TestViewsReportBelongsHere pins the ownership rule for the views report
// (recap-site readership: visitor IPs and attacker-suppliable URIs). It is the
// operator's data — layout.go and projection-site.go both say so — but the
// ProjectionReports schedule fans out once per tenant, and before this gate
// every tenant's run published the same deployment-wide CloudFront log digest
// into its own reports/user=<uid>/ partition, serving the operator's site
// analytics to every pilot tester's Views tab.
func TestViewsReportBelongsHere(t *testing.T) {
	cases := []struct {
		name             string
		tenant, operator string
		want             bool
	}{
		// Local dev: no tenancy anywhere. Views renders as it always has.
		{"local dev, both unset", "", "", true},
		// The operator's own fan-out run: the one partition this data belongs in.
		{"operator tenant run", "op-1", "op-1", true},
		// A member's fan-out run: the leak this gate exists to stop.
		{"member tenant run", "member-2", "op-1", false},
		// Misconfiguration (tenant set, operator env missing) fails CLOSED:
		// privacy over availability — the operator missing their views report
		// shows up as the soft-fail warning; a member receiving it shows up
		// nowhere.
		{"tenant set, operator unset", "member-2", "", false},
	}
	for _, c := range cases {
		if got := viewsReportBelongsHere(c.tenant, c.operator); got != c.want {
			t.Errorf("%s: viewsReportBelongsHere(%q, %q) = %v, want %v",
				c.name, c.tenant, c.operator, got, c.want)
		}
	}
}

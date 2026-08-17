package layout

import "testing"

// TestTenantRosterPrefixIsPinned exists because infra/infra.go scopes the
// dispatcher's and notifier's IAM with a LITERAL "tenants/" rather than
// importing this constant.
//
// That literal is deliberate — infra/ has no dependency on the application
// module, and adding one for a single constant would pull the root's whole
// module graph into infra/go.sum. The trade is that a rename here would leave
// the IAM scoped to a prefix nothing uses.
//
// The consequence of that drift is loud rather than silent (the dispatcher logs
// a failed roster publish and the heartbeat falls back to its ledger-derived
// tenants, losing nothing), but "loud" still means someone has to notice. This
// makes the rename fail here instead, where the message can name the file that
// has to change with it.
func TestTenantRosterPrefixIsPinned(t *testing.T) {
	if got := TenantRoster.S3Prefix; got != "tenants/" {
		t.Fatalf("TenantRoster.S3Prefix = %q, want %q — infra/infra.go hardcodes this "+
			"prefix for the dispatcher's write grant and opsnotify's read grant; "+
			"update tenantRosterPrefix there in the same change", got, "tenants/")
	}
}

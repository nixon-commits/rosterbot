package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// stubDirectory satisfies tenantDirectory: connections from the embedded
// stubConns, the profile from its own fields.
type stubDirectory struct {
	stubConns
	user    *lineupapi.User
	userErr error
}

func (s stubDirectory) GetUser(context.Context, lineupapi.UserID) (*lineupapi.User, bool, error) {
	if s.userErr != nil {
		return nil, false, s.userErr
	}
	return s.user, s.user != nil, nil
}

// TestTenantRunConfig_LowersAutoApplyToTheTenantsConsent pins the pilot's core
// safety property: cfg.AutoApply arrives true (config.Load's single-tenant
// default) and a tenant run must lower it to what THAT tenant consented to —
// false unless they deliberately opted in, and false again on any failure to
// read the answer, because writing to somebody's roster on the strength of a
// DynamoDB hiccup is the wrong direction to fail. Before tenantRunConfig
// existed this lowering lived inside resolveTenantCredentials, which
// hard-builds DynamoDB and KMS clients — no test could fail if the lowering
// was dropped.
func TestTenantRunConfig_LowersAutoApplyToTheTenantsConsent(t *testing.T) {
	uid := lineupapi.UserID("tenant-1")
	verified := func() stubConns {
		return stubConns{conn: connFor(t, uid, lineupapi.ConnVerified)}
	}

	cases := []struct {
		name string
		dir  stubDirectory
		want bool
	}{
		{"tenant has not opted in", stubDirectory{stubConns: verified(),
			user: &lineupapi.User{ID: uid, AutoApply: false}}, false},
		{"tenant deliberately opted in", stubDirectory{stubConns: verified(),
			user: &lineupapi.User{ID: uid, AutoApply: true}}, true},
		{"profile read fails", stubDirectory{stubConns: verified(),
			userErr: errors.New("dynamo down")}, false},
		{"profile missing", stubDirectory{stubConns: verified()}, false},
	}
	for _, c := range cases {
		cfg := envConfig()
		cfg.AutoApply = true // what config.Load hands every Fargate task
		if err := tenantRunConfig(context.Background(), cfg, uid, c.dir, stubOpener{}); err != nil {
			t.Fatalf("%s: tenantRunConfig: %v", c.name, err)
		}
		if cfg.AutoApply != c.want {
			t.Errorf("%s: AutoApply = %v, want %v", c.name, cfg.AutoApply, c.want)
		}
	}
}

// TestTenantRunConfig_RefusalDoesNotReachTheConsentRead: a tenant who may not
// run at all must fail before the consent question is even asked, with the
// config left unusable (applyTenantCredentials' clearing contract).
func TestTenantRunConfig_RefusalDoesNotReachTheConsentRead(t *testing.T) {
	uid := lineupapi.UserID("tenant-1")
	cfg := envConfig()
	cfg.AutoApply = true
	dir := stubDirectory{stubConns: stubConns{}} // never connected

	if err := tenantRunConfig(context.Background(), cfg, uid, dir, stubOpener{}); err == nil {
		t.Fatal("tenantRunConfig succeeded for a never-connected tenant")
	}
	if cfg.Username != "" || cfg.Password != "" {
		t.Error("refusal left operator credentials in the config")
	}
}

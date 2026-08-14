package cmd

import (
	"strings"
	"testing"
)

// TestResolveCredentialMode covers all four states of the two environment
// signals, because one of them is a misconfiguration that must not be quiet.
//
// The signals are IDENTITY_TABLE ("is there a user directory?") and
// ROSTERBOT_USER_ID ("who is this run for?"). Three combinations are ordinary;
// the fourth is the one this whole ticket is about.
func TestResolveCredentialMode(t *testing.T) {
	for _, tc := range []struct {
		name          string
		identityTable string
		tenant        string
		want          credentialMode
		wantErr       bool
	}{
		{
			// Local dev and `serve`: no DynamoDB, no tenancy. The deployment's
			// own FANTRAX_* is the only credential there is, and it is the
			// right one.
			name: "neither configured is single-tenant dev",
			want: credsFromEnv,
		},
		{
			// Fargate under tenancy: both are injected by the task definition.
			name:          "both configured is tenant mode",
			identityTable: "IdentityTable", tenant: "alice",
			want: credsFromTenant,
		},
		{
			// THE DANGEROUS ONE. A user directory exists, so tenancy is real,
			// but this task cannot say who it is running for. Falling back to
			// env here is precisely the failure rosterbot-crq.17 names: the job
			// acts as the OPERATOR against whatever team id it was handed, and
			// reports success. There is no safe default, so it is an error.
			name:          "a user directory with no tenant is a hard error",
			identityTable: "IdentityTable", tenant: "",
			wantErr: true,
		},
		{
			// Benign: tenant-scoped S3 prefixes without a directory to consult.
			// Nothing to look a tenant up in, so env is the only option and it
			// is not a silent substitution for something better.
			name:   "a tenant with no user directory falls back to env",
			tenant: "alice",
			want:   credsFromEnv,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCredentialMode(tc.identityTable, tc.tenant)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected a hard error; a silent env fallback here runs the job as the operator")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveCredentialMode_ErrorNamesTheMissingVariable: the failure is a
// deployment mistake someone has to fix in infra, so the message must say which
// half is missing rather than "misconfigured".
func TestResolveCredentialMode_ErrorNamesTheMissingVariable(t *testing.T) {
	_, err := resolveCredentialMode("IdentityTable", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ROSTERBOT_USER_ID") {
		t.Errorf("error %q does not name ROSTERBOT_USER_ID, the variable to set", err.Error())
	}
}

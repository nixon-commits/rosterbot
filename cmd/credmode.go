package cmd

import "fmt"

// credentialMode says where a run's Fantrax credentials come from.
type credentialMode int

const (
	// credsFromEnv is the single-tenant path: FANTRAX_USERNAME/PASSWORD/TEAM_ID
	// straight from the process environment, as every job did before tenancy.
	credsFromEnv credentialMode = iota

	// credsFromTenant requires a verified connection record for this run's
	// tenant, and uses ONLY that. There is deliberately no fallback from here
	// to credsFromEnv: on Fargate the deployment's own FANTRAX_* secrets are
	// present in every task's environment, so a fallback would keep the job
	// working while silently acting as the operator against whatever team id
	// it was handed (rosterbot-crq.17).
	credsFromTenant
)

func (m credentialMode) String() string {
	if m == credsFromTenant {
		return "tenant"
	}
	return "env"
}

// resolveCredentialMode decides which path a run takes from the two signals the
// deployment provides: whether a user directory exists, and who this run is for.
//
// Three of the four combinations are ordinary. The fourth — a user directory
// with no tenant — is a deployment mistake with NO safe default, and it is the
// one this function exists for. Tenancy is real (there is a directory full of
// users) but the task cannot say which of them it is running for, so falling
// back to the environment would run somebody's scheduled job as the operator
// and report success. It is an error instead.
//
// The reverse, a tenant with no directory, is benign: tenant-scoped S3 prefixes
// with nothing to look a tenant up in. Env is then the only credential that
// exists rather than a silent substitution for a better one.
func resolveCredentialMode(identityTable, tenant string) (credentialMode, error) {
	if identityTable == "" {
		return credsFromEnv, nil
	}
	if tenant == "" {
		return credsFromEnv, fmt.Errorf(
			"IDENTITY_TABLE is set but ROSTERBOT_USER_ID is not: this run cannot say which "+
				"tenant it is for, and using the deployment's FANTRAX_* credentials would run "+
				"it as the operator. Set ROSTERBOT_USER_ID (%s)", "see infra/infra.go")
	}
	return credsFromTenant, nil
}

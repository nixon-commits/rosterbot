package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/config"
	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/ddbuser"
	"github.com/nixon-commits/rosterbot/internal/lineupapi/kmscreds"
	"github.com/nixon-commits/rosterbot/internal/statestore"
)

// resolveTenantCredentials is initApp's one call into this file: it decides
// whether this run is tenant-scoped and, if so, replaces the deployment's
// Fantrax credentials with that tenant's.
//
// It is a no-op in single-tenant deployments and in local dev, where there is
// no user directory to consult, so every existing command keeps working
// unchanged (rosterbot-crq.17).
//
// Two commands deliberately do NOT reach here, and both should stay that way.
// `version-check` builds no config at all — its probe is unauthenticated on
// purpose — so the canary that detects Fantrax API drift keeps running through
// a credential outage, which is exactly when you want to know the difference
// between "our credentials broke" and "Fantrax changed". `archive` never talks
// to Fantrax.
func resolveTenantCredentials(ctx context.Context, cfg *config.Config) error {
	table := os.Getenv("IDENTITY_TABLE")
	mode, err := resolveCredentialMode(table, statestore.Tenant())
	if err != nil {
		return err
	}
	if mode == credsFromEnv {
		return nil
	}

	store, err := ddbuser.New(ctx, table)
	if err != nil {
		return fmt.Errorf("tenant credentials: identity store: %w", err)
	}
	opener, err := kmscreds.NewOpener(ctx)
	if err != nil {
		return fmt.Errorf("tenant credentials: kms opener: %w", err)
	}
	uid := lineupapi.UserID(statestore.Tenant())
	if err := tenantRunConfig(ctx, cfg, uid, store, opener); err != nil {
		return err
	}

	// The ladder runs AFTER the session is installed, so its probe tests the
	// exact cookie this run will use rather than a hypothetical one.
	//
	// The sealer is built here rather than inside the ladder because the task
	// role holds kms:Encrypt for precisely this — writing a refreshed session
	// back so the NEXT run starts from it instead of logging in again.
	sealer, err := kmscreds.NewSealer(ctx, os.Getenv("FANTRAX_CRED_KEY"))
	if err != nil {
		return fmt.Errorf("tenant credentials: kms sealer: %w", err)
	}
	lad := sessionLadder{
		conns:  store,
		sealer: sealer,
		probe:  liveSessionProbe,
		mint:   fantraxLogin,
	}
	return lad.refresh(ctx, uid, cfg)
}

// tenantDirectory is the slice of the identity store a tenant run reads: the
// connection (credentials) and the profile (consent). ddbuser.Store satisfies
// it; tests satisfy it with stubs, which is the point — the consent lowering
// used to live where no test could reach it.
type tenantDirectory interface {
	lineupapi.ConnectionStore
	GetUser(ctx context.Context, id lineupapi.UserID) (*lineupapi.User, bool, error)
}

// tenantRunConfig applies one tenant's credentials AND their consent gate to
// the config, in that order.
//
// CONSENT TO BEING WRITTEN TO IS SEPARATE FROM CONNECTING (rosterbot-18wq).
// A manager who connects has agreed to the bot READING their team. Letting it
// APPLY a lineup is a second decision, and auto_apply is the field that
// records it — false for every new user until a person turns it on.
//
// cfg.AutoApply arrives here true, which is the correct default for the
// single-tenant env path and the wrong one for a tenant, so it is lowered
// rather than raised. A read failure lowers it too: acting on an unknown
// answer would mean writing to somebody's roster on the strength of a
// DynamoDB hiccup, and the safe direction is obvious
// (TestTenantRunConfig_LowersAutoApplyToTheTenantsConsent).
func tenantRunConfig(ctx context.Context, cfg *config.Config, uid lineupapi.UserID,
	dir tenantDirectory, opener lineupapi.Opener) error {

	if err := applyTenantCredentials(ctx, cfg, uid, dir, opener); err != nil {
		return err
	}

	cfg.AutoApply = false
	u, ok, err := dir.GetUser(ctx, uid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tenant %s: could not read the auto-apply preference (%v); "+
			"proposing only\n", uid, err)
	} else if ok {
		cfg.AutoApply = u.AutoApply
	}
	return nil
}

// applyTenantCredentials replaces the deployment's Fantrax credentials in cfg
// with the ones belonging to uid, refusing outright if that tenant is not
// verified.
//
// EVERY FAILURE PATH CLEARS THE CONFIG FIRST. That is not defensive habit: on
// Fargate the deployment's own FANTRAX_USERNAME/PASSWORD/TEAM_ID are injected
// into every task's environment from SSM, so config.Load has already populated
// cfg with the OPERATOR's credentials by the time this runs. Returning an error
// while leaving them in place would mean any caller that failed to check it ran
// a tenant's scheduled job as the operator, against whatever team id it was
// handed — the exact failure rosterbot-crq.17 names. Clearing first makes the
// unchecked path useless rather than dangerous.
//
// It takes its store and opener as parameters rather than building them, so the
// whole decision is testable without DynamoDB or KMS.
func applyTenantCredentials(ctx context.Context, cfg *config.Config, uid lineupapi.UserID,
	conns lineupapi.ConnectionStore, opener lineupapi.Opener) error {

	// Cleared up front, so every `return err` below is safe by construction
	// rather than by remembering to clear at each one.
	cfg.Username, cfg.Password, cfg.TeamID = "", "", ""

	// The ENVIRONMENT is cleared alongside the config, and that is a separate
	// hole rather than belt-and-braces. auth_client reads FANTRAX_USERNAME,
	// FANTRAX_PASSWORD and FANTRAX_COOKIES with os.Getenv and never sees
	// config.Config, while infra.go injects the deployment's own values into
	// EVERY task from SSM. So a refusal that only cleared cfg would leave
	// auth_client able to log in as the operator the moment anything downstream
	// asked it for cookies — the run gate closed the config path and left this
	// one open underneath it.
	setFantraxEnv("", "", "")

	conn, ok, err := conns.GetConnection(ctx, uid)
	if err != nil {
		return fmt.Errorf("tenant %s: read connection: %w", uid, err)
	}
	if !ok {
		conn = nil
	}

	grant, reason := lineupapi.AuthorizeRun(uid, conn)
	if reason != "" {
		return fmt.Errorf("tenant %s may not run: %s", uid, reason)
	}

	plain, err := opener.Open(ctx, uid, grant.Creds)
	if err != nil {
		// Infrastructure, not the user's problem — a wrong key or a missing
		// grant. Deliberately NOT recorded as needs_reconnect, which would tell
		// someone to re-enter credentials that are perfectly good.
		return fmt.Errorf("tenant %s: open credentials: %w", uid, err)
	}
	var creds lineupapi.FantraxCreds
	if err := json.Unmarshal(plain, &creds); err != nil {
		return fmt.Errorf("tenant %s: malformed credential blob", uid)
	}
	if creds.Username == "" || creds.Password == "" {
		// Valid JSON carrying nothing. Refused here so it reads as a credential
		// problem, rather than surfacing three layers down as auth_client's
		// "FANTRAX_USERNAME and FANTRAX_PASSWORD must be set".
		return fmt.Errorf("tenant %s: decrypted credentials are empty", uid)
	}

	// THE SESSION IS THE POINT, not a bonus. Until this, the FX_RM the connect
	// task captured and sealed was written, length-checked, copied into the
	// grant and dropped — a secret with no consumer (rosterbot-crq.17 criterion
	// 4). What actually carried a session between runs was the PLAINTEXT
	// .fantrax_cookie_cache.json synced through S3, which made the KMS envelope
	// the whole design rests on decorative.
	//
	// A decrypt failure here is NOT fatal. The sealed cookie is an optimisation:
	// auth_client can always mint a fresh one from the credentials we just
	// decrypted, so refusing the run would turn a recoverable session problem
	// into an outage. It is logged rather than swallowed, because a cookie that
	// silently stops being used is how this became invisible the first time.
	session, err := opener.Open(ctx, uid, grant.FXRM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tenant %s: sealed session unusable (%v); "+
			"falling back to a fresh login\n", uid, err)
		session = nil
	}

	cfg.Username, cfg.Password = creds.Username, creds.Password
	// The team comes from the grant, which took it from the record the connect
	// task PROVED against Fantrax's own MyTeamIDs. LeagueID is deliberately not
	// touched: the league is deployment-wide, and this pilot is scoped to one.
	cfg.TeamID = grant.TeamID

	// Installed LAST, once every refusal is behind us, so the window in which
	// this process can authenticate as anybody is as short as the function
	// allows.
	setFantraxEnv(creds.Username, creds.Password, fantraxCookieHeader(string(session)))
	return nil
}

// setFantraxEnv installs the process-level Fantrax credentials that auth_client
// reads directly, or clears them when called with empty strings.
//
// It exists because auth_client's entry points take no credential argument:
// GetCookiesWithBrowser reads the username and password from the environment,
// and GetCookies consults FANTRAX_COOKIES before its cache file and before the
// browser. That environment read is also precisely why the fan-out has to be one
// ECS task per tenant per job rather than a loop in one process — two tenants
// sharing a process would share these variables.
func setFantraxEnv(user, pass, cookie string) {
	for _, kv := range []struct{ k, v string }{
		{"FANTRAX_USERNAME", user},
		{"FANTRAX_PASSWORD", pass},
		{"FANTRAX_COOKIES", cookie},
	} {
		if kv.v == "" {
			_ = os.Unsetenv(kv.k)
			continue
		}
		_ = os.Setenv(kv.k, kv.v)
	}
}

// fantraxCookieHeader renders a raw FX_RM value as the Cookie header
// auth_client expects.
//
// The quote unwrapping mirrors convertCookiesToString in the fork, which is what
// builds this header on the path that does not come through us. If the two
// disagreed, a session would work when freshly minted by the browser and fail
// when replayed from the sealed copy — a difference that would look like an
// expiring cookie rather than a formatting bug.
func fantraxCookieHeader(fxrm string) string {
	if fxrm == "" {
		return ""
	}
	if len(fxrm) >= 2 && fxrm[0] == '"' && fxrm[len(fxrm)-1] == '"' {
		fxrm = fxrm[1 : len(fxrm)-1]
	}
	return "FX_RM=" + fxrm
}

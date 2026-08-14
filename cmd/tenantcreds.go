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
	return applyTenantCredentials(ctx, cfg, lineupapi.UserID(statestore.Tenant()), store, opener)
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

	cfg.Username, cfg.Password = creds.Username, creds.Password
	// The team comes from the grant, which took it from the record the connect
	// task PROVED against Fantrax's own MyTeamIDs. LeagueID is deliberately not
	// touched: the league is deployment-wide, and this pilot is scoped to one.
	cfg.TeamID = grant.TeamID
	return nil
}

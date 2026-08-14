package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
	"github.com/nixon-commits/rosterbot/internal/statesync"
	"github.com/spf13/cobra"
)

// sync-down / sync-up are internal commands invoked by entrypoint.sh to warm
// run state from S3 and save it back, replacing the awscli `aws s3 sync` and
// `aws cloudfront create-invalidation` calls so the runtime image no longer
// ships python+awscli. Both are best-effort: a sync hiccup must never fail an
// otherwise-successful job, so per-step errors are logged and the command still
// exits 0 (mirroring the old `|| true` shell wrappers).

type statePair struct {
	dir    string
	prefix string
}

// statePairsFor returns the bulk dir<->prefix mappings under STATE_BUCKET for
// one tenant. The TTL cache (cache/ prefix) is intentionally absent — the bot
// reads/writes it per-key directly via cache.Store.
//
// THE PREFIXES ARE DERIVED FROM THE LAYOUT, NEVER WRITTEN AS LITERALS. This
// sync is the only writer that does not go through statestore's typed
// constructors, so it was the only one that never composed the tenant segment,
// and its prefixes were a second independent statement of a fact the layout
// already owned. Nothing made the two agree, so they didn't.
//
// For session/ the consequence is cross-tenant CREDENTIAL sharing, not
// untidiness: every task syncs .fantrax-cache/ up when it finishes and pulls it
// down when the next one starts, so a single shared session/ prefix means
// tenant B's task boots holding tenant A's Fantrax FX_RM cookie and acts as
// them, with every job reporting success. Measured on the live bucket
// 2026-08-14, session/.fantrax_cookie_cache.json and its user=<uid>/ twin
// carried the same write timestamp — the un-tenanted copy was being actively
// maintained, not left behind by the crq.11 backfill.
//
// backtest/ is the same mechanism with lower stakes: projection snapshots
// mixing between tenants rather than a credential.
//
// claims/ and archive/ are deliberately NOT PerTenant — a league-wide
// transaction ledger and upstream snapshots identical for everyone — and
// PrefixFor leaves them alone, so this stays one rule rather than a list of
// exceptions.
//
// The LOCAL side keeps its plain directory: a task runs for exactly one tenant,
// so the container's own filesystem needs no tenant segment, and adding one
// would move paths every other command already hardcodes.
// syncPairs is what the two commands call. The tenant is read here rather than
// passed in, so a call site has nothing to get wrong — an earlier version took
// it as an argument and a mutation that passed "" at both call sites went
// undetected by every test of statePairsFor itself.
func syncPairs() []statePair { return statePairsFor(statestore.Tenant()) }

func statePairsFor(tenant string) []statePair {
	return []statePair{
		{".fantrax-cache/", layout.Session.PrefixFor(tenant)},
		{".waivers/", layout.Claims.PrefixFor(tenant)},
		{".backtest/", layout.Backtest.PrefixFor(tenant)},
		{".archive/", layout.Archive.PrefixFor(tenant)},
	}
}

var syncDownCmd = &cobra.Command{
	Use:    "sync-down",
	Short:  "Internal: warm run state from S3 (used by entrypoint.sh)",
	Hidden: true,
	RunE:   runSyncDown,
}

var syncUpCmd = &cobra.Command{
	Use:    "sync-up",
	Short:  "Internal: save run state and publish sites to S3 (used by entrypoint.sh)",
	Hidden: true,
	RunE:   runSyncUp,
}

func init() {
	rootCmd.AddCommand(syncDownCmd, syncUpCmd)
}

func runSyncDown(cmd *cobra.Command, args []string) error {
	// Bulk dir<->prefix sync (statePairs), not a per-key store — intentionally
	// distinct from statestore's typed constructors, but the env read is shared.
	bucket := statestore.Bucket()
	if bucket == "" {
		return nil
	}
	s, err := statesync.New(context.Background())
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, p := range syncPairs() {
		if err := s.Down(ctx, bucket, p.prefix, p.dir); err != nil {
			warn("sync-down %s: %v", p.prefix, err)
		}
	}
	return nil
}

func runSyncUp(cmd *cobra.Command, args []string) error {
	s, err := statesync.New(context.Background())
	if err != nil {
		return err
	}
	ctx := context.Background()

	if bucket := statestore.Bucket(); bucket != "" {
		for _, p := range syncPairs() {
			if err := s.Up(ctx, bucket, p.prefix, p.dir, false); err != nil {
				warn("sync-up %s: %v", p.prefix, err)
			}
		}
	}

	// Publish the static sites when present (recap-site writes ./dist,
	// projection-site writes ./report). recap-site.go publishes a full
	// bucket-root mirror with --delete; projection-site's output is
	// published under a "report/" prefix inside the shared dashboard
	// bucket instead (so the SPA's own files aren't touched by the
	// --delete pass) — followed by a CloudFront invalidation so the new
	// pages aren't masked by the distribution's cache TTL.
	publishSite(ctx, s, "./dist", os.Getenv("SITE_BUCKET"), os.Getenv("SITE_CF_DIST_ID"), "")
	publishSite(ctx, s, "./report", os.Getenv("DASHBOARD_BUCKET"), dashboardCFDistID(ctx), "report/")
	return nil
}

// dashboardCFDistID resolves DashboardCdn's distribution ID for the
// projection-site publish's post-upload invalidation. It can't be injected as
// a direct env var (DASHBOARD_CF_DIST_ID) the way SITE_CF_DIST_ID is: a
// CloudFormation reference from the bot's task definition to DashboardCdn
// creates a circular dependency (Task -> DashboardCdn -> LineupApiFunctionUrl
// -> LineupApi -> Task, the Lambda's TASK_DEF env var closing the loop) —
// confirmed live via a rejected CloudFormation changeset. infra.go instead
// publishes the ID into SSM after the distribution exists and hands the task
// only the (static) parameter name via DASHBOARD_CF_DIST_ID_PARAM, mirroring
// lambda/main.go's RP_ID_PARAM/RP_ORIGIN_PARAM fix for the identical cycle
// shape. Soft-fails to "" (no invalidation, matching publishSite's existing
// behavior for an unset distID) on any error — a stale CloudFront cache is a
// self-healing annoyance, not a reason to fail the run.
func dashboardCFDistID(ctx context.Context) string {
	name := os.Getenv("DASHBOARD_CF_DIST_ID_PARAM")
	if name == "" {
		return ""
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		warn("dashboard dist id: load aws config: %v", err)
		return ""
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{Name: &name})
	if err != nil {
		warn("dashboard dist id: fetch %s: %v", name, err)
		return ""
	}
	return *out.Parameter.Value
}

// publishSite mirrors a local site dir into a bucket under prefix (with
// --delete scoped to that prefix) and invalidates its CloudFront
// distribution. No-op when the dir or bucket is absent. prefix must be ""
// for a bucket-root site (recap's SiteBucket) and a non-empty, trailing-
// slash prefix (e.g. "report/") for a site sharing a bucket with other
// content (the dashboard SPA's own files) — an empty prefix against a
// shared bucket would treat the SPA's files as orphans and delete them,
// since Up's --delete pass only spares keys under the given prefix.
func publishSite(ctx context.Context, s *statesync.Syncer, dir, bucket, distID, prefix string) {
	if bucket == "" {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // nothing rendered this run
	}
	if err := s.Up(ctx, bucket, prefix, dir, true); err != nil {
		warn("publish %s: %v", dir, err)
		return
	}
	if distID != "" {
		if err := s.Invalidate(ctx, distID); err != nil {
			warn("invalidate %s: %v", distID, err)
		}
	}
}

func warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "warn: "+format+"\n", a...)
}

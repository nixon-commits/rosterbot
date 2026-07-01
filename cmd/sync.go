package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/nixon-commits/rosterbot/internal/statesync"
	"github.com/spf13/cobra"
)

// sync-down / sync-up are internal commands invoked by entrypoint.sh to warm
// run state from S3 and save it back, replacing the awscli `aws s3 sync` and
// `aws cloudfront create-invalidation` calls so the runtime image no longer
// ships python+awscli. Both are best-effort: a sync hiccup must never fail an
// otherwise-successful job, so per-step errors are logged and the command still
// exits 0 (mirroring the old `|| true` shell wrappers).

// statePairs are the bulk dir<->prefix mappings under STATE_BUCKET. The TTL
// cache (cache/ prefix) is intentionally absent — the bot reads/writes it
// per-key directly via cache.Store.
var statePairs = []struct {
	dir    string
	prefix string
}{
	{".fantrax-cache/", "session/"},
	{".waivers/", "claims/"},
	{".backtest/", "backtest/"},
	{".archive/", "archive/"},
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
	bucket := os.Getenv("STATE_BUCKET")
	if bucket == "" {
		return nil
	}
	s, err := statesync.New(context.Background())
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, p := range statePairs {
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

	if bucket := os.Getenv("STATE_BUCKET"); bucket != "" {
		for _, p := range statePairs {
			if err := s.Up(ctx, bucket, p.prefix, p.dir, false); err != nil {
				warn("sync-up %s: %v", p.prefix, err)
			}
		}
	}

	// Publish the static sites when present (recap-site writes ./dist,
	// projection-site writes ./report). Each is a full-bucket mirror with
	// --delete, followed by a CloudFront invalidation so the new pages aren't
	// masked by the distribution's cache TTL.
	publishSite(ctx, s, "./dist", os.Getenv("SITE_BUCKET"), os.Getenv("SITE_CF_DIST_ID"))
	publishSite(ctx, s, "./report", os.Getenv("REPORT_BUCKET"), os.Getenv("REPORT_CF_DIST_ID"))
	return nil
}

// publishSite mirrors a local site dir into a bucket root (with --delete) and
// invalidates its CloudFront distribution. No-op when the dir or bucket is absent.
func publishSite(ctx context.Context, s *statesync.Syncer, dir, bucket, distID string) {
	if bucket == "" {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // nothing rendered this run
	}
	if err := s.Up(ctx, bucket, "", dir, true); err != nil {
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

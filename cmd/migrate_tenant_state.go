package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/nixon-commits/rosterbot/internal/statestore"
	"github.com/nixon-commits/rosterbot/internal/statestore/layout"
)

var (
	migrateTenantUser  string
	migrateTenantApply bool
)

// migrate-tenant-state relocates the deployment's existing per-tenant state
// under a user=<uid>/ segment, so the single operator's data lives where the
// tenant-scoped stores will look for it (rosterbot-crq.11).
//
// It COPIES; it never moves. The originals stay exactly where they are, so a
// deployment running the pre-cutover code keeps working and a rollback costs
// nothing. Deleting the legacy copies is a separate, later, deliberate act —
// and one worth postponing until the new layout has been serving reads for a
// while, because a bucket holding two copies of 3,000 small objects is a
// rounding error next to being unable to go back.
var migrateTenantStateCmd = &cobra.Command{
	Use:    "migrate-tenant-state",
	Short:  "Internal: copy per-tenant state under user=<uid>/ (rosterbot-crq.11)",
	Hidden: true,
	RunE:   runMigrateTenantState,
}

func init() {
	f := migrateTenantStateCmd.Flags()
	f.StringVar(&migrateTenantUser, "user", "", "user id to file the existing state under (required)")
	f.BoolVar(&migrateTenantApply, "apply", false, "actually copy (default is a dry run)")
	_ = migrateTenantStateCmd.MarkFlagRequired("user")
	rootCmd.AddCommand(migrateTenantStateCmd)
}

func runMigrateTenantState(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	out := cmd.OutOrStdout()

	bucket := statestore.Bucket()
	if bucket == "" {
		return fmt.Errorf("migrate-tenant-state: STATE_BUCKET must be set")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	api := s3.NewFromConfig(cfg)

	var totalToCopy, totalCopied, totalSkipped int
	for _, a := range perTenantArtifacts() {
		n, copied, skipped, err := migrateOnePrefix(ctx, out, api, bucket, a.S3Prefix, migrateTenantUser)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		totalToCopy += n
		totalCopied += copied
		totalSkipped += skipped
	}

	fmt.Fprintln(out)
	if !migrateTenantApply {
		fmt.Fprintf(out, "dry run — nothing written. %d object(s) would be copied, %d already present.\n",
			totalToCopy, totalSkipped)
		fmt.Fprintln(out, "re-run with --apply to copy. Originals are never deleted.")
		return nil
	}
	fmt.Fprintf(out, "copied %d object(s), %d already present. Originals left in place.\n",
		totalCopied, totalSkipped)
	return nil
}

// perTenantArtifacts is every artifact the layout marks PerTenant, read from
// the table rather than listed here so a newly-marked artifact is migrated
// without anyone remembering to add it in two places.
func perTenantArtifacts() []layout.Artifact {
	seen := map[string]bool{}
	var out []layout.Artifact
	all := append(layout.All(), layout.Progress)
	for _, a := range all {
		if !a.PerTenant || seen[a.S3Prefix] {
			continue
		}
		seen[a.S3Prefix] = true
		out = append(out, a)
	}
	return out
}

// migrateOnePrefix copies every legacy object under prefix to
// prefix + "user=<uid>/" + rest.
func migrateOnePrefix(ctx context.Context, out io.Writer, api *s3.Client,
	bucket, prefix, uid string) (todo, copied, skipped int, err error) {

	dstPrefix := prefix + "user=" + uid + "/"

	// Destination keys first, so a re-run skips what is already there rather
	// than re-copying 3,000 objects. Idempotence matters here because the
	// realistic failure is a partial run — a throttle, a dropped connection —
	// and recovery must be "run it again", not manual reconciliation.
	existing := map[string]bool{}
	if err := eachKey(ctx, api, bucket, dstPrefix, func(k string) {
		existing[strings.TrimPrefix(k, dstPrefix)] = true
	}); err != nil {
		return 0, 0, 0, err
	}

	var srcKeys []string
	if err := eachKey(ctx, api, bucket, prefix, func(k string) {
		// Never re-migrate something already filed under a tenant. Without this
		// a second run would produce user=X/user=X/... and quietly double the
		// object count on every invocation.
		if strings.HasPrefix(k, prefix+"user=") {
			return
		}
		srcKeys = append(srcKeys, k)
	}); err != nil {
		return 0, 0, 0, err
	}

	for _, k := range srcKeys {
		rest := strings.TrimPrefix(k, prefix)
		if existing[rest] {
			skipped++
			continue
		}
		todo++
		if !migrateTenantApply {
			continue
		}
		// Server-side copy: the bytes never transit this process.
		_, err := api.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(dstPrefix + rest),
			CopySource: aws.String(url.PathEscape(bucket + "/" + k)),
		})
		if err != nil {
			return todo, copied, skipped, fmt.Errorf("copy %s: %w", k, err)
		}
		copied++
	}

	fmt.Fprintf(out, "%-26s %4d to copy, %4d already present  -> %s\n",
		prefix, todo, skipped, dstPrefix)
	return todo, copied, skipped, nil
}

// eachKey walks every object under prefix, following continuation tokens.
//
// Paginated because an unpaginated listing silently stops at 1,000 keys, and
// several of these prefixes are well past that (runledger/ alone is ~1,600).
// A truncated listing here would not fail — it would migrate a prefix of the
// data and report success.
func eachKey(ctx context.Context, api *s3.Client, bucket, prefix string, fn func(string)) error {
	var token *string
	for {
		page, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		for _, o := range page.Contents {
			fn(aws.ToString(o.Key))
		}
		if page.NextContinuationToken == nil {
			return nil
		}
		token = page.NextContinuationToken
	}
}
